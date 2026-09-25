package reaper_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const (
	ended   = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a70")
	running = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a71")
	ancient = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a72")
)

// buildkite answers get-states.
type buildkite struct {
	states map[job.ID]string
	err    error
	calls  int
}

func (b *buildkite) States(context.Context, []job.ID) (map[job.ID]string, error) {
	b.calls++

	return b.states, b.err
}

// fleet answers as Orchard: it holds the snapshot, and it records the deletions.
type fleet struct {
	snapshot inventory.Snapshot
	read     bool
	refuse   map[string]bool
	deleted  []string
}

func (f *fleet) Latest() (inventory.Snapshot, bool) { return f.snapshot, f.read }

func (f *fleet) DeleteVM(_ context.Context, name string) error {
	if f.refuse[name] {
		return errors.New("orchard refused the deletion")
	}

	f.deleted = append(f.deleted, name)

	return nil
}

// decisions keeps the records that the reaper writes at info level and above.
type decisions struct {
	mu      sync.Mutex
	records []slog.Record
}

func (d *decisions) Enabled(context.Context, slog.Level) bool { return true }
func (d *decisions) WithAttrs([]slog.Attr) slog.Handler       { return d }
func (d *decisions) WithGroup(string) slog.Handler            { return d }

func (d *decisions) Handle(_ context.Context, record slog.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if record.Level >= slog.LevelInfo {
		d.records = append(d.records, record.Clone())
	}

	return nil
}

func (d *decisions) messages() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]string, 0, len(d.records))
	for _, record := range d.records {
		out = append(out, record.Message)
	}

	return out
}

// owned gives a machine of this controller for a job, created some time ago.
func owned(id job.ID, age time.Duration) inventory.Owned {
	return inventory.Owned{
		Job: id,
		VM: orchard.VM{
			Name:      "bk-macos-prod-" + string(id),
			CreatedAt: now.Add(-age),
			Worker:    "mac-01",
			Status:    orchard.StatusRunning,
		},
	}
}

func build(t *testing.T, api *buildkite, machines *fleet, log *decisions) *reaper.Reaper {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	built, err := reaper.New(reaper.Options{
		Stacks:    api,
		Orchard:   machines,
		Fleet:     machines,
		Profiles:  []profile.Profile{{Name: "xcode-27"}},
		Defaults:  limits,
		Freshness: freshness,
		Metrics:   instruments,
		Logger:    slog.New(log),
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the reaper", err)
	}

	return built
}

// The walking skeleton ends here: a job finished, and its machine goes.
func TestTheMachineOfAFinishedJobGoesAndTheOthersStay(t *testing.T) {
	t.Parallel()

	api := &buildkite{states: map[job.ID]string{ended: "finished", running: "running"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Hour), owned(running, time.Hour)},
	}}
	log := &decisions{}

	if err := build(t, api, machines, log).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 || machines.deleted[0] != "bk-macos-prod-"+string(ended) {
		t.Errorf("orchard deleted %v, want only the machine of the finished job", machines.deleted)
	}

	// The design asks for exactly one decision record for each action.
	if got := log.messages(); len(got) != 1 {
		t.Errorf("the reaper wrote %v, want one decision record", got)
	}
}

// All the states arrive in one call, because one call for each machine would spend the rate
// limit of the whole organisation on a busy fleet.
func TestTheStatesArriveInOneCall(t *testing.T) {
	t.Parallel()

	api := &buildkite{states: map[job.ID]string{}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Hour), owned(running, time.Hour)},
	}}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if api.calls != 1 {
		t.Errorf("the reaper asked Buildkite %d times, want 1", api.calls)
	}
}

// A controller that cannot reach Buildkite still removes a machine that outlived its limit, and
// deletes nothing on a state that it could not read.
func TestWithoutStatesOnlyTheLifetimeActs(t *testing.T) {
	t.Parallel()

	api := &buildkite{err: errors.New("buildkite is not available")}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Hour), owned(ancient, 26*time.Hour)},
	}}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 || machines.deleted[0] != "bk-macos-prod-"+string(ancient) {
		t.Errorf("orchard deleted %v, want only the machine that outlived its limit",
			machines.deleted)
	}
}

// A job that Buildkite did not name is not a job that ended. The row for it is not in the table
// yet, and until it is, the machine stays.
func TestAJobThatBuildkiteDidNotNameKeepsItsMachine(t *testing.T) {
	t.Parallel()

	api := &buildkite{states: map[job.ID]string{}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(running, time.Hour)},
	}}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 0 {
		t.Errorf("orchard deleted %v, want nothing", machines.deleted)
	}
}

// One machine that Orchard will not delete must not stop the others. The next tick decides the
// same way and tries again, so nothing needs to remember the failure.
func TestADeletionThatFailsDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	api := &buildkite{states: map[job.ID]string{ended: "finished", running: "finished"}}
	machines := &fleet{
		read:   true,
		refuse: map[string]bool{"bk-macos-prod-" + string(ended): true},
		snapshot: inventory.Snapshot{
			Owned: []inventory.Owned{owned(ended, time.Hour), owned(running, time.Hour)},
		},
	}

	err := build(t, api, machines, &decisions{}).Reap(t.Context())
	if err == nil {
		t.Error("Reap() = nil, want the failure to reach the tick")
	}

	if !slices.Contains(machines.deleted, "bk-macos-prod-"+string(running)) {
		t.Errorf("orchard deleted %v, want the machine beside the one that failed",
			machines.deleted)
	}
}

// A controller that has not read Orchard yet knows of no machine, and asks Buildkite nothing.
func TestNothingHappensBeforeTheFleetIsRead(t *testing.T) {
	t.Parallel()

	api := &buildkite{}
	machines := &fleet{read: false}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if api.calls != 0 || len(machines.deleted) != 0 {
		t.Error("the reaper acted before the fleet was read")
	}
}

func TestNewRefusesAnIncompleteReaper(t *testing.T) {
	t.Parallel()

	if _, err := reaper.New(reaper.Options{}); err == nil {
		t.Error("New() with nothing = nil, want an error")
	}
}
