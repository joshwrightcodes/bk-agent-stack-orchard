// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/app"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// clock is a moment years after the clock of the machine that runs the test, so that a part that
// reads time.Now instead of the clock that it was given decides differently.
var clock = time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)

const waiting = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")

// stacks answers as Buildkite does for one job that waits, and that every call then holds.
type stacks struct{}

func (stacks) Register(context.Context) error   { return nil }
func (stacks) Deregister(context.Context) error { return nil }

func (stacks) ScheduledJobs(context.Context, string, int) (buildkite.Page, error) {
	return buildkite.Page{Jobs: []buildkite.ScheduledJob{{
		ID:          waiting,
		Rules:       []string{"queue=macos", "xcode=27"},
		ScheduledAt: clock.Add(-time.Minute),
		RunnableAt:  clock.Add(-time.Minute),
	}}}, nil
}

func (stacks) Reserve(_ context.Context, ids []job.ID, _ time.Duration) (buildkite.Reservation, error) {
	return buildkite.Reservation{Reserved: ids}, nil
}

func (stacks) Tokens(_ context.Context, ids []job.ID, lifetime time.Duration) (buildkite.Tokens, error) {
	var issued []buildkite.Token
	for _, id := range ids {
		issued = append(issued, buildkite.Token{JobID: id, Token: "bkaj_test", ExpiresAt: clock.Add(lifetime)})
	}

	return buildkite.Tokens{Issued: issued}, nil
}

func (stacks) States(_ context.Context, ids []job.ID) (map[job.ID]string, error) {
	states := map[job.ID]string{}
	for _, id := range ids {
		states[id] = "reserved"
	}

	return states, nil
}

func (stacks) Finish(context.Context, job.ID, int, string) error      { return nil }
func (stacks) Notify(context.Context, []buildkite.Notification) error { return nil }

// fleet is one worker that reported at the time of the clock, and the machines created on it.
type fleet struct {
	mu      sync.Mutex
	created []string
}

func (f *fleet) CreateVM(_ context.Context, spec orchard.VM) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.created = append(f.created, spec.Name)

	return nil
}

func (f *fleet) ListVMs(context.Context) ([]orchard.VM, error) { return nil, nil }
func (f *fleet) DeleteVM(context.Context, string) error        { return nil }

func (f *fleet) ListWorkers(context.Context) ([]orchard.Worker, error) {
	return []orchard.Worker{{
		Name:     "mac-01",
		LastSeen: clock,
		Resources: map[string]uint64{
			orchard.ResourceVMs:       2,
			orchard.ResourceCores:     16,
			orchard.ResourceMemoryMiB: 65536,
		},
	}}, nil
}

func (f *fleet) Events(context.Context, string) ([]orchard.Event, error) { return nil, nil }

func settings(t *testing.T) *config.Config {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `controllerID: app-test
buildkite:
  queue: macos
orchard:
  url: https://orchard.internal:6120
  serviceAccount: bk-agent-stack-orchard
profiles:
  - name: xcode-27
    match: {xcode: "27"}
    image: ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	secrets := map[string]string{
		"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN": "bkct_test",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":         "orchard_test",
	}

	loaded, err := config.Load(path, func(name string) string { return secrets[name] })
	if err != nil {
		t.Fatalf("loading the configuration: %v", err)
	}

	return loaded
}

func quiet(t *testing.T) *telemetry.Telemetry {
	t.Helper()

	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		t.Setenv("OTEL_"+signal+"_EXPORTER", "none")
	}

	recorder, err := telemetry.Setup(t.Context(), telemetry.Options{Output: new(discard)})
	if err != nil {
		t.Fatalf("starting telemetry: %v", err)
	}

	t.Cleanup(func() { _ = recorder.Shutdown(context.Background()) })

	return recorder
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

// The loop runs on the clock that it was given. The tick is years after the clock of the machine,
// so a source that stamped its answer with time.Now would give an answer years old at the tick,
// and admission would start nothing. The forbidigo rule of the lint configuration keeps every
// other part off the clock of the machine.
func TestTheLoopRunsOnTheClockThatItWasGiven(t *testing.T) {
	orchardFake := &fleet{}

	loop, err := app.Build(app.Options{
		Config:    settings(t),
		Telemetry: quiet(t),
		Stacks:    stacks{},
		Orchard:   orchardFake,
		Now:       func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), clock)

	if len(orchardFake.created) != 1 {
		t.Errorf("the tick created the machines %v, want the one of the waiting job",
			orchardFake.created)
	}
}

func TestBuildRefusesAnIncompleteController(t *testing.T) {
	complete := app.Options{
		Config:    settings(t),
		Telemetry: quiet(t),
		Stacks:    stacks{},
		Orchard:   &fleet{},
		Now:       func() time.Time { return clock },
	}

	for name, change := range map[string]func(*app.Options){
		"no configuration": func(o *app.Options) { o.Config = nil },
		"no telemetry":     func(o *app.Options) { o.Telemetry = nil },
		"no Stacks API":    func(o *app.Options) { o.Stacks = nil },
		"no Orchard API":   func(o *app.Options) { o.Orchard = nil },
		"no clock":         func(o *app.Options) { o.Now = nil },
	} {
		t.Run(name, func(t *testing.T) {
			options := complete
			change(&options)

			if _, err := app.Build(options); err == nil {
				t.Errorf("Build() with %s = nil, want an error", name)
			}
		})
	}
}

// broken is an Orchard whose calls fail, as one that is not available.
type broken struct{ fleet }

func (*broken) ListVMs(context.Context) ([]orchard.VM, error) {
	return nil, errors.New("orchard answered 503 Service Unavailable")
}

// Every call to either API is counted when it fails: the parts see the APIs only through the
// counter that app.Build puts in front of them.
func TestAFailedCallIsCountedAsAnErrorOfItsSystem(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	loop, err := app.Build(app.Options{
		Config:    settings(t),
		Telemetry: &telemetry.Telemetry{Metrics: instruments, Logger: slog.New(slog.DiscardHandler), Tracer: tracenoop.NewTracerProvider().Tracer("")},
		Stacks:    stacks{},
		Orchard:   &broken{},
		Now:       func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), clock)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "cicd.system.errors" {
				continue
			}

			for _, point := range m.Data.(metricdata.Sum[int64]).DataPoints {
				component, _ := point.Attributes.Value("cicd.system.component")
				kind, _ := point.Attributes.Value("error.type")

				if component.AsString() == "orchard" && kind.AsString() == "_OTHER" && point.Value == 1 {
					return
				}
			}
		}
	}

	t.Errorf("the metrics %+v hold no failed Orchard call", collected.ScopeMetrics)
}

// job.match and job.admit go into the trace of the job once each, however many ticks the job
// waits, and the tick that wrote them links to them.
func TestAJobIsMatchedAndAdmittedOnceInItsTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	quiet := quiet(t)

	loop, err := app.Build(app.Options{
		Config: settings(t),
		Telemetry: &telemetry.Telemetry{
			Metrics: quiet.Metrics, Logger: slog.New(slog.DiscardHandler),
			Tracer: provider.Tracer("test"),
		},
		Stacks:  stacks{},
		Orchard: &fleet{},
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	for i := range 3 {
		loop.Tick(t.Context(), clock.Add(time.Duration(i)*time.Second))
	}

	want, err := telemetry.TraceID(waiting)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	count := map[string]int{}
	var admit trace.SpanID

	for _, span := range exporter.GetSpans() {
		if span.SpanContext.TraceID() == want {
			count[span.Name]++
		}

		if span.Name == "job.admit" {
			admit = span.SpanContext.SpanID()
		}
	}

	if count["job.match"] != 1 || count["job.admit"] != 1 {
		t.Errorf("the trace of the job holds %v, want job.match and job.admit once each", count)
	}

	for _, span := range exporter.GetSpans() {
		if span.Name != "tick.admit" {
			continue
		}

		for _, link := range span.Links {
			if link.SpanContext.SpanID() == admit {
				return
			}
		}
	}

	t.Error("no tick.admit span links to the job.admit span")
}

// full is a fleet with no room, so a job waits there tick after tick.
type full struct{ fleet }

func (*full) ListWorkers(context.Context) ([]orchard.Worker, error) {
	return []orchard.Worker{{Name: "mac-01", LastSeen: clock, Resources: map[string]uint64{}}}, nil
}

// A job that waits is matched once, not at each tick: a span each second would bury the trace of
// a job that waits fifteen minutes.
func TestAJobThatWaitsIsMatchedOnce(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	loop, err := app.Build(app.Options{
		Config: settings(t),
		Telemetry: &telemetry.Telemetry{
			Metrics: quiet(t).Metrics, Logger: slog.New(slog.DiscardHandler),
			Tracer: provider.Tracer("test"),
		},
		Stacks:  stacks{},
		Orchard: &full{},
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	for i := range 5 {
		loop.Tick(t.Context(), clock.Add(time.Duration(i)*time.Second))
	}

	count := map[string]int{}
	for _, span := range exporter.GetSpans() {
		count[span.Name]++
	}

	if count["job.match"] != 1 || count["job.admit"] != 0 {
		t.Errorf("five ticks wrote %v, want job.match once and no job.admit", count)
	}
}

// crowded is a fleet with one slot, and a machine that a person left in it. The machine stays in
// the listing until the reaper deletes it.
type crowded struct {
	fleet
	deleted bool
}

func (c *crowded) ListWorkers(context.Context) ([]orchard.Worker, error) {
	return []orchard.Worker{{Name: "mac-01", LastSeen: clock, Resources: map[string]uint64{
		orchard.ResourceVMs: 1, orchard.ResourceCores: 16, orchard.ResourceMemoryMiB: 65536,
	}}}, nil
}

func (c *crowded) ListVMs(context.Context) ([]orchard.VM, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.deleted {
		return nil, nil
	}

	return []orchard.VM{{
		Name: "bk-app-test-left-behind", Worker: "mac-01", Status: orchard.StatusRunning,
		Resources: map[string]uint64{orchard.ResourceVMs: 1, orchard.ResourceCores: 8},
	}}, nil
}

func (c *crowded) DeleteVM(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.deleted = true

	return nil
}

// The steps of a tick run in sequence, and admission sees the deletions of the same tick (target
// design, section 4). A job that waits for the slot of a machine that the reaper deletes starts
// in that tick, and not one tick later.
func TestAdmissionSeesTheRoomThatTheReaperFreedInTheSameTick(t *testing.T) {
	machines := &crowded{}

	loop, err := app.Build(app.Options{
		Config:    settings(t),
		Telemetry: quiet(t),
		Stacks:    stacks{},
		Orchard:   machines,
		Now:       func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), clock)

	machines.mu.Lock()
	defer machines.mu.Unlock()

	if !machines.deleted || len(machines.created) != 1 {
		t.Errorf("one tick deleted=%v and created %v, want the orphan deleted and a machine for the waiting job",
			machines.deleted, machines.created)
	}
}

// ended is the job of a machine that a person left running after its job finished.
const ended = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7e")

// afterwards answers for a job that finished, and that is not in the queue any more.
type afterwards struct{ stacks }

func (afterwards) ScheduledJobs(context.Context, string, int) (buildkite.Page, error) {
	return buildkite.Page{}, nil
}

func (afterwards) States(_ context.Context, ids []job.ID) (map[job.ID]string, error) {
	states := map[job.ID]string{}
	for _, id := range ids {
		states[id] = "finished"
	}

	return states, nil
}

// remnant is a fleet with the machine of a job that finished. Orchard refuses or accepts the
// deletion of it, as the test says, and an accepted deletion removes it from the next listing.
type remnant struct {
	fleet
	refuse bool
	gone   bool
}

func (r *remnant) ListVMs(context.Context) ([]orchard.VM, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.gone {
		return nil, nil
	}

	return []orchard.VM{{
		Name: "bk-app-test-" + string(ended), Worker: "mac-01", Status: orchard.StatusRunning,
		Resources: map[string]uint64{orchard.ResourceVMs: 1, orchard.ResourceCores: 8},
	}}, nil
}

func (r *remnant) DeleteVM(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.refuse {
		return errors.New("orchard refused the deletion")
	}

	r.gone = true

	return nil
}

// leftover ticks the loop once over a machine whose job finished, and reads the gauge of the
// machines that remain, which the first exit criterion watches.
func leftover(t *testing.T, refuse bool) int64 {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	loop, err := app.Build(app.Options{
		Config:    settings(t),
		Telemetry: &telemetry.Telemetry{Metrics: instruments, Logger: slog.New(slog.DiscardHandler), Tracer: tracenoop.NewTracerProvider().Tracer("")},
		Stacks:    afterwards{},
		Orchard:   &remnant{refuse: refuse},
		Now:       func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), clock)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "bk_agent_stack_orchard.vm.leftover" {
				continue
			}

			points := m.Data.(metricdata.Gauge[int64]).DataPoints
			if len(points) != 1 {
				t.Fatalf("the leftover gauge holds %+v, want one point", points)
			}

			return points[0].Value
		}
	}

	t.Fatal("no instrument reports the leftover machines")

	return 0
}

// The gauge of leftover machines counts a machine whose job ended and that the reaper did not
// delete. It reads the machine after the reaper acted in the same tick, so a deletion that Orchard
// accepted does not show. A gauge that stayed 0 would pass the pilot exit criterion without a
// check, so the test reads it through the loop and not through the instrument alone.
func TestTheLeftoverGaugeCountsOnlyAMachineThatTheReaperCouldNotDelete(t *testing.T) {
	if got := leftover(t, true); got != 1 {
		t.Errorf("the gauge holds %d after Orchard refused the deletion, want 1", got)
	}

	if got := leftover(t, false); got != 0 {
		t.Errorf("the gauge holds %d after Orchard accepted the deletion, want 0", got)
	}
}

// measure builds the loop over the two APIs, runs one tick, and gives what the instruments then
// hold.
func measure(t *testing.T, api buildkite.Stacks, machines orchard.API) []metricdata.Metrics {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	loop, err := app.Build(app.Options{
		Config: settings(t),
		Telemetry: &telemetry.Telemetry{
			Metrics: instruments, Logger: slog.New(slog.DiscardHandler),
			Tracer: tracenoop.NewTracerProvider().Tracer(""),
		},
		Stacks:  api,
		Orchard: machines,
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("Build() = %v, want the loop", err)
	}

	loop.Tick(t.Context(), clock)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	var all []metricdata.Metrics
	for _, scope := range collected.ScopeMetrics {
		all = append(all, scope.Metrics...)
	}

	return all
}

// levels gives the value of each series of an integer instrument, by its attributes.
func levels(recorded []metricdata.Metrics, name string) map[string]int64 {
	values := map[string]int64{}

	for _, one := range recorded {
		if one.Name != name {
			continue
		}

		var points []metricdata.DataPoint[int64]

		switch data := one.Data.(type) {
		case metricdata.Sum[int64]:
			points = data.DataPoints
		case metricdata.Gauge[int64]:
			points = data.DataPoints
		}

		for _, point := range points {
			var key []string
			for _, pair := range point.Attributes.ToSlice() {
				key = append(key, pair.Value.String())
			}

			values[strings.Join(key, ",")] = point.Value
		}
	}

	return values
}

// unread is a Buildkite whose queue does not answer, so the controller has read no job.
type unread struct{ stacks }

func (unread) ScheduledJobs(context.Context, string, int) (buildkite.Page, error) {
	return buildkite.Page{}, errors.New("buildkite answered 503")
}

// An alert that compares a value never fires on a series that does not exist. Each profile of the
// configuration therefore has its series from the first tick, with 0 where nothing is counted.
func TestEachProfileHasItsSeriesFromTheFirstTick(t *testing.T) {
	recorded := measure(t, unread{}, &fleet{})

	if got := levels(recorded, "bk_agent_stack_orchard.job.waiting.count"); !maps.Equal(got,
		map[string]int64{"xcode-27": 0}) {
		t.Errorf("job.waiting.count holds %v, want 0 for the profile", got)
	}

	want := map[string]int64{
		"xcode-27,unplaced": 0, "xcode-27,booting": 0, "xcode-27,running": 0, "xcode-27,failed": 0,
	}
	if got := levels(recorded, "bk_agent_stack_orchard.vm.count"); !maps.Equal(got, want) {
		t.Errorf("vm.count holds %v, want %v", got, want)
	}

	if got := levels(recorded, "cicd.worker.count"); !maps.Equal(got,
		map[string]int64{"busy": 0, "offline": 0}) {
		t.Errorf("cicd.worker.count holds %v, want 0 for busy and offline", got)
	}
}

// blind is an Orchard whose workers do not answer, so the inventory has no answer and the gate of
// admission stays closed.
type blind struct{ fleet }

func (*blind) ListWorkers(context.Context) ([]orchard.Worker, error) {
	return nil, errors.New("orchard answered 503")
}

// Jobs keep arriving while the gate of admission is closed, and an outage is when they wait the
// longest. The gauges of the waiting jobs therefore read the queue at each tick, and the gauge of
// the gate says why no job starts.
func TestTheWaitingJobsAreCountedWhileTheGateIsClosed(t *testing.T) {
	recorded := measure(t, stacks{}, &blind{})

	if got := levels(recorded, "bk_agent_stack_orchard.job.waiting.count"); got["xcode-27"] != 1 {
		t.Errorf("job.waiting.count holds %v, want the one job of the queue", got)
	}

	var aged bool

	for _, one := range recorded {
		if one.Name != "bk_agent_stack_orchard.job.waiting.oldest.age" {
			continue
		}

		for _, point := range one.Data.(metricdata.Gauge[float64]).DataPoints {
			aged = aged || point.Value > 0
		}
	}

	if !aged {
		t.Error("job.waiting.oldest.age holds no age, want the age of the waiting job")
	}

	if got := levels(recorded, "bk_agent_stack_orchard.admission.blocked"); got["inventory_unanswered"] != 1 {
		t.Errorf("admission.blocked holds %v, want 1 for inventory_unanswered", got)
	}
}
