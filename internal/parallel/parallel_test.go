// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package parallel_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
)

// Every item gets its work, and each outcome stays at the place of its item.
func TestEachGivesTheOutcomesInTheOrderOfTheItems(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	outcomes := parallel.Each(t.Context(), 3, items, func(_ context.Context, n int) int {
		return n * n
	}, nil)

	for i, outcome := range outcomes {
		if !outcome.Done || outcome.Value != items[i]*items[i] {
			t.Errorf("outcome %d = %+v, want the square of %d", i, outcome, items[i])
		}
	}
}

// No more than the limit run at one time, and as many as the limit do.
func TestEachRunsAtMostTheLimitAtOneTime(t *testing.T) {
	t.Parallel()

	var (
		calls, running, most atomic.Int32
		arrived              sync.WaitGroup
	)

	arrived.Add(4)
	release := make(chan struct{})

	go func() {
		arrived.Wait()
		close(release)
	}()

	parallel.Each(t.Context(), 4, make([]int, 20), func(context.Context, int) bool {
		now := running.Add(1)
		defer running.Add(-1)

		for {
			seen := most.Load()
			if now <= seen || most.CompareAndSwap(seen, now) {
				break
			}
		}

		if calls.Add(1) <= 4 {
			// The first four wait until all four run, which proves that four can.
			arrived.Done()

			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}

		return false
	}, nil)

	if got := most.Load(); got != 4 {
		t.Errorf("at most %d ran at one time, want 4", got)
	}
}

// An outcome that says the API does not answer stops the items that have not started. The items
// that run already finish, and give their outcomes.
func TestEachStartsNoMoreItemsAfterAStop(t *testing.T) {
	t.Parallel()

	var started atomic.Int32

	outcomes := parallel.Each(t.Context(), 2, make([]int, 10), func(context.Context, int) bool {
		started.Add(1)

		return true
	}, func(failed bool) bool { return failed })

	if n := started.Load(); n > 2 {
		t.Errorf("%d items started after the first stop, want at most the limit of 2", n)
	}

	done := 0

	for _, outcome := range outcomes {
		if outcome.Done {
			done++
		}
	}

	if done != int(started.Load()) {
		t.Errorf("%d outcomes are done and %d items started, want the same", done, started.Load())
	}
}

// The end of the context stops the items that have not started, and the work of an item that runs
// sees it.
func TestEachStartsNoMoreItemsAfterTheContextEnds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	var started atomic.Int32

	outcomes := parallel.Each(ctx, 1, make([]int, 5), func(ctx context.Context, _ int) error {
		started.Add(1)
		<-ctx.Done()

		return ctx.Err()
	}, nil)

	if n := started.Load(); n != 1 {
		t.Errorf("%d items started, want only the one that ran when the context ended", n)
	}

	if !outcomes[0].Done || outcomes[0].Value == nil || outcomes[1].Done {
		t.Errorf("the outcomes are %+v, want the first done with the end of the context and the "+
			"rest not started", outcomes)
	}
}

// An empty list runs nothing and gives nothing.
func TestEachOfNothingGivesNothing(t *testing.T) {
	t.Parallel()

	outcomes := parallel.Each(t.Context(), parallel.Limit, []int(nil), func(context.Context, int) int {
		t.Error("the work ran for no item")

		return 0
	}, nil)

	if len(outcomes) != 0 {
		t.Errorf("Each() gave %v, want nothing", outcomes)
	}
}

// The budget of a step is a whole number of call limits, and a step of one call for each item
// starts no more items than the budget can finish.
func TestTheBudgetOfAStepFitsItsItems(t *testing.T) {
	t.Parallel()

	if got := parallel.Budget(10 * time.Second); got != parallel.Rounds*10*time.Second {
		t.Errorf("Budget(10s) = %s, want %d rounds of 10s", got, parallel.Rounds)
	}

	if parallel.PerStep != parallel.Limit*parallel.Rounds {
		t.Errorf("PerStep = %d, want the limit times the rounds", parallel.PerStep)
	}
}
