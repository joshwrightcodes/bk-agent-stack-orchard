// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// callTimeout is the time limit of one call to Buildkite or to Orchard, with all of its tries and
// waits (internal/retry).
//
// One goroutine runs the loop, and the steps of a tick run in sequence, so a call that hangs
// holds every step after it: the extensions of the reservations too. The limit keeps a tick in
// which both APIs hang inside the renewal lead of a reservation.
// With the defaults, a reservation lasts 300 s (reservationExpiry), and the reaper extends it
// once less than 120 s remain (reservationRenewBefore). Before the extension, a tick makes these
// calls: one poll of the queue (a page that fails ends the read), two inventory calls, one call
// for the job states, and one for the progress notices. That is five calls, and the extension
// itself is a sixth. At 10 s each, a tick in which every call hangs reaches the extension in
// 50 s, and the extension answers or fails in 60 s, which leaves the next tick room to try again
// inside the lead. A poll whose three pages each answer just inside the limit adds 20 s more, and
// still fits.
//
// The reaper and the launcher make a call for each VM that they act on. Those calls run
// parallel.Limit at a time inside a budget of parallel.Rounds limits, and the first call that
// finds an API not answering stops the calls that wait, so their number does not set the length of
// a tick. The longest tick, in which every call takes its whole limit and answers, is 21 calls or
// 210 s, and a tick in which both APIs hang is about 110 s (target design, section 4).
//
// 10 s is also long enough for the calls that answer: the Stacks API and Orchard answer in well
// under a second, and the policy adds at most 1.5 s of waits.
const callTimeout = 10 * time.Second

// bound gives a context that ends after the limit, and the function that releases it.
func bound(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, limit)
}

// boundedStacks is the Stacks API with a time limit on each call. The wrapper is the one place
// that sees every call, so no call site can forget the limit.
type boundedStacks struct {
	next  buildkite.Stacks
	limit time.Duration
}

func (s boundedStacks) Register(ctx context.Context) error {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.Register(ctx)
}

func (s boundedStacks) Deregister(ctx context.Context) error {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.Deregister(ctx)
}

func (s boundedStacks) ScheduledJobs(ctx context.Context, cursor string, size int) (
	buildkite.Page, error,
) {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.ScheduledJobs(ctx, cursor, size)
}

func (s boundedStacks) Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (
	buildkite.Reservation, error,
) {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.Reserve(ctx, ids, expiry)
}

func (s boundedStacks) Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (
	buildkite.Tokens, error,
) {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.Tokens(ctx, ids, lifetime)
}

func (s boundedStacks) States(ctx context.Context, ids []job.ID) (map[job.ID]string, error) {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.States(ctx, ids)
}

func (s boundedStacks) Finish(ctx context.Context, id job.ID, exitStatus int, detail string) error {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.Finish(ctx, id, exitStatus, detail)
}

func (s boundedStacks) Notify(ctx context.Context, notifications []buildkite.Notification) error {
	ctx, cancel := bound(ctx, s.limit)
	defer cancel()

	return s.next.Notify(ctx, notifications)
}

// boundedOrchard is the Orchard API with a time limit on each call.
type boundedOrchard struct {
	next  orchard.API
	limit time.Duration
}

func (o boundedOrchard) CreateVM(ctx context.Context, spec orchard.VM) error {
	ctx, cancel := bound(ctx, o.limit)
	defer cancel()

	return o.next.CreateVM(ctx, spec)
}

func (o boundedOrchard) ListVMs(ctx context.Context) ([]orchard.VM, error) {
	ctx, cancel := bound(ctx, o.limit)
	defer cancel()

	return o.next.ListVMs(ctx)
}

func (o boundedOrchard) DeleteVM(ctx context.Context, name string) error {
	ctx, cancel := bound(ctx, o.limit)
	defer cancel()

	return o.next.DeleteVM(ctx, name)
}

func (o boundedOrchard) ListWorkers(ctx context.Context) ([]orchard.Worker, error) {
	ctx, cancel := bound(ctx, o.limit)
	defer cancel()

	return o.next.ListWorkers(ctx)
}

func (o boundedOrchard) Events(ctx context.Context, name string) ([]orchard.Event, error) {
	ctx, cancel := bound(ctx, o.limit)
	defer cancel()

	return o.next.Events(ctx, name)
}
