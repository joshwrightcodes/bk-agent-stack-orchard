// Package controller runs the tick.
//
// One goroutine runs the loop, and the steps of a tick run in sequence: refresh the sources,
// reap the machines, then admit and launch. The order is the reason that Admission always sees
// the deletions of the same tick (target design, section 4).
//
// The loop owns the order, the timing, and the decision to skip a step. It owns no behaviour of
// a step: each step is a collaborator, so that a unit of work can add one without changing the
// loop, and so that a test of the loop needs none of them.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// shutdownGrace is how long the withdrawal of the stack may take after the loop stops. The
// context of the loop is already cancelled then, so the withdrawal needs one of its own.
const shutdownGrace = 10 * time.Second

// Registrar announces this controller to Buildkite and withdraws it.
type Registrar interface {
	Register(ctx context.Context) error
	Deregister(ctx context.Context) error
}

// Source is one supply of observed state that the loop refreshes.
type Source interface {
	// Source says which supply this is.
	Source() telemetry.Source

	// Interval is how often the source may be read. The loop decides when that is, because the
	// loop owns the timing of a tick.
	Interval() time.Duration

	// Refresh reads the source once.
	Refresh(ctx context.Context) error

	// At gives when the last answer arrived, and the zero time while none has.
	At() time.Time
}

// Queue gives the last answer of the scheduled-jobs poll.
type Queue interface {
	Latest() (stack.Queue, bool)
}

// admissionRests are the sources that a decision to start a job rests on (target design, section
// 6.3). An old answer of either one can start a job for a slot that is gone, or for a job that
// another agent took.
var admissionRests = []telemetry.Source{telemetry.SourceScheduledJobs, telemetry.SourceInventory}

// step names the part of a tick that a span and a metric carry.
var step = map[telemetry.Source]string{
	telemetry.SourceScheduledJobs: "poll",
	telemetry.SourceInventory:     "inventory",
	telemetry.SourceJobStates:     "job_states",
}

// Options configures the loop.
type Options struct {
	// Stack is the registration of this controller.
	Stack Registrar

	// Sources are refreshed at the start of a tick, each one when its interval is complete.
	Sources []Source

	// Queue gives the jobs that wait, and the value that says whether an operator paused the
	// queue.
	Queue Queue

	// Fleet gives what the parts of the controller saw, which the instruments report. The loop
	// adds the age of each source to it. A nil value reports the sources only.
	Fleet func() telemetry.Observation

	// Reap examines the machines of this controller. A nil value skips the step.
	Reap func(ctx context.Context) error

	// Admit starts the jobs that the fleet can carry. A nil value skips the step.
	Admit func(ctx context.Context) error

	// Interval is how often the tick runs.
	Interval time.Duration

	// Freshness is how old the answers that admission rests on may be. An older answer stops
	// admission, and not the reaper.
	Freshness time.Duration

	// Metrics, Logger, and Tracer record what the loop did. A nil Tracer records no span.
	Metrics *telemetry.Metrics
	Logger  *slog.Logger
	Tracer  trace.Tracer
}

// Controller runs the tick.
type Controller struct {
	stack     Registrar
	sources   []Source
	queue     Queue
	fleet     func() telemetry.Observation
	reap      func(ctx context.Context) error
	admit     func(ctx context.Context) error
	interval  time.Duration
	freshness time.Duration
	metrics   *telemetry.Metrics
	logger    *slog.Logger
	tracer    trace.Tracer

	// refreshed holds the tick at which each source was last read. Only the goroutine of the
	// loop reads and writes it.
	refreshed map[telemetry.Source]time.Time
}

// New builds the loop.
func New(options Options) (*Controller, error) {
	var problems []error

	if options.Stack == nil {
		problems = append(problems, errors.New("the controller needs a stack"))
	}

	if options.Queue == nil {
		problems = append(problems, errors.New("the controller needs the queue"))
	}

	if options.Metrics == nil {
		problems = append(problems, errors.New("the controller needs the instruments"))
	}

	if options.Logger == nil {
		problems = append(problems, errors.New("the controller needs a logger"))
	}

	if options.Interval <= 0 {
		problems = append(problems, fmt.Errorf("the tick interval is %s, and it must be more "+
			"than zero", options.Interval))
	}

	if options.Freshness <= 0 {
		problems = append(problems, fmt.Errorf("the state freshness is %s, and it must be more "+
			"than zero", options.Freshness))
	}

	// Without a source, the gate of admission has nothing to measure, and a wiring that left one
	// out would start jobs on an answer of any age.
	if options.Admit != nil {
		for _, rest := range admissionRests {
			if !slices.ContainsFunc(options.Sources, func(s Source) bool { return s.Source() == rest }) {
				problems = append(problems, fmt.Errorf("admission rests on the source %s, and the "+
					"loop does not have it", rest))
			}
		}
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	tracer := options.Tracer
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("")
	}

	return &Controller{
		stack:     options.Stack,
		sources:   options.Sources,
		queue:     options.Queue,
		fleet:     options.Fleet,
		reap:      options.Reap,
		admit:     options.Admit,
		interval:  options.Interval,
		freshness: options.Freshness,
		metrics:   options.Metrics,
		logger:    options.Logger,
		tracer:    tracer,
		refreshed: map[telemetry.Source]time.Time{},
	}, nil
}

// Run registers the stack, ticks until the context ends, and withdraws the stack.
//
// A tick that fails does not stop the loop, because almost every failure here is one call to one
// API, and the next tick reads the world again. The loop stops only when the context ends.
func (c *Controller) Run(ctx context.Context) error {
	if err := c.stack.Register(ctx); err != nil {
		return err
	}

	c.logger.InfoContext(ctx, "the controller registered its stack and starts its loop",
		"interval", c.interval.String())

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return c.withdraw(ctx)
		case at := <-ticker.C:
			c.tick(ctx, at)
		}
	}
}

// withdraw takes the stack out of Buildkite. The context of the loop has ended, so the call gets
// a context of its own: a withdrawal that the shutdown itself cancels would leave the stack in
// Buildkite until it times out there.
func (c *Controller) withdraw(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	if err := c.stack.Deregister(ctx); err != nil {
		return err
	}

	c.logger.InfoContext(ctx, "the controller withdrew its stack and stopped")

	return nil
}

// tick runs the steps of one tick, in order. At is the time of the tick.
func (c *Controller) tick(ctx context.Context, at time.Time) {
	// The observation comes last, so that it reports the fleet as this tick left it and not as
	// this tick found it.
	defer c.observe()

	c.refresh(ctx, at)

	c.run(ctx, "reap", c.reap)

	if reason, ok := c.mayAdmit(at); !ok {
		c.logger.DebugContext(ctx, "the controller admitted no job", "reason", reason)

		return
	}

	c.run(ctx, "admit", c.admit)
}

// refresh reads each source whose interval is complete.
func (c *Controller) refresh(ctx context.Context, at time.Time) {
	for _, source := range c.sources {
		if !c.due(source, at) {
			continue
		}

		// The attempt counts, and not only a success. A source that failed waits for its
		// interval like one that answered, because the API behind it is where the failure is,
		// and a call at every tick would press it harder.
		c.refreshed[source.Source()] = at

		c.run(ctx, step[source.Source()], func(ctx context.Context) error {
			return source.Refresh(ctx)
		})
	}
}

// due reports a source whose interval is complete at the tick at.
//
// The interval is measured between ticks, and not between the answers of the source. An answer
// arrives some time after its tick, and later still for a source that waits for the ones before
// it in the same tick, so a comparison of answers skips every other tick when the two intervals
// are equal. Ticks arrive one tick interval apart, so half of that interval is room for the jitter
// of the ticker, and it can never let a source be read a whole tick early.
func (c *Controller) due(source Source, at time.Time) bool {
	last, read := c.refreshed[source.Source()]
	if !read {
		return true
	}

	return at.Sub(last) >= source.Interval()-c.interval/2
}

// run carries out one step, and records what it cost. A step that fails is a log record and a
// tick that does less, not a loop that stops.
func (c *Controller) run(ctx context.Context, name string, work func(context.Context) error) {
	if work == nil {
		return
	}

	ctx, span := c.tracer.Start(ctx, "tick."+name)
	defer span.End()

	started := time.Now()
	err := work(ctx)
	c.metrics.Tick(ctx, name, time.Since(started))

	if err != nil {
		span.RecordError(err)
		c.logger.ErrorContext(ctx, "a step of the tick failed", "step", name, "error", err)
	}
}

// observe gives the instruments the state that this tick left.
//
// One observation replaces the one before it, so the loop assembles the whole of it here: a part
// that reported its own series would stop the series of every other part.
func (c *Controller) observe() {
	observation := telemetry.Observation{}
	if c.fleet != nil {
		observation = c.fleet()
	}

	synced := make(map[telemetry.Source]time.Time, len(c.sources))

	for _, source := range c.sources {
		if at := source.At(); !at.IsZero() {
			synced[source.Source()] = at
		}
	}

	observation.Synced = synced

	c.metrics.Observe(observation)
}

// mayAdmit reports whether the tick at may start jobs, and why not when it may not.
//
// The controller starts no job while it has not read the queue, because an empty queue and an
// unread queue look the same, and because a paused queue is an instruction from an operator. It
// starts no job on an answer older than the freshness limit either: a source whose calls fail
// keeps its last answer, and the fleet and the queue move on without it.
func (c *Controller) mayAdmit(at time.Time) (string, bool) {
	queue, ok := c.queue.Latest()
	if !ok {
		return "the controller has not read the queue yet", false
	}

	if queue.Paused {
		return "an operator paused the queue", false
	}

	for _, source := range c.sources {
		if !slices.Contains(admissionRests, source.Source()) {
			continue
		}

		answered := source.At()
		if answered.IsZero() {
			return fmt.Sprintf("the source %s has not answered yet", source.Source()), false
		}

		if age := at.Sub(answered); age > c.freshness {
			return fmt.Sprintf("the last answer of the source %s is %s old, and admission "+
				"needs one inside %s", source.Source(), age.Round(time.Second), c.freshness), false
		}
	}

	return "", true
}
