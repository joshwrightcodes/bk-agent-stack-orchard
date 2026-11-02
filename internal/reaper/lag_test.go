// SPDX-License-Identifier: AGPL-3.0-or-later

package reaper_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// A job that ended gives a machine that lives until Orchard accepts the deletion. The time
// between the two is the lag that the first exit criterion bounds, so the reaper records it from
// the first tick that saw the job end, and not from the tick that finally deleted the machine.
func TestTheFinishedToDeletedPhaseIsRecorded(t *testing.T) {
	t.Parallel()

	w := newLagWorld(t)
	clock, reader, machines, built, name := &w.clock, w.reader, w.machines, w.built, w.name

	// Orchard refuses the first deletion: the job has ended, and no deletion is accepted.
	if err := built.Reap(t.Context()); err == nil {
		t.Fatal("Reap() = nil, want the refused deletion to reach the tick")
	}

	if got := finishedToDeleted(t, reader); len(got) != 0 {
		t.Fatalf("the reaper recorded %v before Orchard accepted a deletion, want nothing", got)
	}

	*clock = clock.Add(45 * time.Second)

	delete(machines.refuse, name)

	if err := built.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	got := finishedToDeleted(t, reader)
	if len(got) != 1 || got[0] != 45 {
		t.Errorf("the phase finished_to_deleted holds %v seconds, want one record of 45", got)
	}
}

// lagWorld is a reaper whose clock a test moves, over one finished job whose machine Orchard
// refuses to delete until the test lets it.
type lagWorld struct {
	clock    time.Time
	reader   *sdkmetric.ManualReader
	machines *fleet
	built    *reaper.Reaper
	name     string
}

func newLagWorld(t *testing.T) *lagWorld {
	t.Helper()

	w := &lagWorld{clock: now, name: "bk-macos-prod-" + string(ended)}
	w.reader = sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(w.reader))

	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	w.machines = &fleet{
		read:   true,
		refuse: map[string]bool{w.name: true},
		snapshot: inventory.Snapshot{
			Owned: []inventory.Owned{owned(ended, time.Hour)},
		},
	}

	w.built, err = reaper.New(reaper.Options{
		Stacks:       &pages{},
		States:       &observed{states: map[job.ID]string{ended: "finished"}},
		Reservations: &held{},
		RenewBefore:  120 * time.Second,
		Orchard:      w.machines,
		Fleet:        w.machines,
		Profile:      func(job.ID) string { return "xcode-27" },
		Profiles:     []profile.Profile{{Name: "xcode-27"}},
		Defaults:     limits,
		Freshness:    freshness,
		Metrics:      instruments,
		Logger:       slog.New(&decisions{}),
		Now:          func() time.Time { return w.clock },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the reaper", err)
	}

	return w
}

// Orchard answers a machine that is already gone as a success, so a pass that still lists the
// machine deletes it again. The second deletion is not a second end of the phase.
func TestAMachineThatIsAlreadyGoneRecordsThePhaseOnce(t *testing.T) {
	t.Parallel()

	w := newLagWorld(t)
	delete(w.machines.refuse, w.name)

	for range 2 {
		if err := w.built.Reap(t.Context()); err != nil {
			t.Fatalf("Reap() = %v, want no error", err)
		}

		w.clock = w.clock.Add(5 * time.Second)
	}

	if got := finishedToDeleted(t, w.reader); len(got) != 1 || got[0] != 0 {
		t.Errorf("the phase holds %v, want one record of 0 seconds", got)
	}
}

// A machine that leaves the fleet without the reaper deleting it, such as one that an operator
// removed, must not leave its entry in the memory of the reaper for the life of the process.
func TestTheStartOfThePhaseIsForgottenWhenTheMachineLeaves(t *testing.T) {
	t.Parallel()

	w := newLagWorld(t)

	if err := w.built.Reap(t.Context()); err == nil {
		t.Fatal("Reap() = nil, want the refused deletion to reach the tick")
	}

	if got := w.built.Pending(); got != 1 {
		t.Fatalf("the reaper holds %d starts, want 1 for the machine that stays", got)
	}

	w.machines.snapshot = inventory.Snapshot{}

	if err := w.built.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if got := w.built.Pending(); got != 0 {
		t.Errorf("the reaper holds %d starts after the machine left, want 0", got)
	}
}

// finishedToDeleted gives the sums of the records of the phase from a job that ended to a machine
// that Orchard deleted, in seconds, one for each series.
func finishedToDeleted(t *testing.T, reader sdkmetric.Reader) []float64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	var sums []float64

	for _, scope := range collected.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != "bk_agent_stack_orchard.job.phase.duration" {
				continue
			}

			for _, point := range recorded.Data.(metricdata.Histogram[float64]).DataPoints {
				phase, _ := point.Attributes.Value("bk_agent_stack_orchard.job.phase")
				if phase.AsString() == "finished_to_deleted" {
					sums = append(sums, point.Sum)
				}
			}
		}
	}

	return sums
}
