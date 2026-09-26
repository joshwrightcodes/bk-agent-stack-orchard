package app_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
