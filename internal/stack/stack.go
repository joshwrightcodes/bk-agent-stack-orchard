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
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
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
	// Jobs are the jobs of the pages of one read: the head of the queue, and the pages of the
	// sweep when the queue is longer than the head. The list gives every job of the queue, and the
	// Matcher decides which of them this controller can carry.
	Jobs []buildkite.ScheduledJob

	// Paused reports a queue whose dispatch an operator stopped. The controller then starts no
	// machine, and the machines in progress continue.
	Paused bool

	// Truncated reports a read that is not a whole listing: its pages do not follow one another
	// from the head to the end of the list. A job that is not in Jobs may still be in the queue.
	Truncated bool

	// At is when Buildkite answered. The control loop reads it to decide whether the answer is
	// still fresh enough to act on.
	At time.Time
}

// registerPatience is the longest that Register waits in total for the waits that Buildkite names.
//
// Register runs once, at start-up, before the loop, so a wait there holds no other work. A wait
// that Buildkite names is the cheapest way through a rate limit: the process that ends instead
// starts again after the backoff of the platform, and registers into the same limit. The total
// keeps a limit that does not end from holding a process that cannot start for long.
const registerPatience = 60 * time.Second

// Stack registers this controller with Buildkite.
type Stack struct {
	stacks Stacks

	// sleep waits for a time, or until the context ends. A test replaces it.
	sleep func(ctx context.Context, d time.Duration) error

	mu         sync.Mutex
	registered bool
}

// New builds the stack.
func New(stacks Stacks) (*Stack, error) {
	if stacks == nil {
		return nil, errors.New("the stack needs a Buildkite client")
	}

	return &Stack{stacks: stacks, sleep: retry.Sleep}, nil
}

// Register announces this controller to Buildkite. The call is idempotent, so a restart repeats
// it and Buildkite keeps one stack.
//
// A call that fails with a wait that Buildkite named (Retry-After) waits for it and tries again,
// while the waits stay inside registerPatience in total. Any other failure, and a wait that would
// pass the total, ends this call at once, and the loop decides whether to register again
// (internal/controller). The end of the context ends a wait.
func (s *Stack) Register(ctx context.Context) error {
	var waited time.Duration

	for {
		err := s.stacks.Register(ctx)
		if err == nil {
			break
		}

		after, named := retry.After(err)
		if !named || waited+after > registerPatience {
			return fmt.Errorf("registering the stack: %w", err)
		}

		if slept := s.sleep(ctx, after); slept != nil {
			return fmt.Errorf("registering the stack: %w", errors.Join(err, slept))
		}

		waited += after
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

	// reading serialises the reads, and it guards sweep: the cursor where the next read
	// continues the sweep. It is empty when the next sweep starts after the head.
	reading sync.Mutex
	sweep   string

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

	// Metrics counts the truncated reads. It is optional.
	Metrics *telemetry.Metrics

	// Logger gets the warning of a truncated read. A nil logger discards it.
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

// MaxPages is the most requests that one poll makes, whatever the length of the queue. The API
// documentation advises to read the first few pages only.
const MaxPages = 3

// HeadPages is the number of pages that each poll reads from the head of the list. The pages
// after them are for the sweep, which moves through the rest of the list one read at a time.
const HeadPages = 2

// Refresh reads the queue and keeps the answer. The API orders the list by the time that the job
// was scheduled, oldest first, and it gives no filter and no other order.
//
// Each read takes the head of the list, up to HeadPages pages of PageSize jobs. When the list
// continues, the read takes MaxPages minus HeadPages more pages from the sweep. The sweep starts
// after the head, and each read continues from the cursor where the last read stopped. At the end
// of the list, the sweep starts again after the head. Jobs that never leave the list, such as
// jobs that no profile matches, therefore cannot hide the jobs behind them for ever: a job behind
// them is in one read of each sweep, and a longer flood only makes the sweep longer. The number of
// requests of a read stays MaxPages.
//
// A read is whole when its pages follow one another from the head to the last page. Any other
// read is truncated: it is logged as a warning and counted, so that an operator can see that jobs
// wait behind a long head.
//
// A job that two pages both give enters the answer once, at its first place, because the cursor
// reads a list that changes. The poll keeps nothing when a page fails, so that a half-read queue
// does not replace a whole one. A failed read also starts the sweep again after the head, because
// a cursor that Buildkite refused once may never work again.
//
// Refresh holds a lock for the whole read, so that two reads cannot move the sweep at once.
func (p *Poller) Refresh(ctx context.Context) error {
	p.reading.Lock()
	defer p.reading.Unlock()

	read := listing{seen: map[job.ID]bool{}}

	cursor, err := read.pages(ctx, p.stacks, "", HeadPages)
	if err != nil {
		p.sweep = ""

		return err
	}

	whole := cursor == ""

	if whole {
		p.sweep = ""
	} else {
		start, continued := p.sweep, p.sweep != ""
		if !continued {
			start = cursor
		}

		next, err := read.pages(ctx, p.stacks, start, MaxPages-HeadPages)
		if err != nil {
			p.sweep = ""

			return err
		}

		// The sweep starts again after the head when it reached the end of the list.
		p.sweep = next
		whole = !continued && next == ""
	}

	if !whole {
		p.warn(ctx, read.jobs)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.latest = Queue{Jobs: read.jobs, Paused: read.paused, Truncated: !whole, At: p.now()}
	p.got = true

	return nil
}

// listing collects the pages of one read.
type listing struct {
	jobs   []buildkite.ScheduledJob
	paused bool
	calls  int
	seen   map[job.ID]bool
}

// pages reads up to count pages from the cursor, and gives the cursor of the next page. The
// cursor is empty when the last page was the end of the list.
func (l *listing) pages(ctx context.Context, stacks Stacks, cursor string, count int) (
	string, error,
) {
	for range count {
		page, err := stacks.ScheduledJobs(ctx, cursor, PageSize)
		if err != nil {
			return "", fmt.Errorf("reading page %d of the queue: %w", l.calls+1, err)
		}

		if l.calls == 0 {
			l.paused = page.Paused
		}

		l.calls++

		for _, scheduled := range page.Jobs {
			if !l.seen[scheduled.ID] {
				l.seen[scheduled.ID] = true
				l.jobs = append(l.jobs, scheduled)
			}
		}

		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}

	return cursor, nil
}

// warn writes the warning of a truncated read and counts it. The warning names the pipeline that
// holds the most jobs of the read, which is often the source of a flood.
func (p *Poller) warn(ctx context.Context, jobs []buildkite.ScheduledJob) {
	counts := map[string]int{}

	var (
		pipeline string
		most     int
	)

	for _, scheduled := range jobs {
		counts[scheduled.Pipeline]++

		if n := counts[scheduled.Pipeline]; n > most {
			pipeline, most = scheduled.Pipeline, n
		}
	}

	p.logger.WarnContext(ctx, "the queue is longer than one read, and jobs behind the head of "+
		"the queue wait for the sweep to reach them",
		slog.Int("pages", MaxPages), slog.Int("pageSize", PageSize),
		slog.String("pipeline", pipeline), slog.Int("pipelineJobs", most))

	if p.metrics != nil {
		p.metrics.QueueTruncated(ctx)
	}
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
