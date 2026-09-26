// Package app builds the controller from its parts and wires them together.
//
// The binary gives it the clients of Buildkite and of Orchard and the clock of the machine. The
// component suite gives it in-memory fakes of both APIs and a clock that the suite moves, and it
// then runs the controller that ships, one tick at a time. A suite with a copy of this wiring
// would test a controller that nobody runs.
package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Options is everything that the controller takes from outside itself.
type Options struct {
	Config    *config.Config
	Telemetry *telemetry.Telemetry

	// Stacks and Orchard are the two APIs of the controller.
	Stacks  buildkite.Stacks
	Orchard orchard.API

	// Now is the one clock of every part.
	Now func() time.Time

	// Environment reads the variables that a profile names, such as the image credentials. A nil
	// value reads none.
	Environment func(string) string
}

// Build gives the control loop.
func Build(options Options) (*controller.Controller, error) {
	if options.Config == nil || options.Telemetry == nil || options.Stacks == nil ||
		options.Orchard == nil || options.Now == nil {
		return nil, errors.New("the controller needs its configuration, its telemetry, both APIs, " +
			"and a clock")
	}

	settings, recorder := options.Config, options.Telemetry

	now := options.Now

	environment := options.Environment
	if environment == nil {
		environment = func(string) string { return "" }
	}

	// Each call to either API is counted when it fails, here and nowhere else.
	stacks := countedStacks{
		next:    options.Stacks,
		counter: counter{metrics: recorder.Metrics, component: telemetry.ComponentBuildkite},
	}
	machines := countedOrchard{
		next:    options.Orchard,
		counter: counter{metrics: recorder.Metrics, component: telemetry.ComponentOrchard},
	}

	registration, err := stack.New(stacks)
	if err != nil {
		return nil, err
	}

	poller, err := stack.NewPoller(stack.PollerOptions{
		Stacks:   stacks,
		Interval: settings.Buildkite.PollInterval,
		Now:      now,
	})
	if err != nil {
		return nil, err
	}

	fleet, err := inventory.New(inventory.Options{
		Orchard:        machines,
		ControllerID:   settings.ControllerID,
		Interval:       settings.Orchard.InventoryInterval,
		OfflineTimeout: settings.Orchard.WorkerOfflineTimeout,
		Now:            now,
	})
	if err != nil {
		return nil, err
	}

	starter, err := launcher.New(launcher.Options{
		Stacks:            stacks,
		Orchard:           machines,
		ControllerID:      settings.ControllerID,
		Queue:             settings.Buildkite.Queue,
		ReservationExpiry: settings.Buildkite.ReservationExpiry,
		TokenLifetime:     settings.Buildkite.JobTokenLifetime,
		Environment:       environment,
		Metrics:           recorder.Metrics,
		Logger:            recorder.Logger,
		Tracer:            recorder.Tracer,
		Now:               now,
	})
	if err != nil {
		return nil, err
	}

	// The job states ask about the machines that the inventory holds, so they follow it in the
	// sources of the loop, and each tick asks about the machines that it has just read.
	observed, err := jobstates.New(jobstates.Options{
		Stacks:   stacks,
		Fleet:    fleet,
		Interval: settings.Buildkite.JobStatesInterval,
		Now:      now,
	})
	if err != nil {
		return nil, err
	}

	reaping, err := reaper.New(reaper.Options{
		Stacks:       stacks,
		States:       observed,
		Reservations: starter,
		RenewBefore:  settings.Buildkite.ReservationRenewBefore,
		Orchard:      machines,
		Fleet:        fleet,
		Profile:      starter.Profile,
		Profiles:     settings.Profiles,
		Defaults:     settings.Defaults.Timeouts,
		Freshness:    settings.StateFreshness,
		Metrics:      recorder.Metrics,
		Logger:       recorder.Logger,
		Tracer:       recorder.Tracer,
		Now:          now,
	})
	if err != nil {
		return nil, err
	}

	decisions := &admitter{
		settings: settings,
		queue:    poller,
		fleet:    fleet,
		starter:  starter,
		logger:   recorder.Logger,
		metrics:  recorder.Metrics,
		tracer:   recorder.Tracer,
		now:      now,
		seen:     map[job.ID]bool{},
	}

	return controller.New(controller.Options{
		Stack:     registration,
		Sources:   []controller.Source{poller, fleet, observed},
		Queue:     poller,
		Fleet:     decisions.observe,
		Reap:      reaping.Reap,
		Admit:     decisions.admit,
		Interval:  settings.Buildkite.PollInterval,
		Freshness: settings.StateFreshness,
		Metrics:   recorder.Metrics,
		Logger:    recorder.Logger,
		Tracer:    recorder.Tracer,
	})
}

// admitter runs the admission decision of a tick and remembers what it decided, so that the
// observation of the same tick can report it.
type admitter struct {
	settings *config.Config
	queue    *stack.Poller
	fleet    *inventory.Inventory
	starter  *launcher.Launcher
	logger   *slog.Logger
	metrics  *telemetry.Metrics
	tracer   trace.Tracer
	now      func() time.Time

	// seen holds the jobs of the queue whose job.match span this process wrote. Only the
	// goroutine of the loop reads and writes it.
	seen map[job.ID]bool

	mu     sync.Mutex
	latest admission.Result
}

// admit decides which jobs the fleet can carry, and starts them.
func (a *admitter) admit(ctx context.Context) error {
	queue, read := a.queue.Latest()
	if !read {
		return nil
	}

	held, read := a.fleet.Latest()
	if !read {
		// Admission needs the machines as well as the queue: without them it would count the
		// room of the fleet as empty and start a job for every waiting one.
		return nil
	}

	result := admission.Decide(admission.Request{
		Jobs:        queue.Jobs,
		Profiles:    a.settings.Profiles,
		Fleet:       held,
		Held:        a.starter.Held(held),
		Eligible:    a.fleet.Eligible,
		MaxInFlight: a.settings.MaxInFlight,
		Now:         a.now(),
	})

	a.trace(ctx, queue.Jobs, result)

	for _, unmatched := range result.Unmatched {
		a.metrics.Unmatched(ctx)
		a.logger.WarnContext(ctx, "no profile carries this job",
			telemetry.LogJob, string(unmatched.Job.ID),
			telemetry.LogPipeline, unmatched.Job.Pipeline,
			"reason", unmatched.Reason)
	}

	a.mu.Lock()
	a.latest = result
	a.mu.Unlock()

	return a.starter.Launch(ctx, result, held)
}

// trace writes job.match for each job that this process sees in the queue for the first time,
// and job.admit for each job that admission starts. A span at each tick would put a span each
// second into the trace of a job that waits. A job that comes back to the queue, because its
// reservation lapsed, is matched again.
func (a *admitter) trace(ctx context.Context, waiting []buildkite.ScheduledJob,
	result admission.Result,
) {
	profiles := make(map[job.ID]string, len(result.Matched))
	for _, candidate := range result.Matched {
		profiles[candidate.Job.ID] = candidate.Profile.Name
	}

	reasons := make(map[job.ID]string, len(result.Unmatched))
	for _, unmatched := range result.Unmatched {
		reasons[unmatched.Job.ID] = unmatched.Reason
	}

	queued := make(map[job.ID]bool, len(waiting))

	for _, scheduled := range waiting {
		queued[scheduled.ID] = true

		if a.seen[scheduled.ID] {
			continue
		}

		profile, matched := profiles[scheduled.ID]
		reason, unmatched := reasons[scheduled.ID]

		// A job with a machine is in neither list, and its match is in its trace already.
		if !matched && !unmatched {
			continue
		}

		_, span := telemetry.StartJob(ctx, a.tracer, scheduled.ID, "job.match",
			trace.WithAttributes(
				attribute.Bool(telemetry.SpanMatched, matched),
				attribute.String(telemetry.SpanProfile, profile),
			))
		if unmatched {
			span.SetStatus(codes.Error, reason)
		}
		span.End()

		a.seen[scheduled.ID] = true
	}

	for id := range a.seen {
		if !queued[id] {
			delete(a.seen, id)
		}
	}

	for _, admitted := range result.Admit {
		_, span := telemetry.StartJob(ctx, a.tracer, admitted.Job.ID, "job.admit",
			trace.WithAttributes(attribute.String(telemetry.SpanProfile, admitted.Profile.Name)))
		span.End()
	}
}

// observe gives the instruments what this tick saw.
func (a *admitter) observe() telemetry.Observation {
	a.mu.Lock()
	result := a.latest
	a.mu.Unlock()

	observation := telemetry.Observation{}
	result.Observe(&observation)

	if held, read := a.fleet.Latest(); read {
		held.Observe(&observation, a.fleet.Online, a.profileOf)
	}

	return observation
}

// profileOf gives the profile of a job that this process started. A machine of a process before
// a restart has none, and it counts as one of an unknown profile until it ends.
func (a *admitter) profileOf(id job.ID) string {
	return a.starter.Profile(id)
}
