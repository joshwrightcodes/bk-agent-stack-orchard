// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

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
	"maps"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
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
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/progress"
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

	// CallTimeout is the time limit of each call to either API. Zero gives the limit of the
	// design. The component suite gives a short one, so that a fake that never answers costs it
	// little real time.
	CallTimeout time.Duration
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

	limit := options.CallTimeout
	if limit <= 0 {
		limit = callTimeout
	}

	// Each call to either API has a time limit, and is counted when it fails, here and nowhere
	// else. The count wraps the limit, so that a call that the limit ends counts as a timeout.
	stacks := countedStacks{
		next:    boundedStacks{next: options.Stacks, limit: limit},
		counter: counter{metrics: recorder.Metrics, component: telemetry.ComponentBuildkite},
	}
	machines := countedOrchard{
		next:    boundedOrchard{next: options.Orchard, limit: limit},
		counter: counter{metrics: recorder.Metrics, component: telemetry.ComponentOrchard},
	}

	registration, err := stack.New(stacks)
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

	poller, err := stack.NewPoller(stack.PollerOptions{
		Stacks:   stacks,
		Interval: settings.Buildkite.PollInterval,
		Metrics:  recorder.Metrics,
		Logger:   recorder.Logger,
		Now:      now,
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
		DefaultTimeouts:   settings.Defaults.Timeouts,
		CallTimeout:       limit,
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
		CallTimeout:  limit,
		Orchard:      machines,
		Fleet:        fleet,
		Profile:      starter.Profile,
		TokenAt:      starter.TokenAt,
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

	// The steps of each machine go to its job page before the reaper acts, so that a page shows
	// "booting" before "VM deleted" when both happen in one tick.
	steps, err := progress.New(progress.Options{
		Stacks:    stacks,
		Fleet:     fleet,
		States:    observed,
		Timelines: starter,
		Metrics:   recorder.Metrics,
		Logger:    recorder.Logger,
		Tracer:    recorder.Tracer,
		Now:       now,
	})
	if err != nil {
		return nil, err
	}

	decisions := &admitter{
		settings: settings,
		queue:    poller,
		fleet:    fleet,
		starter:  starter,
		states:   observed,
		logger:   recorder.Logger,
		metrics:  recorder.Metrics,
		tracer:   recorder.Tracer,
		now:      now,
		seen:     map[job.ID]bool{},
	}

	return controller.New(controller.Options{
		Stack:   registration,
		Sources: []controller.Source{poller, fleet, observed},
		Queue:   poller,
		Fleet:   decisions.observe,
		Reap: func(ctx context.Context) error {
			if err := steps.Observe(ctx); err != nil {
				return err
			}

			return reaping.Reap(ctx)
		},
		Admit:     decisions.admit,
		Interval:  settings.Buildkite.PollInterval,
		Freshness: settings.StateFreshness,
		Now:       now,
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
	states   *jobstates.States
	logger   *slog.Logger
	metrics  *telemetry.Metrics
	tracer   trace.Tracer
	now      func() time.Time

	// seen holds the jobs of the queue whose job.match span this process wrote. Only the
	// goroutine of the loop reads and writes it.
	seen map[job.ID]bool

	mu     sync.Mutex
	latest admission.Result

	// decided reports a decision of admission since the last observation. Without one, the gate
	// was closed, and the observation counts the waiting jobs of the last queue read itself.
	decided bool
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
		Most:        a.starter.PerTick(),
		Truncated:   queue.Truncated,
		Now:         a.now(),
	})

	a.trace(ctx, queue.Jobs, result)

	a.mu.Lock()
	a.latest, a.decided = result, true
	a.mu.Unlock()

	return a.starter.Launch(ctx, result, held)
}

// waiting counts the jobs of the last queue read that wait, for a tick that decided nothing. The
// gate of admission closes in an outage, and an outage is when jobs wait longest, so the demand
// stays visible. A queue that the controller has not read has no waiting job.
func (a *admitter) waiting() admission.Result {
	queue, read := a.queue.Latest()
	if !read {
		return admission.Result{}
	}

	// A fleet that has not answered holds no machine, so each matched job of the queue counts.
	held, _ := a.fleet.Latest()

	return admission.Waiting(admission.Request{
		Jobs:     queue.Jobs,
		Profiles: a.settings.Profiles,
		Fleet:    held,
		Held:     a.starter.Held(held),
		Eligible: a.fleet.Eligible,
		Now:      a.now(),
	})
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

	skipped := make(map[job.ID]admission.Unmatched, len(result.Unmatched))
	for _, unmatched := range result.Unmatched {
		skipped[unmatched.Job.ID] = unmatched
	}

	queued := make(map[job.ID]bool, len(waiting))

	for _, scheduled := range waiting {
		queued[scheduled.ID] = true

		if a.seen[scheduled.ID] {
			continue
		}

		profile, matched := profiles[scheduled.ID]
		skip, unmatched := skipped[scheduled.ID]

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
			// A skip is not an error of the controller: another runner may carry the job.
			span.SetAttributes(attribute.Bool(telemetry.SpanSkipped, true))
			span.AddEvent("no profile", trace.WithAttributes(
				attribute.String(telemetry.SpanMismatch, skip.Reason())))
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
	result, decided := a.latest, a.decided
	a.decided = false
	a.mu.Unlock()

	if !decided {
		result = a.waiting()
	}

	observation := telemetry.Observation{}
	result.Observe(&observation)

	if held, read := a.fleet.Latest(); read {
		held.Observe(&observation, a.fleet.Online, a.profileOf)
		held.Leftover(&observation, a.ended)

		open := admission.Slots(admission.Request{
			Profiles: a.settings.Profiles,
			Fleet:    held,
			Held:     a.starter.Held(held),
			Eligible: a.fleet.Eligible,
		}, a.fleet.Online)

		maps.Copy(observation.Slots, open)
	}

	profiles := make([]string, 0, len(a.settings.Profiles))
	for _, p := range a.settings.Profiles {
		profiles = append(profiles, p.Name)
	}

	observation.Expect(profiles)

	return observation
}

// profileOf gives the profile of a machine: the one that this process started it with, or else the
// one that its tags name. A machine of neither counts as one of an unknown profile until it ends.
func (a *admitter) profileOf(owned inventory.Owned) string {
	return inventory.ProfileOf(owned, a.starter.Profile, a.settings.Profiles)
}

// ended reports a job whose state in Buildkite, as the controller last read it, is terminal.
func (a *admitter) ended(id job.ID) bool {
	observation, asked := a.states.Observed(id)

	return asked && buildkite.Terminal(observation.State)
}
