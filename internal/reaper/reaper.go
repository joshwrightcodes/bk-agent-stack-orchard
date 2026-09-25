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

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Stacks is what the reaper needs of Buildkite.
type Stacks interface {
	States(ctx context.Context, ids []job.ID) (map[job.ID]string, error)
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
}

// New builds the reaper.
func New(options Options) (*Reaper, error) {
	var problems []error

	if options.Stacks == nil || options.Orchard == nil || options.Fleet == nil {
		problems = append(problems, errors.New("the reaper needs Buildkite, Orchard, and the fleet"))
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
	}, nil
}

// Reap examines every machine of this controller once.
//
// A machine that fails to go is a log record and a machine that the next tick tries again: the
// decision reads observed state, so it gives the same answer next time, and nothing here needs
// to remember that it failed.
func (r *Reaper) Reap(ctx context.Context) error {
	snapshot, read := r.fleet.Latest()
	if !read || len(snapshot.Owned) == 0 {
		return nil
	}

	states, observedAt, observed := r.states(ctx, snapshot)
	now := r.now()

	var failed, deleted int

	for _, owned := range snapshot.Owned {
		machine := Machine{
			VM:       owned.VM,
			Job:      owned.Job,
			Timeouts: Timeouts(r.profile(owned.Job), r.profiles, r.defaults),
		}

		if observed {
			machine.State = states[owned.Job]
			machine.Observed = true
			machine.ObservedAt = observedAt
		}

		decision := Decide(machine, now, r.freshness)
		if decision.Action != Delete {
			continue
		}

		if err := r.delete(ctx, machine, decision); err != nil {
			failed++

			continue
		}

		deleted++
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d deletions failed", failed, failed+deleted)
	}

	return nil
}

// states reads the Buildkite state of every job that has a machine, in one call, and gives the
// time that the controller read them.
//
// A call that fails gives no states and not empty ones. A job that Buildkite did not name and a
// job that the controller could not ask about are different things, and only the first one says
// anything about the job.
func (r *Reaper) states(ctx context.Context, snapshot inventory.Snapshot) (map[job.ID]string,
	time.Time, bool,
) {
	ids := make([]job.ID, 0, len(snapshot.Owned))
	for _, owned := range snapshot.Owned {
		ids = append(ids, owned.Job)
	}

	states, err := r.stacks.States(ctx, ids)
	if err != nil {
		r.logger.WarnContext(ctx, "the reaper could not read the job states, and acts only on "+
			"the rows that need none", "error", err)

		return nil, time.Time{}, false
	}

	return states, r.now(), true
}

// delete carries out one deletion, and writes its one decision record.
func (r *Reaper) delete(ctx context.Context, machine Machine, decision Decision) error {
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
		"row", strconv.Itoa(decision.Row),
		"reason", decision.Reason,
	}

	deleteCtx, deleting := r.tracer.Start(ctx, "orchard.vm.delete")
	err := r.machines.DeleteVM(deleteCtx, machine.VM.Name)
	deleting.End()

	if err != nil {
		span.RecordError(err)
		r.logger.ErrorContext(ctx, "the reaper could not delete a machine",
			append(fields, "error", err)...)

		return err
	}

	r.metrics.Deleted(ctx, decision.Reason)
	r.logger.InfoContext(ctx, "the reaper deleted a machine", fields...)

	return nil
}
