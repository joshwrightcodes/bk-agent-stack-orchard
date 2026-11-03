// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
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

// Reservations gives when the reservation of a job ends, and extends reservations. The launcher
// holds both, because it makes the reservations and knows their expiry.
type Reservations interface {
	ReservedUntil(id job.ID) (time.Time, bool)
	Extend(ctx context.Context, ids []job.ID) error
}

// States gives what Buildkite said about a job, and when the controller asked.
type States interface {
	Observed(id job.ID) (jobstates.Observation, bool)
}

// Machines is what the reaper needs of Orchard.
type Machines interface {
	DeleteVM(ctx context.Context, name string) error
	Events(ctx context.Context, name string) ([]orchard.Event, error)
}

// Fleet gives the last snapshot of the machines, and takes a machine out of it after a delete.
// Admission runs after the reaper in the same tick and reads the same snapshot, so it must not
// count the room of a machine that Orchard no longer holds.
type Fleet interface {
	Latest() (inventory.Snapshot, bool)
	Forget(name string)
}

// Options configures the reaper.
type Options struct {
	Stacks       Stacks
	States       States
	Reservations Reservations
	Orchard      Machines
	Fleet        Fleet

	// Profile gives the profile of a job that this process started, and empty text for one that
	// it did not. The tags of a machine name the profile of one that it did not start.
	Profile func(job.ID) string

	// Profiles and Defaults give the limits of a machine.
	Profiles []profile.Profile
	Defaults profile.Timeouts

	// Freshness is how old a job state may be and still count.
	Freshness time.Duration

	// RenewBefore is how long before its end a reservation is extended.
	RenewBefore time.Duration

	// CallTimeout is the time limit of one call to either API. The actions of one pass share a
	// time budget of a few of these limits.
	CallTimeout time.Duration

	Metrics *telemetry.Metrics
	Logger  *slog.Logger
	Tracer  trace.Tracer

	// Now is the clock of the part. internal/app gives every part the same one.
	Now func() time.Time
}

// Reaper examines the machines of this controller at each tick, and removes the ones that the
// table says must go.
type Reaper struct {
	stacks       Stacks
	states       States
	reservations Reservations
	renewBefore  time.Duration
	budget       time.Duration
	machines     Machines
	fleet        Fleet
	profile      func(job.ID) string
	profiles     []profile.Profile
	defaults     profile.Timeouts
	freshness    time.Duration
	metrics      *telemetry.Metrics
	logger       *slog.Logger
	tracer       trace.Tracer
	now          func() time.Time

	// ignored holds the jobs that Buildkite does not know (row 5). Every further call about such a
	// job gives the same refusal, so the controller makes none (target design, section 7.1, rule
	// 4). Only the goroutine of the loop reads and writes it.
	ignored map[job.ID]bool

	// refused holds the jobs whose finish Buildkite refused with a 404 or a 422. A refusal can
	// mean that the job ended, and also that an agent acquired it after the state that the finish
	// acted on, so the machine stays in that pass and the next state decides. A second finish
	// would get the same refusal, so the controller sends none. Only the goroutine of the loop
	// reads and writes it.
	refused map[job.ID]bool

	// passed is when the last pass began. A job state that the controller asked for after it is
	// a state of this tick, because the refresh of a tick comes before its pass.
	passed time.Time

	// warned holds the state that the last warning named for a job whose state matches no row, so
	// that a job stuck in one such state gives one record and not one at every tick.
	warned map[job.ID]string

	// ended holds when a tick first saw each job in a terminal state, until its machine leaves the
	// owned set. It times the phase from the end of a job to the deletion of its machine. After
	// Orchard accepts the deletion, the entry holds the zero time, so that a pass that still
	// lists the machine records nothing more. A restart loses it, and the first tick after one
	// then starts the time again, which the design states.
	ended map[job.ID]time.Time

	// listed holds, for each machine of a snapshot, when a snapshot first listed it, first
	// showed it on a worker, and first showed it running, and the creation time that Orchard gave
	// it. A machine with another creation time is a new machine with the same name, such as the
	// second machine of a job, and its times start again. Only the goroutine of the loop reads and
	// writes it.
	listed map[string]sighting
}

// sighting is what the snapshots of this process first showed of one machine.
type sighting struct {
	// created is the creation time that Orchard gave the machine, which tells two machines of one
	// name apart. It is never compared with a time of the controller.
	created time.Time

	// at, placed, and running are when a snapshot first listed the machine, first showed it on a
	// worker, and first showed it running, on the clock of the controller.
	at, placed, running time.Time
}

// New builds the reaper.
func New(options Options) (*Reaper, error) {
	var problems []error

	if options.Now == nil {
		problems = append(problems, errors.New("the reaper needs a clock"))
	}

	if options.Stacks == nil || options.States == nil || options.Reservations == nil ||
		options.Orchard == nil || options.Fleet == nil {
		problems = append(problems, errors.New("the reaper needs Buildkite, the job states, "+
			"the reservations, Orchard, and the fleet"))
	}

	if options.RenewBefore <= 0 {
		problems = append(problems, fmt.Errorf("the renewal lead is %s, and it must be more "+
			"than zero", options.RenewBefore))
	}

	if options.CallTimeout <= 0 {
		problems = append(problems, errors.New("the reaper needs the time limit of a call, "+
			"which sets the time budget of its actions"))
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

	name := options.Profile
	if name == nil {
		name = func(job.ID) string { return "" }
	}

	return &Reaper{
		stacks:       options.Stacks,
		states:       options.States,
		reservations: options.Reservations,
		renewBefore:  options.RenewBefore,
		budget:       parallel.Budget(options.CallTimeout),
		machines:     options.Orchard,
		fleet:        options.Fleet,
		profile:      name,
		profiles:     options.Profiles,
		defaults:     options.Defaults,
		freshness:    options.Freshness,
		metrics:      options.Metrics,
		logger:       options.Logger,
		tracer:       tracer,
		now:          now,
		ignored:      map[job.ID]bool{},
		refused:      map[job.ID]bool{},
		warned:       map[job.ID]string{},
		ended:        map[job.ID]time.Time{},
		listed:       map[string]sighting{},
	}, nil
}

// Reap examines every machine of this controller once, and carries out what the table decides.
//
// An action that fails is a log record and a machine that the next tick tries again: the decision
// reads observed state, so it gives the same answer next time, and nothing here needs to remember
// that it failed.
//
// The extensions go first, in one call, because they keep jobs with this stack, and an action
// that waits for an API must not hold them back. The actions then run parallel.Limit at a time,
// inside the time budget of the step (target design, section 4). An action that finds an API not
// answering stops the actions that wait, because they would wait for the same API, and the next
// tick decides again. The lines of the job pages go last, together.
func (r *Reaper) Reap(ctx context.Context) error {
	snapshot, read := r.fleet.Latest()
	if !read {
		return nil
	}

	now := r.now()
	rules := Rules{Freshness: r.freshness, RenewBefore: r.renewBefore}

	machines := r.collect(snapshot)
	r.passed = now

	var (
		failures int
		extend   []job.ID
		tasks    []task
	)

	for _, machine := range machines {
		decision := Decide(machine, now, rules)

		switch decision.Action {
		case Extend:
			extend = append(extend, machine.Job)
		case Keep:
			if decision.Row == 0 {
				r.warn(ctx, machine)
			}
		case Delete, FinishAndDelete, NotifyAndDelete:
			tasks = append(tasks, task{
				machine: machine, decision: decision, ignored: r.ignored[machine.Job],
				refused: r.refused[machine.Job],
			})
		}
	}

	if err := r.reservations.Extend(ctx, extend); err != nil {
		failures++

		r.logger.ErrorContext(ctx, "the reaper could not extend the reservations of the machines "+
			"that start", "jobs", len(extend), "error", err)
	}

	step, cancel := context.WithTimeout(ctx, r.budget)
	outcomes := parallel.Each(step, parallel.Limit, tasks, r.carry, func(done result) bool {
		return retry.Unavailable(done.err)
	})
	cancel()

	var (
		lines []buildkite.Notification
		left  int
	)

	for i, outcome := range outcomes {
		if !outcome.Done {
			left++

			continue
		}

		if outcome.Value.err != nil {
			failures++
		}

		lines = r.settle(ctx, tasks[i], outcome.Value, lines)
	}

	if left > 0 {
		r.logger.WarnContext(ctx, "the reaper stopped its pass early, because an API did not "+
			"answer or the step ran out of time, and the next tick decides again", "machines", left)
	}

	r.write(ctx, lines)
	r.forget(snapshot)

	if failures > 0 {
		return fmt.Errorf("%d actions of the reaper failed", failures)
	}

	return nil
}

// task is one decision to carry out, as the goroutine of the loop prepared it.
type task struct {
	machine  Machine
	decision Decision

	// ignored is a job that Buildkite does not know: the action makes no call about it.
	ignored bool

	// refused is a job whose finish Buildkite refused before: the action sends no second finish.
	refused bool
}

// result is what carrying out one task did. The goroutine of the loop applies it after every
// action of the pass ended, so that the maps of the reaper keep one writer.
type result struct {
	// finished is a job that Buildkite ended, and refused is a job whose finish Buildkite
	// refused.
	finished, refused bool

	// deleted is a machine whose deletion Orchard accepted.
	deleted bool

	// err is the failure that stopped the action.
	err error
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
		seen := r.list(owned.VM, snapshot.At)
		machine := Machine{
			VM:         owned.VM,
			Job:        owned.Job,
			SnapshotAt: snapshot.At,
			ListedAt:   seen.at,
			PlacedAt:   seen.placed,
			RunningAt:  seen.running,
			Timeouts:   Timeouts(r.nameOf(owned), r.profiles, r.defaults),
		}

		// A reservation whose end the controller does not know is extended at once, which is
		// every reservation after a restart.
		if until, known := r.reservations.ReservedUntil(owned.Job); known {
			machine.ReservedUntil = until
		}

		// A job that the controller has not asked about has no state, and not an empty one.
		if observation, asked := r.states.Observed(owned.Job); asked {
			machine.State = observation.State
			machine.Named = observation.Named
			machine.Absent = observation.Absent
			machine.Observed = true
			machine.ObservedAt = observation.At
			machine.Current = observation.At.After(r.passed)

			if _, counted := r.ended[owned.Job]; !counted && buildkite.Terminal(observation.State) {
				r.ended[owned.Job] = r.now()
			}
		}

		machines = append(machines, machine)
	}

	return machines
}

// list gives what the snapshots first showed of a machine, and records the snapshot at as the
// first one that listed it, placed it, or showed it running, for each of those that no snapshot
// showed before.
func (r *Reaper) list(vm orchard.VM, at time.Time) sighting {
	seen, known := r.listed[vm.Name]
	if !known || !seen.created.Equal(vm.CreatedAt) {
		seen = sighting{created: vm.CreatedAt, at: at}
	}

	if seen.placed.IsZero() && vm.Placed() {
		seen.placed = at
	}

	if seen.running.IsZero() && vm.Status == orchard.StatusRunning {
		seen.running = at
	}

	r.listed[vm.Name] = seen

	return seen
}

// carry carries out one task, and gives what it did. It runs on a goroutine of the step, so it
// makes the calls and writes the log, and it changes no state of the reaper.
func (r *Reaper) carry(ctx context.Context, t task) result {
	machine, decision := t.machine, t.decision

	ctx, span := telemetry.StartJob(ctx, r.tracer, machine.Job, "reaper.decide",
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

	var done result

	// A machine whose job did not finish stays, so that the next tick finishes the job before the
	// machine goes. Without that, a failed job would wait for its lost-agent timeout.
	if decision.Action == FinishAndDelete && !t.ignored && !t.refused {
		err := r.finish(ctx, machine, decision)

		switch {
		case err == nil:
			done.finished = true
		case errors.Is(err, buildkite.ErrTerminal):
			// Buildkite refuses a finish of a job that ended, of a job that it does not know,
			// and, as far as anyone knows, of a job that an agent acquired after the state that
			// this finish read. The last one runs a build, so the machine stays in this pass,
			// and the next state decides: an agent keeps it, and a job that ended loses it.
			r.logger.InfoContext(ctx, "buildkite refused to end a job, and the next job state "+
				"decides what happens to its machine", append(fields, "error", err)...)

			done.refused = true

			return done
		default:
			span.RecordError(err)
			r.logger.ErrorContext(ctx, "the reaper could not end a job",
				append(fields, "error", err)...)

			done.err = err

			return done
		}
	}

	if err := r.delete(ctx, machine); err != nil {
		span.RecordError(err)
		r.logger.ErrorContext(ctx, "the reaper could not delete a machine",
			append(fields, "error", err)...)

		done.err = err

		return done
	}

	done.deleted = true

	r.logger.InfoContext(ctx, "the reaper acted on a machine", append(fields,
		telemetry.SpanDecision, string(decision.Action))...)

	return done
}

// settle applies what one task did to the state of the reaper, and adds the lines of its job page
// to lines. It runs on the goroutine of the loop.
func (r *Reaper) settle(ctx context.Context, t task, done result,
	lines []buildkite.Notification,
) []buildkite.Notification {
	machine, decision := t.machine, t.decision

	if done.refused {
		r.refused[machine.Job] = true
	}

	if done.finished {
		r.metrics.Failed(ctx, decision.Reason)
	}

	// Why the machine failed comes before its removal on the job page, and it goes whether the
	// removal worked or not: a line that does not arrive is worth less than the removal.
	if decision.Action == NotifyAndDelete && !t.ignored {
		lines = r.line(lines, machine, Failed(machine.VM.StatusMessage))
	}

	if !done.deleted {
		return lines
	}

	// Admission runs after the reaper in the same tick, and must not count the room of a
	// machine that Orchard no longer holds.
	r.fleet.Forget(machine.VM.Name)

	if decision.Ignore {
		r.ignored[machine.Job] = true
	}

	r.metrics.Deleted(ctx, decision.Reason)
	r.deleted(ctx, machine)

	// The last step on the job page. Buildkite may refuse a line for a job that ended, which
	// changes nothing here.
	return r.line(lines, machine, Deleted(decision.Reason))
}

// finish ends a job that no agent acquired.
func (r *Reaper) finish(ctx context.Context, machine Machine, decision Decision) error {
	detail := message(machine, decision, r.script(ctx, machine, decision))

	ctx, span := r.tracer.Start(ctx, "stacks.finish_job")
	defer span.End()

	err := r.stacks.Finish(ctx, machine.Job, failedExitStatus, detail)
	if err != nil && !errors.Is(err, buildkite.ErrTerminal) {
		span.RecordError(err)
	}

	return err
}

// script gives the lines of the startup script for an acquire timeout and a failed machine, and
// nothing for any other reason. Lines that Orchard cannot give do not stop the finish: the job
// still fails, with the message that it has without them.
func (r *Reaper) script(ctx context.Context, machine Machine, decision Decision) []string {
	if decision.Reason != ReasonAcquireTimeout && decision.Reason != ReasonVMFailed {
		return nil
	}

	ctx, span := r.tracer.Start(ctx, "orchard.vm.events")
	defer span.End()

	events, err := r.machines.Events(ctx, machine.VM.Name)
	if err != nil {
		span.RecordError(err)
		r.logger.WarnContext(ctx, "the reaper could not read the startup script of a machine",
			telemetry.LogJob, string(machine.Job), telemetry.LogVM, machine.VM.Name, "error", err)

		return nil
	}

	return logLines(events)
}

// line adds one line for the job page of a machine to lines. An orphan has no job, and so no page.
func (r *Reaper) line(lines []buildkite.Notification, machine Machine, detail string,
) []buildkite.Notification {
	if machine.Job == "" {
		return lines
	}

	return append(lines, buildkite.Notification{JobID: machine.Job, Detail: detail, At: r.now()})
}

// write sends the lines of one pass, in as few requests as the limit of a request permits, as the
// extensions go in one call. One request for each machine would spend the rate limit of the
// organisation when a wave of jobs ends together (target design, section 4).
//
// A line that does not arrive is worth less than the removal of the machine, so a failure here is
// a log record and nothing more. Buildkite refuses a line for a job that ended, which is the usual
// case for the line of a deletion, so a refusal is a debug record and not a warning at each pass.
func (r *Reaper) write(ctx context.Context, lines []buildkite.Notification) {
	for start := 0; start < len(lines); start += buildkite.NotificationsPerRequest {
		batch := lines[start:min(start+buildkite.NotificationsPerRequest, len(lines))]

		err := r.stacks.Notify(ctx, batch)

		switch {
		case err == nil:
		case errors.Is(err, buildkite.ErrRefused):
			r.logger.DebugContext(ctx, "buildkite refused some lines of the reaper",
				"lines", len(batch), "error", err)
		default:
			r.logger.WarnContext(ctx, "the reaper could not write to the job pages",
				"lines", len(batch), "error", err)
		}

		// A Buildkite that does not answer would hold each later request for its whole time
		// limit, so they wait for the next pass.
		if retry.Unavailable(err) {
			return
		}
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

// forget drops what the reaper remembers about jobs whose machines are gone, so that no map grows
// for the life of the process.
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

	for id := range r.refused {
		if !owned[id] {
			delete(r.refused, id)
		}
	}

	for id := range r.warned {
		if !owned[id] {
			delete(r.warned, id)
		}
	}

	for id := range r.ended {
		if !owned[id] {
			delete(r.ended, id)
		}
	}

	names := make(map[string]bool, len(snapshot.Owned))
	for _, machine := range snapshot.Owned {
		names[machine.VM.Name] = true
	}

	for name := range r.listed {
		if !names[name] {
			delete(r.listed, name)
		}
	}
}

// deleted records the phase from the end of a job to the deletion that Orchard accepted, for a
// job whose end a tick saw.
func (r *Reaper) deleted(ctx context.Context, machine Machine) {
	began, ended := r.ended[machine.Job]
	if !ended || began.IsZero() {
		return
	}

	// The entry stays, as recorded, until forget removes it with the machine.
	r.ended[machine.Job] = time.Time{}

	name := r.nameOf(inventory.Owned{VM: machine.VM, Job: machine.Job})
	if name == "" {
		name = inventory.UnknownProfile
	}

	r.metrics.JobPhase(ctx, name, telemetry.PhaseFinishedToDeleted, r.now().Sub(began))
}

// nameOf gives the profile of a machine: the one that this process started it with, or else the
// one that its tags name, when the configuration has it.
func (r *Reaper) nameOf(owned inventory.Owned) string {
	return inventory.ProfileOf(owned, r.profile, r.profiles)
}

// delete removes a machine from Orchard. Orchard answers a machine that is already gone as a
// success, so a second deletion is harmless.
func (r *Reaper) delete(ctx context.Context, machine Machine) error {
	ctx, span := r.tracer.Start(ctx, "orchard.vm.delete")
	defer span.End()

	return r.machines.DeleteVM(ctx, machine.VM.Name)
}
