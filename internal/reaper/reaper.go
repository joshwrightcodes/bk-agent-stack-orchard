package reaper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// failedExitStatus is the exit status of a job that the controller ends. The design states it, and
// a pipeline reads it as a failure of the job and not of the agent.
const failedExitStatus = -1

// Stacks is what the executor needs of Buildkite.
type Stacks interface {
	Finish(ctx context.Context, id job.ID, exitStatus int, detail string) error
	Notify(ctx context.Context, notifications []buildkite.Notification) error
}

// States gives what Buildkite said about a job, and when the controller asked.
type States interface {
	Observed(id job.ID) (jobstates.Observation, bool)
}

// Machines is what the reaper needs of Orchard.
type Machines interface {
	DeleteVM(ctx context.Context, name string) error
}

// Fleet gives the last snapshot of the machines.
type Fleet interface {
	Latest() (inventory.Snapshot, bool)
}

// Options configures the reaper.
type Options struct {
	Stacks  Stacks
	States  States
	Orchard Machines
	Fleet   Fleet

	// Profile gives the profile of a job that this process started, and empty text for one that
	// it did not.
	Profile func(job.ID) string

	// Profiles and Defaults give the limits of a machine.
	Profiles []profile.Profile
	Defaults profile.Timeouts

	// Freshness is how old a job state may be and still count.
	Freshness time.Duration

	Metrics *telemetry.Metrics
	Logger  *slog.Logger
	Tracer  trace.Tracer

	// Now is for a test. A nil value gives time.Now.
	Now func() time.Time
}

// Reaper examines the machines of this controller at each tick, and removes the ones that the
// table says must go.
type Reaper struct {
	stacks    Stacks
	states    States
	machines  Machines
	fleet     Fleet
	profile   func(job.ID) string
	profiles  []profile.Profile
	defaults  profile.Timeouts
	freshness time.Duration
	metrics   *telemetry.Metrics
	logger    *slog.Logger
	tracer    trace.Tracer
	now       func() time.Time

	// ignored holds the jobs that Buildkite refused with a 404 or a 422. Every further call about
	// such a job gives the same refusal, so the controller makes none (target design, section 7.1,
	// rule 4). Only the goroutine of the loop reads and writes it.
	ignored map[job.ID]bool

	// warned holds the state that the last warning named for a job whose state matches no row, so
	// that a job stuck in one such state gives one record and not one at every tick.
	warned map[job.ID]string
}

// New builds the reaper.
func New(options Options) (*Reaper, error) {
	var problems []error

	if options.Stacks == nil || options.States == nil || options.Orchard == nil ||
		options.Fleet == nil {
		problems = append(problems, errors.New("the reaper needs Buildkite, the job states, "+
			"Orchard, and the fleet"))
	}

	if options.Metrics == nil || options.Logger == nil {
		problems = append(problems, errors.New("the reaper needs the instruments and a logger"))
	}

	if options.Freshness <= 0 {
		problems = append(problems, fmt.Errorf("the state freshness is %s, and it must be more "+
			"than zero", options.Freshness))
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	tracer := options.Tracer
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("")
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	name := options.Profile
	if name == nil {
		name = func(job.ID) string { return "" }
	}

	return &Reaper{
		stacks:    options.Stacks,
		states:    options.States,
		machines:  options.Orchard,
		fleet:     options.Fleet,
		profile:   name,
		profiles:  options.Profiles,
		defaults:  options.Defaults,
		freshness: options.Freshness,
		metrics:   options.Metrics,
		logger:    options.Logger,
		tracer:    tracer,
		now:       now,
		ignored:   map[job.ID]bool{},
		warned:    map[job.ID]string{},
	}, nil
}

// Reap examines every machine of this controller once, and carries out what the table decides.
//
// An action that fails is a log record and a machine that the next tick tries again: the decision
// reads observed state, so it gives the same answer next time, and nothing here needs to remember
// that it failed.
func (r *Reaper) Reap(ctx context.Context) error {
	snapshot, read := r.fleet.Latest()
	if !read {
		return nil
	}

	now := r.now()
	rules := Rules{Freshness: r.freshness}

	var failures int

	for _, machine := range r.collect(snapshot) {
		if err := r.carry(ctx, machine, Decide(machine, now, rules)); err != nil {
			failures++
		}
	}

	r.forget(snapshot)

	if failures > 0 {
		return fmt.Errorf("%d actions of the reaper failed", failures)
	}

	return nil
}

// collect gives every machine that the table reads: the orphans, and the machines of jobs.
func (r *Reaper) collect(snapshot inventory.Snapshot) []Machine {
	machines := make([]Machine, 0, len(snapshot.Orphans)+len(snapshot.Owned))

	for _, vm := range snapshot.Orphans {
		machines = append(machines, Machine{
			VM: vm, Orphan: true, Timeouts: Timeouts("", r.profiles, r.defaults),
		})
	}

	for _, owned := range snapshot.Owned {
		machine := Machine{
			VM:       owned.VM,
			Job:      owned.Job,
			Timeouts: Timeouts(r.profile(owned.Job), r.profiles, r.defaults),
		}

		// A job that the controller has not asked about has no state, and not an empty one.
		if observation, asked := r.states.Observed(owned.Job); asked {
			machine.State = observation.State
			machine.Named = observation.Named
			machine.Observed = true
			machine.ObservedAt = observation.At
		}

		machines = append(machines, machine)
	}

	return machines
}

// carry carries out one decision.
func (r *Reaper) carry(ctx context.Context, machine Machine, decision Decision) error {
	switch decision.Action {
	case Keep:
		if decision.Row == 0 {
			r.warn(ctx, machine)
		}

		return nil
	case Extend:
		// The extensions of one tick go in one call, which the renewal makes after this pass.
		return nil
	case Delete, FinishAndDelete, NotifyAndDelete:
	}

	ctx, span := r.tracer.Start(telemetry.WithJob(ctx, machine.Job), "reaper.decide",
		trace.WithAttributes(
			attribute.String(telemetry.SpanVMPhase, string(inventory.Phase(machine.VM))),
			attribute.String(telemetry.SpanJobState, machine.State),
			attribute.String(telemetry.SpanDecision, string(decision.Action)),
			attribute.String(telemetry.SpanReason, decision.Reason),
			attribute.Int(telemetry.SpanRow, decision.Row),
		))
	defer span.End()

	fields := []any{
		telemetry.LogJob, string(machine.Job),
		telemetry.LogVM, machine.VM.Name,
		telemetry.LogWorker, machine.VM.Worker,
		telemetry.SpanRow, strconv.Itoa(decision.Row),
		telemetry.SpanReason, decision.Reason,
	}

	switch decision.Action {
	case FinishAndDelete:
		// A machine whose job did not finish stays, so that the next tick finishes the job before
		// the machine goes. Without that, a failed job would wait for its lost-agent timeout.
		if err := r.finish(ctx, machine, decision); err != nil {
			span.RecordError(err)
			r.logger.ErrorContext(ctx, "the reaper could not end a job",
				append(fields, "error", err)...)

			return err
		}
	case NotifyAndDelete:
		r.notify(ctx, machine, decision)
	case Keep, Extend, Delete:
	}

	if err := r.delete(ctx, machine); err != nil {
		span.RecordError(err)
		r.logger.ErrorContext(ctx, "the reaper could not delete a machine",
			append(fields, "error", err)...)

		return err
	}

	if decision.Ignore {
		r.ignored[machine.Job] = true
	}

	r.metrics.Deleted(ctx, decision.Reason)
	r.logger.InfoContext(ctx, "the reaper acted on a machine", append(fields,
		telemetry.SpanDecision, string(decision.Action))...)

	return nil
}

// finish ends a job that no agent acquired.
func (r *Reaper) finish(ctx context.Context, machine Machine, decision Decision) error {
	if r.ignored[machine.Job] {
		return nil
	}

	err := r.stacks.Finish(ctx, machine.Job, failedExitStatus, message(machine, decision))

	// Buildkite refuses a finish of a job that already ended or that it does not know. A second
	// finish after a restart is the common case. The job is done either way, and every further
	// call would give the same refusal.
	if errors.Is(err, buildkite.ErrTerminal) {
		r.ignored[machine.Job] = true

		return nil
	}

	if err != nil {
		return err
	}

	r.metrics.Failed(ctx, decision.Reason)

	return nil
}

// notify writes the reason on the job page. A line that does not arrive is worth less than the
// removal of the machine, so a failure here is a log record and nothing more.
func (r *Reaper) notify(ctx context.Context, machine Machine, decision Decision) {
	if r.ignored[machine.Job] {
		return
	}

	err := r.stacks.Notify(ctx, []buildkite.Notification{{
		JobID: machine.Job, Detail: message(machine, decision), At: r.now(),
	}})
	if err != nil {
		r.logger.WarnContext(ctx, "the reaper could not write to the job page",
			telemetry.LogJob, string(machine.Job), "error", err)
	}
}

// warn writes one record for a machine whose job is in a state that no row names, and not one at
// every tick while it stays there.
func (r *Reaper) warn(ctx context.Context, machine Machine) {
	if seen, warned := r.warned[machine.Job]; warned && seen == machine.State {
		return
	}

	r.warned[machine.Job] = machine.State
	r.logger.WarnContext(ctx, "no row of the decision table matches this machine",
		telemetry.LogJob, string(machine.Job),
		telemetry.LogVM, machine.VM.Name,
		telemetry.SpanVMPhase, string(inventory.Phase(machine.VM)),
		telemetry.SpanJobState, machine.State)
}

// forget drops what the reaper remembers about jobs whose machines are gone, so that neither map
// grows for the life of the process.
func (r *Reaper) forget(snapshot inventory.Snapshot) {
	owned := make(map[job.ID]bool, len(snapshot.Owned))
	for _, machine := range snapshot.Owned {
		owned[machine.Job] = true
	}

	for id := range r.ignored {
		if !owned[id] {
			delete(r.ignored, id)
		}
	}

	for id := range r.warned {
		if !owned[id] {
			delete(r.warned, id)
		}
	}
}

// delete removes a machine from Orchard. Orchard answers a machine that is already gone as a
// success, so a second deletion is harmless.
func (r *Reaper) delete(ctx context.Context, machine Machine) error {
	ctx, span := r.tracer.Start(ctx, "orchard.vm.delete")
	defer span.End()

	return r.machines.DeleteVM(ctx, machine.VM.Name)
}
