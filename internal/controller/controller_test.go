// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package controller_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// recorder writes down what the loop did, in the order that it did it.
type recorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *recorder) add(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.steps = append(r.steps, step)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.steps)
}

// registrar is the stack of a test.
type registrar struct {
	recorder     *recorder
	registerErr  error
	registerErrs []error
	registered   int
	deregistered int
	mu           sync.Mutex
}

// Register gives the answers of registerErrs first, one for each call, and then registerErr.
func (s *registrar) Register(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.registered++
	s.recorder.add("register")

	if len(s.registerErrs) > 0 {
		err := s.registerErrs[0]
		s.registerErrs = s.registerErrs[1:]

		return err
	}

	return s.registerErr
}

func (s *registrar) registrations() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.registered
}

func (s *registrar) Deregister(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deregistered++
	s.recorder.add("deregister")

	return nil
}

func (s *registrar) withdrawals() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.deregistered
}

// source is one supply of observed state.
type source struct {
	recorder *recorder
	kind     telemetry.Source
	interval time.Duration
	err      error
	at       time.Time
	mu       sync.Mutex
	reads    int
}

func (s *source) Source() telemetry.Source { return s.kind }
func (s *source) Interval() time.Duration  { return s.interval }
func (s *source) At() time.Time            { return s.at }

func (s *source) Refresh(context.Context) error {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()

	s.recorder.add(string(s.kind))

	return s.err
}

func (s *source) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reads
}

// queue gives the answer that the loop reads before it admits a job.
type queue struct {
	latest stack.Queue
	read   bool
}

func (q queue) Latest() (stack.Queue, bool) { return q.latest, q.read }

// options gives a loop whose parts a test controls.
func options(t *testing.T, r *recorder) controller.Options {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	return controller.Options{
		Stack:     &registrar{recorder: r},
		Sources:   fresh(r),
		Queue:     queue{latest: stack.Queue{At: time.Now()}, read: true},
		Interval:  time.Millisecond,
		Freshness: time.Minute,
		Now:       time.Now,
		Metrics:   instruments,
		Logger:    slog.New(slog.DiscardHandler),
	}
}

// fresh gives the sources that admission depends on, each with an answer that has just arrived
// and an interval that no test outlasts.
func fresh(r *recorder) []controller.Source {
	return []controller.Source{
		&source{recorder: r, kind: telemetry.SourceScheduledJobs, interval: time.Hour, at: time.Now()},
		&source{recorder: r, kind: telemetry.SourceInventory, interval: time.Hour, at: time.Now()},
		&source{recorder: r, kind: telemetry.SourceJobStates, interval: time.Hour, at: time.Now()},
	}
}

// runOnce runs the loop until it has done the work of at least one tick.
func runOnce(t *testing.T, settings controller.Options, done func() bool) {
	t.Helper()

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	finished := make(chan error, 1)
	go func() { finished <- loop.Run(ctx) }()

	deadline := time.After(2 * time.Second)

	for !done() {
		select {
		case <-deadline:
			cancel()
			t.Fatal("the loop did not do the work of a tick inside two seconds")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()

	if err := <-finished; err != nil {
		t.Fatalf("Run() = %v, want no error", err)
	}
}

// The steps of a tick run in sequence, and Admission always sees the deletions of the same tick
// because of it. A step that runs at the wrong time would break that.
func TestTheStepsOfATickRunInOrder(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "admit") })

	seen := r.seen()
	want := []string{"register", "scheduled_jobs", "inventory", "job_states", "reap", "admit"}

	for i, step := range want {
		if i >= len(seen) || seen[i] != step {
			t.Fatalf("the loop did %v, want it to start with %v", seen, want)
		}
	}

	if last := seen[len(seen)-1]; last != "deregister" {
		t.Errorf("the loop finished with %q, want it to withdraw the stack", last)
	}
}

// An operator who pauses a queue means it: the controller starts no job, and the machines in
// progress continue, which the reap step is free to look at.
func TestAPausedQueueStopsAdmissionAndNotTheReaper(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	settings.Queue = queue{latest: stack.Queue{Paused: true, At: time.Now()}, read: true}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if slices.Contains(r.seen(), "admit") {
		t.Errorf("the loop admitted a job while the queue was paused: %v", r.seen())
	}
}

// An empty queue and an unread queue look the same, so a controller that has read nothing yet
// must start nothing.
func TestAnUnreadQueueStopsAdmission(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	settings.Queue = queue{read: false}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if slices.Contains(r.seen(), "admit") {
		t.Errorf("the loop admitted a job before it read the queue: %v", r.seen())
	}
}

// A source whose calls fail keeps its last answer, and the fleet and the queue move on without it.
// A job started on that answer can take a slot that is gone, so an old answer stops admission. Old
// job states stop it too: the reaper cannot extend a reservation on them, so a machine started
// then keeps only its first reservation. The reaper still runs, because its own rules decide what
// an old answer allows it.
func TestAnOldAnswerStopsAdmissionAndNotTheReaper(t *testing.T) {
	t.Parallel()

	for _, stale := range []telemetry.Source{
		telemetry.SourceScheduledJobs, telemetry.SourceInventory, telemetry.SourceJobStates,
	} {
		for name, at := range map[string]time.Time{
			"older than the limit": time.Now().Add(-2 * time.Minute),
			"never answered":       {},
		} {
			t.Run(string(stale)+" "+name, func(t *testing.T) {
				t.Parallel()

				r := &recorder{}
				settings := options(t, r)

				for _, s := range settings.Sources {
					if s := s.(*source); s.kind == stale {
						s.at = at
					}
				}

				settings.Reap = func(context.Context) error { r.add("reap"); return nil }
				settings.Admit = func(context.Context) error { r.add("admit"); return nil }

				runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

				if slices.Contains(r.seen(), "admit") {
					t.Errorf("the loop admitted a job on the answer of %s at %v: %v", stale, at,
						r.seen())
				}
			})
		}
	}
}

// After a restart the machines of the last process still run. Admission waits for a reaper pass
// that has seen their job states, which removes the machines of ended jobs and extends every
// reservation first (target design, section 7.4).
func TestAdmissionWaitsForTheFirstJobStates(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	settings.Sources[2].(*source).at = time.Time{}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if slices.Contains(r.seen(), "admit") {
		t.Errorf("the loop admitted a job before the job states answered: %v", r.seen())
	}
}

// Run times each tick with the clock of the controller, and not with the beat of its ticker. A
// ticker gives the time that a beat was due, so after a long tick that time is in the past, and the
// freshness gate then measures an old answer against an old time and admits on it. Here the clock
// of the controller stands at a moment long before the ticker, and the answers are fresh at that
// moment, so only a tick on that clock admits.
func TestRunTimesATickWithTheClockOfTheController(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	moment := time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)

	for _, s := range settings.Sources {
		s.(*source).at = moment
	}

	settings.Now = func() time.Time { return moment }
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "admit") })
}

// clock is a time that a test moves by hand. Run reads it from the goroutine of the loop while
// the test reads it from its own, so it locks.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.at = c.at.Add(by)
}

// The freshness gate measures the answers at the time of the decision, and not at the start of
// the tick. The refresh and the reaper come first and can take long: a source that failed its
// refresh keeps its last answer, and when the reaper then waits on its calls, that answer can be
// older than the limit when admission decides. Here the answers are fresh at the start of the
// tick, and the reap step moves the clock past the limit.
func TestAdmissionMeasuresTheAnswersAtTheTimeOfTheDecision(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	moment := time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)
	wall := &clock{at: moment}

	for _, s := range settings.Sources {
		s.(*source).at = moment
	}

	settings.Now = wall.now
	settings.Reap = func(context.Context) error {
		r.add("reap")
		wall.advance(settings.Freshness + time.Second)

		return nil
	}
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), moment)

	if !slices.Contains(r.seen(), "reap") {
		t.Fatalf("the tick did %v, want it to reap", r.seen())
	}

	if slices.Contains(r.seen(), "admit") {
		t.Errorf("the tick admitted a job on answers older than the limit at the decision: %v",
			r.seen())
	}
}

// Almost every failure here is one call to one API, and the next tick reads the world again. A
// loop that stopped would need an operator for something that fixes itself.
func TestAStepThatFailsDoesNotStopTheLoop(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	settings.Sources = []controller.Source{
		&source{
			recorder: r, kind: telemetry.SourceScheduledJobs,
			err: errors.New("buildkite is not available"),
		},
	}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if !slices.Contains(r.seen(), "scheduled_jobs") {
		t.Errorf("the loop did %v, want it to have tried the source", r.seen())
	}
}

// The tick runs more often than a source with a long interval, so such a source is read at the
// first tick and not again inside its interval.
func TestASourceIsNotReadAgainInsideItsInterval(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)

	idle := &source{recorder: r, kind: telemetry.SourceInventory, interval: time.Hour}
	settings.Sources = []controller.Source{idle}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if idle.count() != 1 {
		t.Errorf("the loop read the source %d times, want once inside its interval", idle.count())
	}
}

// sleeper records each wait of a loop and does not sleep.
type sleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleeper) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.waits = append(s.waits, d)

	return nil
}

func (s *sleeper) waited() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.waits)
}

// A registration that fails because Buildkite does not answer is tried again inside the process.
// A process that stopped instead would wait for the backoff of the platform, which grows to
// minutes, and no reaper pass would run in that time. The waits grow, with jitter, up to a cap,
// and a wait that Buildkite names counts up to that cap.
func TestRunRegistersAgainWhileBuildkiteDoesNotAnswer(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	holder := &registrar{recorder: r, registerErrs: []error{
		retry.Again(errors.New("connection refused"), 0),
		fmt.Errorf("the call ran out of time: %w", context.DeadlineExceeded),
		retry.Again(errors.New("bad gateway"), 0),
		retry.Again(errors.New("slow down"), time.Hour),
	}}
	settings.Stack = holder
	waits := &sleeper{}
	settings.Sleep = waits.sleep
	settings.Random = func() float64 { return 0.5 }
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if got := holder.registrations(); got != 5 {
		t.Errorf("the loop registered %d times, want 5: four failures and a success", got)
	}

	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, time.Minute}
	if got := waits.waited(); !slices.Equal(got, want) {
		t.Errorf("the loop waited %v between the registrations, want %v", got, want)
	}
}

// A refusal is an answer, and another try gets the same one, so the process stops at once and an
// operator reads why.
func TestRunDoesNotRegisterAgainAfterARefusal(t *testing.T) {
	t.Parallel()

	for name, refusal := range map[string]error{
		"a refused token":      fmt.Errorf("registering the stack: %w", buildkite.ErrUnauthorized),
		"a refused request":    errors.New("buildkite answered 422"),
		"an answer of no form": errors.New("the answer does not decode"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := &recorder{}
			settings := options(t, r)
			holder := &registrar{recorder: r, registerErr: refusal}
			settings.Stack = holder
			waits := &sleeper{}
			settings.Sleep = waits.sleep

			loop, err := controller.New(settings)
			if err != nil {
				t.Fatalf("New() = %v, want the loop", err)
			}

			if err := loop.Run(t.Context()); !errors.Is(err, refusal) {
				t.Errorf("Run() = %v, want the refusal", err)
			}

			if got := holder.registrations(); got != 1 {
				t.Errorf("the loop registered %d times, want once", got)
			}

			if got := waits.waited(); len(got) != 0 {
				t.Errorf("the loop waited %v, want no wait", got)
			}
		})
	}
}

// The end of the context ends the tries of the registration, and nothing that was not registered
// is withdrawn.
func TestTheEndOfTheContextEndsTheRegistration(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	holder := &registrar{recorder: r, registerErr: retry.Again(errors.New("connection refused"), 0)}
	settings.Stack = holder

	ctx, cancel := context.WithCancel(t.Context())
	settings.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()

		return ctx.Err()
	}

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want the end of the context", err)
	}

	if holder.registrations() != 1 || holder.withdrawals() != 0 {
		t.Errorf("the loop registered %d times and withdrew %d times, want once and never",
			holder.registrations(), holder.withdrawals())
	}
}

// A controller that cannot register has no stack, so it must stop and say so instead of ticking
// against a stack that Buildkite does not have.
func TestALoopThatCannotRegisterStops(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	stopped := errors.New("buildkite refused the registration")
	holder := &registrar{recorder: r, registerErr: stopped}
	settings.Stack = holder

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	if err := loop.Run(t.Context()); !errors.Is(err, stopped) {
		t.Errorf("Run() = %v, want the error of the registration", err)
	}

	// Nothing was registered, so nothing may be withdrawn.
	if holder.withdrawals() != 0 {
		t.Errorf("the loop withdrew %d times, want none", holder.withdrawals())
	}
}

func TestNewRefusesAnIncompleteLoop(t *testing.T) {
	t.Parallel()

	r := &recorder{}

	for name, change := range map[string]func(*controller.Options){
		"no stack":       func(o *controller.Options) { o.Stack = nil },
		"no queue":       func(o *controller.Options) { o.Queue = nil },
		"no instruments": func(o *controller.Options) { o.Metrics = nil },
		"no logger":      func(o *controller.Options) { o.Logger = nil },
		"no interval":    func(o *controller.Options) { o.Interval = 0 },
		"no freshness":   func(o *controller.Options) { o.Freshness = 0 },
		"no clock":       func(o *controller.Options) { o.Now = nil },
		"admission without the inventory": func(o *controller.Options) {
			o.Admit = func(context.Context) error { return nil }
			o.Sources = o.Sources[:1]
		},
		"admission without the queue": func(o *controller.Options) {
			o.Admit = func(context.Context) error { return nil }
			o.Sources = o.Sources[1:]
		},
		"admission without the job states": func(o *controller.Options) {
			o.Admit = func(context.Context) error { return nil }
			o.Sources = o.Sources[:2]
		},
	} {
		t.Run(name, func(t *testing.T) {
			settings := options(t, r)
			change(&settings)

			if _, err := controller.New(settings); err == nil {
				t.Errorf("New() with %s = nil, want an error", name)
			}
		})
	}
}

// The heartbeat is zero while the loop has not started, because the process then registers its
// stack, and a restart cures nothing that Buildkite causes.
func TestTheHeartbeatIsZeroBeforeTheLoopStarts(t *testing.T) {
	t.Parallel()

	loop, err := controller.New(options(t, &recorder{}))
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	if got := loop.Heartbeat(); !got.IsZero() {
		t.Fatalf("Heartbeat() before Run = %s, want the zero time", got)
	}
}

// The loop beats when it starts, after the registration, and not first at the end of its first
// tick. A first tick that never ends is the case that liveness exists for, and a heartbeat that
// stayed zero through it would never fail liveness.
func TestTheHeartbeatStartsWithTheLoop(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	moment := time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)
	settings.Now = (&clock{at: moment}).now

	reaping := make(chan struct{})
	settings.Reap = func(ctx context.Context) error {
		close(reaping)
		<-ctx.Done()

		return ctx.Err()
	}

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)

	go func() { finished <- loop.Run(ctx) }()

	select {
	case <-reaping:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("the loop did not reach its first tick inside two seconds")
	}

	got := loop.Heartbeat()

	cancel()

	if err := <-finished; err != nil {
		t.Fatalf("Run() = %v, want no error", err)
	}

	if !got.Equal(moment) {
		t.Errorf("Heartbeat() in a first tick that has not ended = %s, want %s, when the loop "+
			"started", got, moment)
	}
}

// The heartbeat is the time that a tick ended, and not the time that it began. A beat from the
// start of a tick adds the length of that tick to the age that liveness measures, so two long
// ticks in a row could fail liveness while the loop still works.
func TestTheHeartbeatIsTheEndOfTheLastTick(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	moment := time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)
	wall := &clock{at: moment}
	settings.Now = wall.now
	settings.Reap = func(context.Context) error {
		wall.advance(5 * time.Minute)

		return nil
	}

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), moment)

	if got, want := loop.Heartbeat(), moment.Add(5*time.Minute); !got.Equal(want) {
		t.Errorf("Heartbeat() after a tick of 5 minutes = %s, want %s, when the tick ended", got,
			want)
	}
}

// measured gives the options of a loop whose instruments a test reads.
func measured(t *testing.T, r *recorder) (controller.Options, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	settings := options(t, r)
	settings.Metrics = instruments

	return settings, reader
}

// series gives the names of the instruments that hold a value, and the value of each series of
// stack.registered.
func series(t *testing.T, reader *sdkmetric.ManualReader) (map[string]int, []int64) {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	names := map[string]int{}

	var registered []int64

	for _, scope := range collected.ScopeMetrics {
		for _, one := range scope.Metrics {
			switch data := one.Data.(type) {
			case metricdata.Gauge[float64]:
				names[one.Name] += len(data.DataPoints)
			case metricdata.Gauge[int64]:
				names[one.Name] += len(data.DataPoints)

				if one.Name == "bk_agent_stack_orchard.stack.registered" {
					for _, point := range data.DataPoints {
						registered = append(registered, point.Value)
					}
				}
			case metricdata.Sum[int64]:
				names[one.Name] += len(data.DataPoints)
			}
		}
	}

	return names, registered
}

// An alert that compares a value never fires on a series that does not exist. The series of the
// loop therefore exist from the start, also while Buildkite does not answer the registration: the
// age of each source that has not answered counts from the start, and the registration reads 0.
func TestTheSeriesOfTheLoopExistWhileTheStackRegisters(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings, reader := measured(t, r)
	settings.Sources = []controller.Source{
		&source{recorder: r, kind: telemetry.SourceScheduledJobs, interval: time.Hour},
		&source{recorder: r, kind: telemetry.SourceInventory, interval: time.Hour},
		&source{recorder: r, kind: telemetry.SourceJobStates, interval: time.Hour},
	}
	settings.Stack = &registrar{recorder: r, registerErrs: []error{
		retry.Again(errors.New("connection refused"), 0),
	}}

	waiting, release := make(chan struct{}), make(chan struct{})
	settings.Sleep = func(context.Context, time.Duration) error {
		close(waiting)
		<-release

		return nil
	}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }

	loop, err := controller.New(settings)
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)

	go func() { finished <- loop.Run(ctx) }()

	<-waiting

	names, registered := series(t, reader)

	if names["bk_agent_stack_orchard.source.staleness"] != 3 {
		t.Errorf("source.staleness has %d series while the stack registers, want one for each "+
			"source", names["bk_agent_stack_orchard.source.staleness"])
	}

	if names["bk_agent_stack_orchard.loop.heartbeat.age"] != 1 {
		t.Error("loop.heartbeat.age has no series while the stack registers, want one")
	}

	if !slices.Equal(registered, []int64{0}) {
		t.Errorf("stack.registered reads %v while the stack registers, want [0]", registered)
	}

	close(release)

	for !slices.Contains(r.seen(), "reap") {
		time.Sleep(time.Millisecond)
	}

	if _, registered := series(t, reader); !slices.Equal(registered, []int64{1}) {
		t.Errorf("stack.registered reads %v after the registration, want [1]", registered)
	}

	cancel()

	if err := <-finished; err != nil {
		t.Fatalf("Run() = %v, want no error", err)
	}
}

// blocked gives the value of each series of admission.blocked, by its reason.
func blocked(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	values := map[string]int64{}

	for _, scope := range collected.ScopeMetrics {
		for _, one := range scope.Metrics {
			if one.Name != "bk_agent_stack_orchard.admission.blocked" {
				continue
			}

			for _, point := range one.Data.(metricdata.Gauge[int64]).DataPoints {
				reason, _ := point.Attributes.Value("bk_agent_stack_orchard.admission.reason")
				values[reason.AsString()] = point.Value
			}
		}
	}

	return values
}

// While the gate of admission is closed, jobs wait and nothing in a debug log is visible to an
// operator. A gauge says that the gate is closed and why, with one series for each reason, so
// that the series exist while the gate is open too.
func TestTheGaugeOfTheGateSaysWhyAdmissionStopped(t *testing.T) {
	t.Parallel()

	reasons := []string{
		"queue_unread", "queue_paused", "scheduled_jobs_stale", "inventory_unanswered",
		"inventory_stale", "job_states_unanswered", "job_states_stale",
	}

	old := time.Now().Add(-2 * time.Minute)

	for name, test := range map[string]struct {
		change func(*controller.Options)
		want   string
	}{
		"an open gate":    {func(*controller.Options) {}, ""},
		"an unread queue": {func(o *controller.Options) { o.Queue = queue{} }, "queue_unread"},
		"a paused queue":  {func(o *controller.Options) { o.Queue = pausedQueue() }, "queue_paused"},
		"an old queue":    {sourceAt(telemetry.SourceScheduledJobs, old), "scheduled_jobs_stale"},
		"no fleet":        {sourceAt(telemetry.SourceInventory, time.Time{}), "inventory_unanswered"},
		"an old fleet":    {sourceAt(telemetry.SourceInventory, old), "inventory_stale"},
		"no job states":   {sourceAt(telemetry.SourceJobStates, time.Time{}), "job_states_unanswered"},
		"old job states":  {sourceAt(telemetry.SourceJobStates, old), "job_states_stale"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			settings, reader := measured(t, &recorder{})
			test.change(&settings)

			loop, err := controller.New(settings)
			if err != nil {
				t.Fatalf("New() = %v, want the loop", err)
			}

			loop.Tick(t.Context(), time.Now())

			got := blocked(t, reader)
			if len(got) != len(reasons) {
				t.Errorf("admission.blocked has the series %v, want one for each of %v", got, reasons)
			}

			for _, reason := range reasons {
				want := int64(0)
				if reason == test.want {
					want = 1
				}

				if got[reason] != want {
					t.Errorf("admission.blocked{%s} = %d, want %d", reason, got[reason], want)
				}
			}
		})
	}
}

func pausedQueue() queue {
	return queue{latest: stack.Queue{Paused: true, At: time.Now()}, read: true}
}

// sourceAt gives a change that sets when one source last answered.
func sourceAt(kind telemetry.Source, at time.Time) func(*controller.Options) {
	return func(o *controller.Options) {
		for _, s := range o.Sources {
			if s := s.(*source); s.kind == kind {
				s.at = at
			}
		}
	}
}
