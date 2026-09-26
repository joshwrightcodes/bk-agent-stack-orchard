// Package progress follows each machine of this controller from its request to the agent that
// takes its job, and tells the job page and the telemetry at each step.
//
// A pipeline author who waits for a job sees the steps on the job page: "VM requested" from the
// launcher, then "placed on <worker>" and "booting" from here, and "VM deleted: …" from the
// reaper (target design, section 10.5). The same steps are span events in the trace of the job,
// and the time between two of them is a phase duration (section 10.1).
//
// The controller sees the steps at the ticks when it reads Orchard and Buildkite, so each
// duration has the tolerance that section 10.1 states.
package progress

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Booting is the line of a machine whose process runs while macOS starts. Orchard reports the
// machine as running when the Tart process starts, which is before the guest has booted.
const Booting = "booting"

// Placed gives the line of a machine that Orchard gave to a worker.
func Placed(worker string) string {
	return "placed on " + worker
}

// Notifier writes lines on job pages.
type Notifier interface {
	Notify(ctx context.Context, notifications []buildkite.Notification) error
}

// Fleet gives the last snapshot of the machines.
type Fleet interface {
	Latest() (inventory.Snapshot, bool)
}

// States gives what Buildkite last said about a job.
type States interface {
	Observed(id job.ID) (jobstates.Observation, bool)
}

// Timelines gives the start of a job that this process admitted.
type Timelines interface {
	Timeline(id job.ID) (launcher.Timeline, bool)
}

// Options configures the tracker.
type Options struct {
	Stacks    Notifier
	Fleet     Fleet
	States    States
	Timelines Timelines
	Metrics   *telemetry.Metrics
	Logger    *slog.Logger
	Tracer    trace.Tracer
	Now       func() time.Time
}

// Tracker follows the machines of this controller.
type Tracker struct {
	stacks    Notifier
	fleet     Fleet
	states    States
	timelines Timelines
	metrics   *telemetry.Metrics
	logger    *slog.Logger
	tracer    trace.Tracer
	now       func() time.Time

	// machines holds what the tracker saw of each machine. Only the goroutine of the loop reads
	// and writes it.
	machines map[job.ID]*seen
}

// seen is how far the tracker has seen one machine come, and when.
type seen struct {
	placed, running, accepted       bool
	placedAt, runningAt, acceptedAt time.Time
}

// New checks the options and gives the tracker.
func New(options Options) (*Tracker, error) {
	if options.Stacks == nil || options.Fleet == nil || options.States == nil ||
		options.Timelines == nil || options.Metrics == nil || options.Logger == nil ||
		options.Tracer == nil || options.Now == nil {
		return nil, errors.New("the progress tracker needs Buildkite, the fleet, the job " +
			"states, the timelines, the telemetry, and a clock")
	}

	return &Tracker{
		stacks:    options.Stacks,
		fleet:     options.Fleet,
		states:    options.States,
		timelines: options.Timelines,
		metrics:   options.Metrics,
		logger:    options.Logger,
		tracer:    options.Tracer,
		now:       options.Now,
		machines:  map[job.ID]*seen{},
	}, nil
}

// Observe reads the last snapshot, and reports each step that a machine took since the last
// tick. A line that does not arrive is worth less than the tick, so a failure of the notification
// is a log record and nothing more.
func (t *Tracker) Observe(ctx context.Context) error {
	snapshot, read := t.fleet.Latest()
	if !read {
		return nil
	}

	now := t.now()
	present := make(map[job.ID]bool, len(snapshot.Owned))

	var lines []buildkite.Notification

	for _, owned := range snapshot.Owned {
		present[owned.Job] = true
		lines = append(lines, t.step(ctx, owned, now)...)
	}

	// A machine that is gone has taken its last step.
	for id := range t.machines {
		if !present[id] {
			delete(t.machines, id)
		}
	}

	if len(lines) == 0 {
		return nil
	}

	if err := t.stacks.Notify(ctx, lines); err != nil {
		t.logger.WarnContext(ctx, "the controller could not write the steps of its machines",
			"error", err)
	}

	return nil
}

// step reports the steps that one machine took since the last tick, and gives the lines for its
// job page.
func (t *Tracker) step(ctx context.Context, owned inventory.Owned, now time.Time) []buildkite.Notification {
	timeline, ours := t.timelines.Timeline(owned.Job)

	machine, known := t.machines[owned.Job]
	if !known {
		machine = &seen{}
		t.machines[owned.Job] = machine

		// A machine that this process did not start belongs to a process before a restart. Its
		// job page already shows the steps that it took, and the times of those steps are gone.
		if !ours {
			machine.placed = owned.VM.Placed()
			machine.running = owned.VM.Status == orchard.StatusRunning
			machine.accepted = t.acquired(owned.Job)

			return nil
		}
	}

	var lines []buildkite.Notification

	if !machine.placed && owned.VM.Placed() {
		machine.placed, machine.placedAt = true, now
		lines = append(lines, t.report(ctx, owned, "placed", Placed(owned.VM.Worker), now))
		t.phase(ctx, timeline, telemetry.PhaseReservedToPlaced, timeline.ReservedAt, now)
	}

	if !machine.running && owned.VM.Status == orchard.StatusRunning {
		machine.running, machine.runningAt = true, now
		lines = append(lines, t.report(ctx, owned, "running", Booting, now))
		t.phase(ctx, timeline, telemetry.PhasePlacedToRunning, machine.placedAt, now)
	}

	if !machine.accepted && t.acquired(owned.Job) {
		machine.accepted, machine.acceptedAt = true, now
		t.phase(ctx, timeline, telemetry.PhaseRunningToAccepted, machine.runningAt, now)
		t.phase(ctx, timeline, telemetry.PhaseTotal, timeline.ScheduledAt, now)
	}

	return lines
}

// report writes one step as a span event in the trace of the job, and gives its line.
func (t *Tracker) report(ctx context.Context, owned inventory.Owned, event, line string,
	now time.Time,
) buildkite.Notification {
	_, span := telemetry.StartJob(ctx, t.tracer, owned.Job, "orchard.vm.change")
	span.AddEvent(event, trace.WithTimestamp(now), trace.WithAttributes(
		attribute.String(telemetry.LogWorker, owned.VM.Worker),
		attribute.String(telemetry.LogVM, owned.VM.Name),
	))
	span.End()

	return buildkite.Notification{JobID: owned.Job, Detail: line, At: now}
}

// phase records the duration of one phase, when the tracker saw where it began.
func (t *Tracker) phase(ctx context.Context, timeline launcher.Timeline, phase telemetry.JobPhase,
	began, ended time.Time,
) {
	if began.IsZero() {
		return
	}

	t.metrics.JobPhase(ctx, timeline.Profile, phase, ended.Sub(began))
}

// acquired reports a job that an agent took, as Buildkite last said.
func (t *Tracker) acquired(id job.ID) bool {
	observation, asked := t.states.Observed(id)

	return asked && buildkite.Acquired(observation.State)
}
