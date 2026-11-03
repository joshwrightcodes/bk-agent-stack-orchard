// SPDX-License-Identifier: AGPL-3.0-or-later

package launcher_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
)

// numbered gives n jobs, named by their number.
func numbered(n int) []job.ID {
	ids := make([]job.ID, 0, n)
	for i := range n {
		ids = append(ids, job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", i)))
	}

	return ids
}

// blocking answers as an Orchard whose creates wait. Each create waits until the test lets it go
// or its context ends, and the fake counts how many wait at the same time.
type blocking struct {
	mu        sync.Mutex
	asked     int
	waiting   int
	most      int
	deadlines []time.Duration

	// answer is what a create gives when it ends without the end of its context.
	answer error

	// release lets every create go. A nil channel lets none go.
	release chan struct{}

	// full is closed once limit creates wait together.
	full   chan struct{}
	filled bool
	limit  int
}

func (b *blocking) CreateVM(ctx context.Context, _ orchard.VM) error {
	b.mu.Lock()
	b.asked++
	b.waiting++
	b.most = max(b.most, b.waiting)

	if deadline, has := ctx.Deadline(); has {
		b.deadlines = append(b.deadlines, time.Until(deadline))
	}

	if b.full != nil && b.waiting == b.limit && !b.filled {
		b.filled = true
		close(b.full)
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.waiting--
		b.mu.Unlock()
	}()

	select {
	case <-b.release:
		return b.answer
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The creates of one tick run at the same time, up to the limit, so that a burst of jobs costs
// the time of a few creates and not the sum of them (target design, section 4).
func TestTheCreatesOfATickRunAtTheSameTimeUpToTheLimit(t *testing.T) {
	t.Parallel()

	api := &stacks{}
	fleet := &blocking{release: make(chan struct{}), full: make(chan struct{}), limit: parallel.Limit}
	started := build(t, api, fleet)

	go func() {
		select {
		case <-fleet.full:
		case <-time.After(5 * time.Second):
		}

		close(fleet.release)
	}()

	ids := numbered(parallel.PerStep)
	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(ids...)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if fleet.asked != len(ids) || fleet.most != parallel.Limit {
		t.Errorf("orchard saw %d creates and at most %d at once, want %d creates and %d at once",
			fleet.asked, fleet.most, len(ids), parallel.Limit)
	}

	for _, id := range ids {
		if started.Profile(id) != pilot.Name {
			t.Errorf("the launcher does not know the machine of %s", id)
		}
	}
}

// An Orchard that does not answer fails each create the same way, so the creates that wait are
// not sent. Their jobs keep their reservations, which lapse, and admission meets them again.
func TestAnOrchardThatDoesNotAnswerStopsTheCreatesThatWait(t *testing.T) {
	t.Parallel()

	for name, answer := range map[string]error{
		"a server error":    retry.Again(errors.New("orchard answered 503"), 0),
		"no answer in time": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			api := &stacks{}
			fleet := &blocking{release: make(chan struct{}), answer: answer}
			close(fleet.release)

			log := &records{Handler: slog.DiscardHandler}
			started := buildWithLog(t, api, fleet, log)

			ids := numbered(parallel.PerStep)
			if err := started.Launch(t.Context(), admission.Result{Admit: candidates(ids...)},
				inventory.Snapshot{}); err != nil {
				t.Fatalf("Launch() = %v, want no error", err)
			}

			if fleet.asked > parallel.Limit {
				t.Errorf("orchard saw %d creates, want at most the %d that ran when the first "+
					"failed", fleet.asked, parallel.Limit)
			}

			// A create that ran and failed gets the line of a failed request, and a create that
			// the step did not send gets no line.
			var lines []buildkite.Notification
			for _, call := range api.notified {
				lines = append(lines, call...)
			}

			if len(lines) != fleet.asked {
				t.Errorf("buildkite saw %d lines, want one for each of the %d creates that ran",
					len(lines), fleet.asked)
			}

			for _, line := range lines {
				if !strings.HasPrefix(line.Detail, "VM request failed") {
					t.Errorf("a job page reads %q, want only the line of a failed request",
						line.Detail)
				}
			}

			if !strings.Contains(strings.Join(log.failures(), "\n"), "could not start a machine") {
				t.Errorf("the log has %q, want the failed create", log.failures())
			}
		})
	}
}

// A refusal of one create is an answer about that machine, and the creates after it go on.
func TestARefusalDoesNotStopTheCreatesThatWait(t *testing.T) {
	t.Parallel()

	api := &stacks{}
	fleet := &machines{err: errors.New("orchard answered 400 Bad Request: invalid")}
	started := build(t, api, fleet)

	ids := numbered(parallel.PerStep)
	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(ids...)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(fleet.created) != len(ids) {
		t.Errorf("orchard saw %d creates, want %d", len(fleet.created), len(ids))
	}
}

// The launcher reserves only the jobs that one step can start inside its time budget. A job that
// it reserves and cannot start keeps its reservation for five minutes, which another stack could
// have used. The rest wait in the queue for the next tick.
func TestALaunchReservesOnlyWhatOneStepCanStart(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	ids := numbered(parallel.PerStep + 6)
	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(ids...)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.reserved) != 1 || len(api.reserved[0]) != parallel.PerStep {
		t.Fatalf("buildkite saw the reservations %v, want one call of %d jobs", api.reserved,
			parallel.PerStep)
	}

	if started.PerTick() != parallel.PerStep {
		t.Errorf("PerTick() = %d, want %d", started.PerTick(), parallel.PerStep)
	}

	for _, id := range ids[parallel.PerStep:] {
		if _, held := started.ReservedUntil(id); held {
			t.Errorf("the launcher reserved %s, past what one step can start", id)
		}
	}
}

// The creates of a tick end at the time budget of the step, so that an Orchard that accepts calls
// and never answers holds the tick for a few call limits and not one limit for each job.
func TestTheCreatesEndAtTheBudgetOfTheStep(t *testing.T) {
	t.Parallel()

	const limit = 20 * time.Millisecond

	api := &stacks{}
	fleet := &blocking{}
	started := buildTimed(t, api, fleet, &records{Handler: slog.DiscardHandler}, limit)

	began := time.Now()

	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(numbered(4)...)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if took := time.Since(began); took > parallel.Budget(limit)+time.Second {
		t.Errorf("the launch took %s, want about the budget of %s", took, parallel.Budget(limit))
	}

	if len(fleet.deadlines) == 0 {
		t.Fatal("orchard saw no create")
	}

	for _, left := range fleet.deadlines {
		if left > parallel.Budget(limit) {
			t.Errorf("a create had %s left, want at most the budget of %s", left,
				parallel.Budget(limit))
		}
	}
}

// The step budget comes from the time limit of a call, so a launcher without one cannot have it.
func TestNewRefusesNoTimeLimitOfACall(t *testing.T) {
	t.Parallel()

	_, err := launcher.New(launcher.Options{})
	if err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Errorf("New() = %v, want a refusal that names the time limit of a call", err)
	}
}
