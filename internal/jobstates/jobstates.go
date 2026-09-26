// Package jobstates holds what Buildkite says about the jobs of the machines of this controller,
// and when the controller asked.
//
// The reaper decides from these states, and a state is worth only as much as its time. Two rules
// of the target design, section 7.1, read that time: a state counts for a machine only when the
// controller asked after the machine existed, and a state older than the freshness limit allows
// only the actions that need no job state. This package records the time. The reaper applies the
// rules, because they are rows of its table.
package jobstates

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Stacks is what this package needs of Buildkite.
type Stacks interface {
	States(ctx context.Context, ids []job.ID) (map[job.ID]string, error)
}

// Fleet gives the machines of this controller, whose jobs this package asks about.
type Fleet interface {
	Latest() (inventory.Snapshot, bool)
}

// Observation is what Buildkite said about one job, and when the controller asked.
type Observation struct {
	// State is the Buildkite state of the job, when Named is true.
	State string

	// Named reports that Buildkite named the job in its answer. A job that the controller asked
	// about and that the answer leaves out is unknown to Buildkite, which is a fact about the job.
	// A job that the controller could not ask about has no observation at all, which is a fact
	// about the call, and the two must never be confused: the first can end a machine, and a
	// failed call that read as the first would end every machine of the fleet.
	Named bool

	// At is when the controller sent the request. The answer describes Buildkite at some moment
	// after that, so this is the earliest time that the state can describe, and every rule that
	// reads it errs towards a state that is older than it is.
	At time.Time
}

// States asks Buildkite about the jobs of the machines of this controller, in one call, at each
// interval of the control loop.
type States struct {
	stacks   Stacks
	fleet    Fleet
	interval time.Duration
	now      func() time.Time

	mu           sync.RWMutex
	observations map[job.ID]Observation
	answered     time.Time
}

// Options configures the states.
type Options struct {
	Stacks Stacks
	Fleet  Fleet

	// Interval is how often Buildkite may be asked. The design states five seconds.
	Interval time.Duration

	// Now is for a test. A nil value gives time.Now.
	Now func() time.Time
}

// New builds the states.
func New(options Options) (*States, error) {
	var problems []error

	if options.Stacks == nil || options.Fleet == nil {
		problems = append(problems, errors.New("the job states need Buildkite and the fleet"))
	}

	if options.Interval <= 0 {
		problems = append(problems, fmt.Errorf("the job states interval is %s, and it must be "+
			"more than zero", options.Interval))
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	return &States{
		stacks:       options.Stacks,
		fleet:        options.Fleet,
		interval:     options.Interval,
		now:          now,
		observations: map[job.ID]Observation{},
	}, nil
}

// Source says which supply of observed state this is.
func (s *States) Source() telemetry.Source {
	return telemetry.SourceJobStates
}

// Interval is how often Buildkite may be asked.
func (s *States) Interval() time.Duration {
	return s.interval
}

// At gives when the last successful answer was asked for, and the zero time while none has been.
func (s *States) At() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.answered
}

// Refresh asks Buildkite about every job that has a machine, in one call.
//
// A call that fails changes nothing. The observations before it keep their times, so they grow
// old and then stale, and the reaper stops acting on them by its own rules. A failed call that
// replaced them with nothing would lose a state that is still true, and one that replaced them
// with an empty answer would make every job look unknown to Buildkite.
func (s *States) Refresh(ctx context.Context) error {
	snapshot, read := s.fleet.Latest()
	if !read {
		return nil
	}

	ids := make([]job.ID, 0, len(snapshot.Owned))
	for _, owned := range snapshot.Owned {
		ids = append(ids, owned.Job)
	}

	asked := s.now()

	var answer map[job.ID]string

	if len(ids) > 0 {
		var err error

		answer, err = s.stacks.States(ctx, ids)
		if err != nil {
			return fmt.Errorf("reading the job states: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// A job whose machine is gone needs no state, and keeping it would grow the map for the life
	// of the process.
	observations := make(map[job.ID]Observation, len(ids))

	for _, id := range ids {
		state, named := answer[id]
		observations[id] = Observation{State: state, Named: named, At: asked}
	}

	s.observations = observations
	s.answered = asked

	return nil
}

// Observed gives what Buildkite said about a job, and reports whether the controller has asked
// about it at all.
func (s *States) Observed(id job.ID) (Observation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	observation, asked := s.observations[id]

	return observation, asked
}
