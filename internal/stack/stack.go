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
	"log/slog"
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
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

	// Truncated reports a read that stopped at MaxPages with a page left. Jobs behind the pages
	// are hidden, so a job that is not in Jobs may still be in the queue.
	Truncated bool

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
	metrics  *telemetry.Metrics
	logger   *slog.Logger
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

	// Metrics counts the reads that stop at the cap. It is optional.
	Metrics *telemetry.Metrics

	// Logger gets the warning of a read that stops at the cap. A nil logger discards it.
	Logger *slog.Logger

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

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Poller{
		stacks:   options.Stacks,
		interval: options.Interval,
		metrics:  options.Metrics,
		logger:   logger,
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

// PageSize is the number of jobs that the poll asks for in each call. It is 1000, the largest
// value that the Stacks API accepts (Stacks API documentation, read on 2026-11-01). The size does
// not follow the slots of the fleet, because the list holds jobs that never leave it: jobs that
// no profile matches, and jobs for other tags. A page sized for the slots would fill with them.
const PageSize = 1000

// MaxPages is the most requests that one poll makes. The API documentation advises to read the
// first few pages only.
const MaxPages = 3

// Refresh reads the head of the queue, up to MaxPages pages of PageSize jobs, and keeps the
// answer. The API orders the list by the time that the job was scheduled, oldest first.
//
// The poll starts at the head at each tick. Jobs that the controller starts leave the list, but
// jobs that never leave it stay at the head. More than MaxPages times PageSize of these hide the
// jobs after them. That limit is not measured. A read that stops at the cap with a page left is
// logged as a warning and counted, so that an operator can see that jobs may be hidden.
//
// A job that two pages both give enters the answer once, at its first place, because the cursor
// reads a list that changes. The poll keeps nothing when a page fails, so that a half-read queue
// does not replace a whole one.
func (p *Poller) Refresh(ctx context.Context) error {
	var (
		jobs   []buildkite.ScheduledJob
		paused bool
		cursor string
		seen   = map[job.ID]bool{}
	)

	for number := range MaxPages {
		page, err := p.stacks.ScheduledJobs(ctx, cursor, PageSize)
		if err != nil {
			return fmt.Errorf("reading page %d of the queue: %w", number+1, err)
		}

		if number == 0 {
			paused = page.Paused
		}

		for _, scheduled := range page.Jobs {
			if !seen[scheduled.ID] {
				seen[scheduled.ID] = true
				jobs = append(jobs, scheduled)
			}
		}

		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}

	if cursor != "" {
		p.logger.WarnContext(ctx, "the read of the queue stopped at the page cap, and jobs behind "+
			"the pages may be hidden", slog.Int("pages", MaxPages), slog.Int("pageSize", PageSize))

		if p.metrics != nil {
			p.metrics.QueueTruncated(ctx)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.latest = Queue{Jobs: jobs, Paused: paused, Truncated: cursor != "", At: p.now()}
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
