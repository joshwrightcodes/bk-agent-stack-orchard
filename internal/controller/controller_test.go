package controller_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
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
	deregistered int
	mu           sync.Mutex
}

func (s *registrar) Register(context.Context) error {
	s.recorder.add("register")

	return s.registerErr
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
	due      bool
	err      error
	at       time.Time
	mu       sync.Mutex
	reads    int
}

func (s *source) Source() telemetry.Source { return s.kind }
func (s *source) Due() bool                { return s.due }
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
		Stack:    &registrar{recorder: r},
		Queue:    queue{latest: stack.Queue{At: time.Now()}, read: true},
		Interval: time.Millisecond,
		Metrics:  instruments,
		Logger:   slog.New(slog.DiscardHandler),
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
	settings.Sources = []controller.Source{
		&source{recorder: r, kind: telemetry.SourceScheduledJobs, due: true, at: time.Now()},
	}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }
	settings.Admit = func(context.Context) error { r.add("admit"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "admit") })

	seen := r.seen()
	want := []string{"register", "scheduled_jobs", "reap", "admit"}

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

// Almost every failure here is one call to one API, and the next tick reads the world again. A
// loop that stopped would need an operator for something that fixes itself.
func TestAStepThatFailsDoesNotStopTheLoop(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)
	settings.Sources = []controller.Source{
		&source{
			recorder: r, kind: telemetry.SourceScheduledJobs, due: true,
			err: errors.New("buildkite is not available"),
		},
	}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if !slices.Contains(r.seen(), "scheduled_jobs") {
		t.Errorf("the loop did %v, want it to have tried the source", r.seen())
	}
}

// The tick runs more often than a source, so a source that is not due must not be read.
func TestASourceThatIsNotDueIsNotRead(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	settings := options(t, r)

	idle := &source{recorder: r, kind: telemetry.SourceInventory, due: false}
	settings.Sources = []controller.Source{idle}
	settings.Reap = func(context.Context) error { r.add("reap"); return nil }

	runOnce(t, settings, func() bool { return slices.Contains(r.seen(), "reap") })

	if idle.count() != 0 {
		t.Errorf("the loop read the source %d times, want none while it is not due", idle.count())
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
