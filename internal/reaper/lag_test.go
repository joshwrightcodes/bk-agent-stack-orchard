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

	clock := now
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	api := &observed{states: map[job.ID]string{ended: "finished"}}
	name := "bk-macos-prod-" + string(ended)
	machines := &fleet{
		read:   true,
		refuse: map[string]bool{name: true},
		snapshot: inventory.Snapshot{
			Owned: []inventory.Owned{owned(ended, time.Hour)},
		},
	}

	built, err := reaper.New(reaper.Options{
		Stacks:       &pages{},
		States:       api,
		Reservations: &held{},
		RenewBefore:  120 * time.Second,
		Orchard:      machines,
		Fleet:        machines,
		Profile:      func(job.ID) string { return "xcode-27" },
		Profiles:     []profile.Profile{{Name: "xcode-27"}},
		Defaults:     limits,
		Freshness:    freshness,
		Metrics:      instruments,
		Logger:       slog.New(&decisions{}),
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the reaper", err)
	}

	// Orchard refuses the first deletion: the job has ended, and no deletion is accepted.
	if err := built.Reap(t.Context()); err == nil {
		t.Fatal("Reap() = nil, want the refused deletion to reach the tick")
	}

	if got := finishedToDeleted(t, reader); len(got) != 0 {
		t.Fatalf("the reaper recorded %v before Orchard accepted a deletion, want nothing", got)
	}

	clock = clock.Add(45 * time.Second)

	delete(machines.refuse, name)

	if err := built.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	got := finishedToDeleted(t, reader)
	if len(got) != 1 || got[0] != 45 {
		t.Errorf("the phase finished_to_deleted holds %v seconds, want one record of 45", got)
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
