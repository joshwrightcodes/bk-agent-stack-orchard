package progress_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/progress"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const carried = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")

var start = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// world is what the tracker reads, which a test moves one tick at a time.
type world struct {
	now       time.Time
	vm        *orchard.VM
	state     string
	timeline  *launcher.Timeline
	lines     []string
	notifyErr error
}

func (w *world) Latest() (inventory.Snapshot, bool) {
	if w.vm == nil {
		return inventory.Snapshot{At: w.now}, true
	}

	return inventory.Snapshot{Owned: []inventory.Owned{{VM: *w.vm, Job: carried}}, At: w.now}, true
}

func (w *world) Observed(job.ID) (jobstates.Observation, bool) {
	if w.state == "" {
		return jobstates.Observation{}, false
	}

	return jobstates.Observation{State: w.state, Named: true, At: w.now}, true
}

func (w *world) Timeline(job.ID) (launcher.Timeline, bool) {
	if w.timeline == nil {
		return launcher.Timeline{}, false
	}

	return *w.timeline, true
}

func (w *world) Notify(_ context.Context, lines []buildkite.Notification) error {
	for _, line := range lines {
		w.lines = append(w.lines, line.Detail)
	}

	return w.notifyErr
}

type recorded struct {
	tracker *progress.Tracker
	reader  *sdkmetric.ManualReader
	spans   *tracetest.InMemoryExporter
}

func track(t *testing.T, w *world) recorded {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	spans := tracetest.NewInMemoryExporter()
	tracers := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))

	t.Cleanup(func() {
		_ = meters.Shutdown(context.Background())
		_ = tracers.Shutdown(context.Background())
	})

	instruments, err := telemetry.NewMetrics(meters.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	tracker, err := progress.New(progress.Options{
		Stacks: w, Fleet: w, States: w, Timelines: w,
		Metrics: instruments, Logger: slog.New(slog.DiscardHandler),
		Tracer: tracers.Tracer("test"), Now: func() time.Time { return w.now },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the tracker", err)
	}

	return recorded{tracker: tracker, reader: reader, spans: spans}
}

// tick moves the world by some seconds, lets it change, and observes it twice: a step is
// reported once, however many ticks see it.
func (r recorded) tick(t *testing.T, w *world, seconds int, change func()) {
	t.Helper()

	w.now = w.now.Add(time.Duration(seconds) * time.Second)
	change()

	for range 2 {
		if err := r.tracker.Observe(t.Context()); err != nil {
			t.Fatalf("Observe() = %v, want no error", err)
		}
	}
}

// phases gives each phase duration that the tracker recorded, in seconds.
func (r recorded) phases(t *testing.T) map[string]float64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := r.reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	durations := map[string]float64{}

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "bk_agent_stack_orchard.job.phase.duration" {
				continue
			}

			for _, point := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				phase, _ := point.Attributes.Value("bk_agent_stack_orchard.job.phase")
				durations[phase.AsString()] += point.Sum
			}
		}
	}

	return durations
}

// T-009. A machine that this process started goes from its request to the agent that takes its
// job, and the job page shows each step once, in order.
func TestTheJobPageShowsEachStepOfTheMachineOnce(t *testing.T) {
	t.Parallel()

	w := &world{now: start, timeline: &launcher.Timeline{
		Profile: "xcode-27", ScheduledAt: start.Add(-30 * time.Second), ReservedAt: start,
	}}
	r := track(t, w)

	vm := &orchard.VM{Name: "bk-macos-prod-" + string(carried), Status: orchard.StatusPending}

	r.tick(t, w, 1, func() { w.vm = vm })
	r.tick(t, w, 20, func() { vm.Worker = "mac-03" })
	r.tick(t, w, 60, func() { vm.Status = orchard.StatusRunning })
	r.tick(t, w, 90, func() { w.state = "running" })

	want := []string{"placed on mac-03", "booting"}
	if !slices.Equal(w.lines, want) {
		t.Errorf("the job page got %q, want %q", w.lines, want)
	}

	// The durations run from the times of the ticks that saw each step.
	wantPhases := map[string]float64{
		"reserved_to_placed":  21,
		"placed_to_running":   60,
		"running_to_accepted": 90,
		"total":               201,
	}

	got := r.phases(t)
	for phase, seconds := range wantPhases {
		if got[phase] != seconds {
			t.Errorf("the phase %s took %v s, want %v s (all: %v)", phase, got[phase], seconds, got)
		}
	}

	// Each change of Orchard is a span event in the trace of the job.
	trace, err := telemetry.TraceID(carried)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	var events []string

	for _, span := range r.spans.GetSpans() {
		if span.SpanContext.TraceID() != trace {
			continue
		}

		for _, event := range span.Events {
			events = append(events, event.Name)
		}
	}

	if !slices.Equal(events, []string{"placed", "running"}) {
		t.Errorf("the trace of the job holds the events %v, want placed and running", events)
	}
}

// After a restart, the job page of a machine that the last process started already shows its
// steps. The new process says nothing of them, and it has no start to measure a phase from.
func TestAMachineOfTheLastProcessRepeatsNoStep(t *testing.T) {
	t.Parallel()

	w := &world{now: start, vm: &orchard.VM{Worker: "mac-03", Status: orchard.StatusRunning}}
	r := track(t, w)

	r.tick(t, w, 1, func() {})
	r.tick(t, w, 30, func() { w.state = "running" })

	if len(w.lines) != 0 {
		t.Errorf("the job page got %q after a restart, want nothing again", w.lines)
	}

	if got := r.phases(t); len(got) != 0 {
		t.Errorf("the tracker recorded %v without a start, want no phase", got)
	}
}

// A line that does not arrive is worth less than the tick.
func TestALineThatDoesNotArriveDoesNotStopTheTick(t *testing.T) {
	t.Parallel()

	w := &world{
		now: start, timeline: &launcher.Timeline{ReservedAt: start},
		notifyErr: errors.New("buildkite is not available"),
	}
	r := track(t, w)

	r.tick(t, w, 1, func() { w.vm = &orchard.VM{Worker: "mac-03"} })
}

func TestNewRefusesAnIncompleteTracker(t *testing.T) {
	t.Parallel()

	if _, err := progress.New(progress.Options{}); err == nil {
		t.Error("New() with nothing = nil, want an error")
	}
}
