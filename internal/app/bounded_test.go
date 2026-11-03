// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// hung is an API whose every call waits until its context ends, as a server that accepts a call
// and never answers does. A call that no limit bounds gives up after a second of real time, so
// that the test fails and does not hang.
type hung struct{}

var errNoLimit = errors.New("the call outlived a second, so no limit bounds it")

func (hung) wait(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errNoLimit
	}
}

func (h hung) Register(ctx context.Context) error   { return h.wait(ctx) }
func (h hung) Deregister(ctx context.Context) error { return h.wait(ctx) }

func (h hung) ScheduledJobs(ctx context.Context, _ string, _ int) (buildkite.Page, error) {
	return buildkite.Page{}, h.wait(ctx)
}

func (h hung) Reserve(ctx context.Context, _ []job.ID, _ time.Duration) (
	buildkite.Reservation, error,
) {
	return buildkite.Reservation{}, h.wait(ctx)
}

func (h hung) Tokens(ctx context.Context, _ []job.ID, _ time.Duration) (buildkite.Tokens, error) {
	return buildkite.Tokens{}, h.wait(ctx)
}

func (h hung) States(ctx context.Context, _ []job.ID) (map[job.ID]string, error) {
	return nil, h.wait(ctx)
}

func (h hung) Finish(ctx context.Context, _ job.ID, _ int, _ string) error { return h.wait(ctx) }

func (h hung) Notify(ctx context.Context, _ []buildkite.Notification) error { return h.wait(ctx) }

func (h hung) CreateVM(ctx context.Context, _ orchard.VM) error { return h.wait(ctx) }

func (h hung) ListVMs(ctx context.Context) ([]orchard.VM, error) { return nil, h.wait(ctx) }

func (h hung) DeleteVM(ctx context.Context, _ string) error { return h.wait(ctx) }

func (h hung) ListWorkers(ctx context.Context) ([]orchard.Worker, error) {
	return nil, h.wait(ctx)
}

func (h hung) Events(ctx context.Context, _ string) ([]orchard.Event, error) {
	return nil, h.wait(ctx)
}

// The loop runs on one goroutine, so a call that hangs would hold the reaper and the extensions
// of the reservations with it. Every call to either API ends at its limit, with the error of a
// deadline, which the error metric counts as a timeout.
func TestEveryCallEndsAtItsTimeLimit(t *testing.T) {
	t.Parallel()

	const limit = 10 * time.Millisecond

	stacks := boundedStacks{next: hung{}, limit: limit}
	machines := boundedOrchard{next: hung{}, limit: limit}
	ids := []job.ID{"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"}

	calls := map[string]func(context.Context) error{
		"Register":   stacks.Register,
		"Deregister": stacks.Deregister,
		"ScheduledJobs": func(ctx context.Context) error {
			_, err := stacks.ScheduledJobs(ctx, "", 0)

			return err
		},
		"Reserve": func(ctx context.Context) error {
			_, err := stacks.Reserve(ctx, ids, time.Minute)

			return err
		},
		"Tokens": func(ctx context.Context) error {
			_, err := stacks.Tokens(ctx, ids, time.Minute)

			return err
		},
		"States": func(ctx context.Context) error {
			_, err := stacks.States(ctx, ids)

			return err
		},
		"Finish": func(ctx context.Context) error { return stacks.Finish(ctx, ids[0], -1, "failed") },
		"Notify": func(ctx context.Context) error { return stacks.Notify(ctx, nil) },
		"CreateVM": func(ctx context.Context) error {
			return machines.CreateVM(ctx, orchard.VM{Name: "bk-a-b"})
		},
		"ListVMs": func(ctx context.Context) error {
			_, err := machines.ListVMs(ctx)

			return err
		},
		"DeleteVM": func(ctx context.Context) error { return machines.DeleteVM(ctx, "bk-a-b") },
		"ListWorkers": func(ctx context.Context) error {
			_, err := machines.ListWorkers(ctx)

			return err
		},
		"Events": func(ctx context.Context) error {
			_, err := machines.Events(ctx, "bk-a-b")

			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call(t.Context())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s() = %v, want the deadline of its limit", name, err)
			}

			if got, _ := cause(err); got != errorTimeout {
				t.Errorf("the cause of %s() is %q, want %q", name, got, errorTimeout)
			}
		})
	}
}

// The limit of the design fits the renewal lead of a reservation with its defaults: a tick in
// which every call hangs reaches and sends the extension well inside the lead.
func TestTheCallLimitFitsTheRenewalLead(t *testing.T) {
	t.Parallel()

	const (
		renewBefore = 120 * time.Second // the default of reservationRenewBefore
		before      = 5                 // poll, two inventory calls, job states, progress
	)

	if worst := (before + 1) * callTimeout; worst > renewBefore/2 {
		t.Errorf("a tick in which every call hangs takes %s to extend, want at most half of the "+
			"%s lead, so that the next tick can try again", worst, renewBefore)
	}
}
