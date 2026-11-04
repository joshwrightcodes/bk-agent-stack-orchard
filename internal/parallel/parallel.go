// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

// Package parallel runs the API calls of one step of the tick at the same time, with a limit.
//
// The steps of a tick run in sequence (target design, section 4), and a step that makes one call
// for each machine would make the tick as long as the sum of its calls. The design lets the calls
// of one step run at the same time, with a limit, so that a step costs a few call limits and not
// one for each machine.
//
// The work of an item runs on its own goroutine, so it must not write state that the loop owns.
// It gives an outcome, and the caller applies the outcomes on the goroutine of the loop after
// Each returns. The maps of the parts then keep one writer.
package parallel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Limit is the most calls of one step that run at the same time. Eight calls at once is small
// beside what Orchard and the Stacks API serve, and it makes a step of 24 calls cost the time of
// three.
const Limit = 8

// Rounds is how many calls, one after another, each of the Limit places of a step may wait for.
// The time budget of a step is Rounds times the time limit of one call.
const Rounds = 3

// PerStep is the most items of one call each that a step can start and still finish inside its
// budget, when every call takes up to its whole time limit.
const PerStep = Limit * Rounds

// Budget gives the time budget of a step whose calls each have the time limit call.
func Budget(call time.Duration) time.Duration {
	return Rounds * call
}

// Outcome is what the work of one item gave. Done is false for an item whose work never started,
// because the step stopped first.
type Outcome[R any] struct {
	Value R
	Done  bool
}

// Each runs work for each item, with at most limit of them at the same time, and gives the
// outcomes in the order of the items.
//
// It starts no more items after the context ends, or after an outcome for which stop reports
// true, such as a call that found the API not answering: the items after it would wait for the
// same API, and fail the same way. A nil stop never stops. The items that run already finish, and
// Each returns only after all of them did.
func Each[T, R any](ctx context.Context, limit int, items []T, work func(context.Context, T) R,
	stop func(R) bool,
) []Outcome[R] {
	outcomes := make([]Outcome[R], len(items))
	places := make(chan struct{}, max(limit, 1))

	var (
		running sync.WaitGroup
		stopped atomic.Bool
	)

	for i, item := range items {
		select {
		case places <- struct{}{}:
		case <-ctx.Done():
		}

		// A place and the end of the context can arrive together, and the select takes either.
		if ctx.Err() != nil || stopped.Load() {
			break
		}

		running.Go(func() {
			defer func() { <-places }()

			value := work(ctx, item)
			outcomes[i] = Outcome[R]{Value: value, Done: true}

			if stop != nil && stop(value) {
				stopped.Store(true)
			}
		})
	}

	running.Wait()

	return outcomes
}
