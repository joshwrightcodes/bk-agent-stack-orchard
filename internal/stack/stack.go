// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stack holds the part of the controller that talks to the queue of Buildkite: the
// registration of this stack, and the poll that reads the jobs that wait.
//
// The two belong together because both are about one queue and one stack key, and because the
// poll gives the value that says whether an operator paused that queue.
//
// Neither type decides anything. The Poller records what Buildkite answered and when, and the
// control loop decides what to do with an answer that is old, because the rule about old data
// belongs to the loop and not to one source (target design, section 7.1).
package stack

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Stacks is what this package needs of Buildkite. It is smaller than the client, so a test of
// the poll needs no HTTP server and no fake of the calls that this package never makes.
type Stacks interface {
	Register(ctx context.Context) error
	Deregister(ctx context.Context) error
	ScheduledJobs(ctx context.Context, cursor string, size int) (buildkite.Page, error)
}

// Queue is one answer of the scheduled-jobs list.
type Queue struct {
	// Jobs are the jobs of the first pages, at most MaxPages of them. The list gives every job
	// of the queue, and the Matcher decides which of them this controller can carry.
	Jobs []buildkite.ScheduledJob

	// Paused reports a queue whose dispatch an operator stopped. The controller then starts no
	// machine, and the machines in progress continue.
	Paused bool

	// At is when Buildkite answered. The control loop reads it to decide whether the answer is
	// still fresh enough to act on.
	At time.Time
}

// Stack registers this controller with Buildkite.
type Stack struct {
	stacks Stacks

	mu         sync.Mutex
	registered bool
}

// New builds the stack.
func New(stacks Stacks) (*Stack, error) {
	if stacks == nil {
		return nil, errors.New("the stack needs a Buildkite client")
	}

	return &Stack{stacks: stacks}, nil
}

// Register announces this controller to Buildkite. The call is idempotent, so a restart repeats
// it and Buildkite keeps one stack.
func (s *Stack) Register(ctx context.Context) error {
	if err := s.stacks.Register(ctx); err != nil {
		return fmt.Errorf("registering the stack: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.registered = true

	return nil
}

// Deregister announces that this controller stops.
//
// It does nothing when the registration did not happen, because a process that could not
// register has nothing to withdraw, and a call that fails at shutdown would hide the failure
// that stopped the process.
func (s *Stack) Deregister(ctx context.Context) error {
	s.mu.Lock()
	registered := s.registered
	s.registered = false
	s.mu.Unlock()

	if !registered {
		return nil
	}

	if err := s.stacks.Deregister(ctx); err != nil {
		return fmt.Errorf("deregistering the stack: %w", err)
	}

	return nil
}

// Poller reads the jobs that wait in the queue.
type Poller struct {
	stacks   Stacks
	interval time.Duration
	slots    func() int
	now      func() time.Time

	mu     sync.RWMutex
	latest Queue
	got    bool
}

// PollerOptions configures the poll.
type PollerOptions struct {
	// Stacks is the Buildkite client.
	Stacks Stacks

	// Interval is how often the poll may run. The design states one second.
	Interval time.Duration

	// Slots gives the number of machine slots of the fleet, at the start of a poll. The poll asks
	// for JobsPerSlot jobs for each slot. A nil function, or an answer of zero or less, means
	// that the slots are not known, and the poll takes the page size that Buildkite chooses.
	Slots func() int

	// Now is the clock of the part. internal/app gives every part the same one.
	Now func() time.Time
}

// NewPoller builds the poll.
func NewPoller(options PollerOptions) (*Poller, error) {
	if options.Stacks == nil {
		return nil, errors.New("the poller needs a Buildkite client")
	}

	if options.Interval <= 0 {
		return nil, fmt.Errorf("the poll interval is %s, and it must be more than zero",
			options.Interval)
	}

	now := options.Now
	if now == nil {
		return nil, errors.New("the poller needs a clock")
	}

	return &Poller{
		stacks:   options.Stacks,
		interval: options.Interval,
		slots:    options.Slots,
		now:      now,
	}, nil
}

// Source says which supply of observed state this is, for the staleness metric and for the log.
func (p *Poller) Source() telemetry.Source {
	return telemetry.SourceScheduledJobs
}

// Interval is how often the source may be read. The control loop decides when that is.
func (p *Poller) Interval() time.Duration {
	return p.interval
}

// JobsPerSlot is how many jobs a page holds for each machine slot of the fleet. A job that the
// controller starts leaves the list, so jobs for the free slots and a few that follow them are
// enough to see what to start, and a longer page spends the rate limit on jobs that wait.
const JobsPerSlot = 3

// MaxPages is the most pages that one poll reads. The list is ordered by Buildkite, and each poll
// starts at its head, so no page is skipped: the jobs that the pages of one poll leave out come
// to the head as the jobs before them start, and a later poll reads them.
const MaxPages = 3

// Refresh reads the head of the queue, up to MaxPages pages, and keeps the answer.
//
// The page size is JobsPerSlot times the slots of the fleet that are known at the start of the
// poll. The total is used, and not the free slots: a full fleet has no free slot, and a page of
// size zero would then be the size that Buildkite chooses, which is the opposite of the intent.
// A queue of more jobs than the pages hold means that the fleet has a backlog, and the jobs that
// these pages leave out wait for a later poll.
//
// The poll keeps nothing when a page fails, so that a half-read queue does not replace a whole
// one.
func (p *Poller) Refresh(ctx context.Context) error {
	size := 0
	if p.slots != nil {
		size = max(p.slots(), 0) * JobsPerSlot
	}

	var (
		jobs   []buildkite.ScheduledJob
		paused bool
		cursor string
	)

	for number := range MaxPages {
		page, err := p.stacks.ScheduledJobs(ctx, cursor, size)
		if err != nil {
			return fmt.Errorf("reading page %d of the queue: %w", number+1, err)
		}

		if number == 0 {
			paused = page.Paused
		}

		jobs = append(jobs, page.Jobs...)

		if page.NextCursor == "" {
			break
		}

		cursor = page.NextCursor
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.latest = Queue{Jobs: jobs, Paused: paused, At: p.now()}
	p.got = true

	return nil
}

// Latest gives the last answer, and reports whether one arrived. A controller that has no answer
// yet must not act, because an empty queue and an unread queue look the same.
func (p *Poller) Latest() (Queue, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.latest, p.got
}

// At gives when the last answer arrived, and the zero time while none has. The control loop
// reports the age of that time, which is what the readiness alert reads.
func (p *Poller) At() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.got {
		return time.Time{}
	}

	return p.latest.At
}
