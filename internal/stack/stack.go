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
	// Jobs are the jobs of the first page. The list gives every job of the queue, and the
	// Matcher decides which of them this controller can carry.
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
	size     int
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

	// PageSize asks for a number of jobs. Zero takes the size that Buildkite chooses.
	PageSize int

	// Now is for a test. A nil value gives time.Now.
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
		now = time.Now
	}

	return &Poller{
		stacks:   options.Stacks,
		interval: options.Interval,
		size:     options.PageSize,
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

// Refresh reads the first page of the queue and keeps the answer.
//
// It reads one page. A queue longer than one page means that the fleet is full, and the jobs
// that this page leaves out wait for a later tick: a controller that reads every page would
// spend its rate limit on jobs that it cannot start.
func (p *Poller) Refresh(ctx context.Context) error {
	page, err := p.stacks.ScheduledJobs(ctx, "", p.size)
	if err != nil {
		return fmt.Errorf("reading the queue: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.latest = Queue{Jobs: page.Jobs, Paused: page.Paused, At: p.now()}
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
