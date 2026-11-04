// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package reaper_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// stalling is an Orchard whose deletions wait. Each deletion waits until the test lets it go or
// its context ends, and the fake counts how many wait at the same time.
type stalling struct {
	*fleet

	mu        sync.Mutex
	asked     int
	waiting   int
	most      int
	deadlines []time.Duration

	// answer is what a deletion gives when it ends without the end of its context.
	answer error

	// release lets every deletion go. A nil channel lets none go.
	release chan struct{}

	// full is closed once limit deletions wait together.
	full   chan struct{}
	filled bool
	limit  int

	// order is shared with the reservations, so that a test sees which call came first.
	order *[]string
}

func (s *stalling) DeleteVM(ctx context.Context, name string) error {
	s.mu.Lock()
	s.asked++
	s.waiting++
	s.most = max(s.most, s.waiting)

	if deadline, has := ctx.Deadline(); has {
		s.deadlines = append(s.deadlines, time.Until(deadline))
	}

	if s.order != nil {
		*s.order = append(*s.order, "delete")
	}

	if s.full != nil && s.waiting == s.limit && !s.filled {
		s.filled = true
		close(s.full)
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}()

	select {
	case <-s.release:
		if s.answer != nil {
			return s.answer
		}

		return s.fleet.DeleteVM(ctx, name)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ordered is the reservations of the launcher, which write the extension into a shared order.
type ordered struct {
	held

	mu    *sync.Mutex
	order *[]string
}

func (o *ordered) Extend(ctx context.Context, ids []job.ID) error {
	o.mu.Lock()
	*o.order = append(*o.order, "extend")
	o.mu.Unlock()

	return o.held.Extend(ctx, ids)
}

// buildStalling gives a reaper over an Orchard whose deletions wait, with the time limit of a call
// that the test chose.
func buildStalling(t *testing.T, states map[job.ID]string, machines *stalling,
	reservations reaper.Reservations, limit time.Duration, log *decisions,
) *reaper.Reaper {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	built, err := reaper.New(reaper.Options{
		Stacks:       &pages{},
		States:       &observed{states: states},
		Reservations: reservations,
		RenewBefore:  120 * time.Second,
		CallTimeout:  limit,
		Orchard:      machines,
		Fleet:        machines,
		Profiles:     []profile.Profile{{Name: "xcode-27"}},
		Defaults:     limits,
		Freshness:    freshness,
		Metrics:      instruments,
		Logger:       slog.New(log),
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the reaper", err)
	}

	return built
}

// The deletions of one pass run at the same time, up to the limit, so that a wave of jobs that
// end together costs the time of a few deletions and not the sum of them (target design, section
// 4).
func TestTheDeletionsOfAPassRunAtTheSameTimeUpToTheLimit(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(parallel.PerStep)
	machines := &stalling{
		fleet:   &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}},
		release: make(chan struct{}), full: make(chan struct{}), limit: parallel.Limit,
	}

	go func() {
		select {
		case <-machines.full:
		case <-time.After(5 * time.Second):
		}

		close(machines.release)
	}()

	if err := buildStalling(t, states, machines, &held{}, time.Second, &decisions{}).
		Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != len(list) || machines.most != parallel.Limit {
		t.Errorf("orchard deleted %d machines with at most %d at once, want %d with %d at once",
			len(machines.deleted), machines.most, len(list), parallel.Limit)
	}

	if len(machines.forgot) != len(list) {
		t.Errorf("the fleet forgot %d machines, want every deleted one", len(machines.forgot))
	}
}

// An Orchard that does not answer fails each deletion the same way, so the deletions that wait
// are not sent. The next tick decides again, from what it observes then.
func TestAnOrchardThatDoesNotAnswerStopsTheDeletionsThatWait(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(parallel.PerStep)
	machines := &stalling{
		fleet:   &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}},
		release: make(chan struct{}), answer: retry.Again(errors.New("orchard answered 503"), 0),
	}
	close(machines.release)

	log := &decisions{}

	if err := buildStalling(t, states, machines, &held{}, time.Second, log).
		Reap(t.Context()); err == nil {
		t.Fatal("Reap() = nil, want the failed deletions")
	}

	if machines.asked > parallel.Limit {
		t.Errorf("orchard saw %d deletions, want at most the %d that ran when the first failed",
			machines.asked, parallel.Limit)
	}

	if !strings.Contains(strings.Join(log.messages(), "\n"), "next tick") {
		t.Errorf("the log has %q, want a record of the machines left for the next tick",
			log.messages())
	}
}

// The extensions of a pass go before its deletions. An extension keeps a job with this stack, and
// a deletion that waits for an Orchard that does not answer must not hold it back.
func TestTheExtensionsGoBeforeTheDeletions(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(3)
	states[running] = "reserved"
	list = append(list, owned(running, time.Hour))

	var order []string

	machines := &stalling{
		fleet:   &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}},
		release: make(chan struct{}), order: &order,
	}
	close(machines.release)

	// One lock guards the order, for the extension and for the deletions.
	reservations := &ordered{mu: &machines.mu, order: &order}

	if err := buildStalling(t, states, machines, reservations, time.Second, &decisions{}).
		Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(order) != 4 || order[0] != "extend" {
		t.Errorf("the calls came in the order %v, want the extension and then three deletions",
			order)
	}
}

// The deletions of a pass end at the time budget of the step, so that an Orchard that accepts
// calls and never answers holds the tick for a few call limits and not one for each machine.
func TestTheDeletionsEndAtTheBudgetOfTheStep(t *testing.T) {
	t.Parallel()

	const limit = 20 * time.Millisecond

	states, list := finishedJobs(4)
	machines := &stalling{fleet: &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}}}

	began := time.Now()

	if err := buildStalling(t, states, machines, &held{}, limit, &decisions{}).
		Reap(t.Context()); err == nil {
		t.Fatal("Reap() = nil, want the deletions that ran out of time")
	}

	if took := time.Since(began); took > parallel.Budget(limit)+time.Second {
		t.Errorf("the pass took %s, want about the budget of %s", took, parallel.Budget(limit))
	}

	if len(machines.deadlines) == 0 {
		t.Fatal("orchard saw no deletion")
	}

	for _, left := range machines.deadlines {
		if left > parallel.Budget(limit) {
			t.Errorf("a deletion had %s left, want at most the budget of %s", left,
				parallel.Budget(limit))
		}
	}
}

// The step budget comes from the time limit of a call, so a reaper without one cannot have it.
func TestTheReaperNeedsTheTimeLimitOfACall(t *testing.T) {
	t.Parallel()

	_, err := reaper.New(reaper.Options{})
	if err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Errorf("New() = %v, want a refusal that names the time limit of a call", err)
	}
}
