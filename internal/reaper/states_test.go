// This test is inside the package, because the rule that it proves is in how the executor reads
// the answer of Buildkite, before any decision sees it.
package reaper

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

type unreachable struct{}

func (unreachable) States(context.Context, []job.ID) (map[job.ID]string, error) {
	return nil, errors.New("buildkite is not available")
}

type answering struct{}

func (answering) States(context.Context, []job.ID) (map[job.ID]string, error) {
	return map[job.ID]string{}, nil
}

// A call that fails gives no states, and not empty ones.
//
// The difference decides nothing in a table that deletes only on an ended job. It decides a great
// deal in the whole table, which deletes the machine of a job that Buildkite does not name: a
// failed call that read as an empty answer would delete every machine of the fleet each time
// Buildkite could not be reached.
func TestAFailedCallGivesNoStatesAndNotEmptyOnes(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	snapshot := inventory.Snapshot{Owned: []inventory.Owned{{Job: "a-job"}}}

	failing := &Reaper{
		stacks: unreachable{}, logger: slog.New(slog.DiscardHandler),
		now: func() time.Time { return at },
	}

	if _, _, observed := failing.states(t.Context(), snapshot); observed {
		t.Error("states() reported an answer for a call that failed")
	}

	working := &Reaper{
		stacks: answering{}, logger: slog.New(slog.DiscardHandler),
		now: func() time.Time { return at },
	}

	_, observedAt, observed := working.states(t.Context(), snapshot)
	if !observed {
		t.Fatal("states() reported no answer for a call that succeeded")
	}

	// The time of the answer is what the freshness rules read.
	if !observedAt.Equal(at) {
		t.Errorf("states() observed at %s, want %s", observedAt, at)
	}
}
