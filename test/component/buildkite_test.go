package component_test

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

// Buildkite job states that this suite uses. An agent owns a job in the acquired states.
const (
	stateScheduled = "scheduled"
	stateReserved  = "reserved"
	stateRunning   = "running"
	stateFinished  = "finished"
	stateCanceled  = "canceled"
)

// work is one job of the queue as Buildkite holds it.
type work struct {
	id          job.ID
	rules       []string
	matched     bool
	scheduledAt time.Time
	state       string

	// reservedUntil is when the reservation of a reserved job lapses, and the job is scheduled
	// again.
	reservedUntil time.Time

	// acquired is set when an agent takes the job. From then the job belongs to the agent.
	acquired bool
}

func (w *work) terminal() bool {
	return w.state == stateFinished || w.state == stateCanceled
}

// stacksFake is Buildkite for one stack and one queue, on the clock of the suite. It answers as
// the API documents, and it records each call that breaks an invariant.
type stacksFake struct {
	now    func() time.Time
	jobs   map[job.ID]*work
	order  []job.ID
	paused bool

	// down is the answer of every call while Buildkite is not available.
	down error

	// calls holds each call, with the endpoint and the time of the suite, for the request
	// budget. finished holds each job that a finish call ended, and lines each line of a job page.
	calls    []call
	finished []job.ID
	lines    []buildkite.Notification

	// reported is the state of each job in the last answer that the controller got. A finish of a
	// job whose last answer showed an agent is a finish after the controller knew of the agent.
	reported map[job.ID]string

	violations *[]string
}

func newStacks(now func() time.Time, violations *[]string) *stacksFake {
	return &stacksFake{
		now:        now,
		jobs:       map[job.ID]*work{},
		reported:   map[job.ID]string{},
		violations: violations,
	}
}

// schedule adds a job to the queue.
func (s *stacksFake) schedule(id job.ID, matched bool) {
	tag := "xcode=27"
	if !matched {
		tag = "xcode=99"
	}

	s.jobs[id] = &work{
		id:          id,
		rules:       []string{"queue=macos", tag},
		matched:     matched,
		scheduledAt: s.now(),
		state:       stateScheduled,
	}
	s.order = append(s.order, id)
}

// lapse schedules again each reserved job whose reservation ended, as Buildkite does.
func (s *stacksFake) lapse() {
	for _, w := range s.jobs {
		if w.state == stateReserved && !s.now().Before(w.reservedUntil) {
			w.state = stateScheduled
		}
	}
}

func (s *stacksFake) violate(format string, args ...any) {
	*s.violations = append(*s.violations, fmt.Sprintf(format, args...))
}

// call is one request of the controller.
type call struct {
	endpoint string
	at       time.Time
}

// called records a request, and gives the answer of an API that is not available.
func (s *stacksFake) called(endpoint string) error {
	s.calls = append(s.calls, call{endpoint: endpoint, at: s.now()})

	return s.down
}

func (s *stacksFake) Register(context.Context) error   { return s.called("register") }
func (s *stacksFake) Deregister(context.Context) error { return s.called("deregister") }

func (s *stacksFake) ScheduledJobs(context.Context, string, int) (buildkite.Page, error) {
	if err := s.called("list"); err != nil {
		return buildkite.Page{}, err
	}

	s.lapse()

	page := buildkite.Page{Paused: s.paused}

	for _, id := range s.order {
		if w := s.jobs[id]; w.state == stateScheduled {
			page.Jobs = append(page.Jobs, buildkite.ScheduledJob{
				ID:          w.id,
				Rules:       slices.Clone(w.rules),
				ScheduledAt: w.scheduledAt,
				RunnableAt:  w.scheduledAt,
			})
		}
	}

	return page, nil
}

// Reserve holds a scheduled job, and extends the reservation of a reserved one.
func (s *stacksFake) Reserve(_ context.Context, ids []job.ID, expiry time.Duration) (
	buildkite.Reservation, error,
) {
	if err := s.called("reserve"); err != nil {
		return buildkite.Reservation{}, err
	}

	s.lapse()

	var answer buildkite.Reservation

	for _, id := range ids {
		w, known := s.jobs[id]
		if !known || (w.state != stateScheduled && w.state != stateReserved) {
			answer.NotReserved = append(answer.NotReserved, id)

			continue
		}

		w.state = stateReserved
		w.reservedUntil = s.now().Add(expiry)
		answer.Reserved = append(answer.Reserved, id)
	}

	return answer, nil
}

func (s *stacksFake) Tokens(_ context.Context, ids []job.ID, lifetime time.Duration) (
	buildkite.Tokens, error,
) {
	if err := s.called("tokens"); err != nil {
		return buildkite.Tokens{}, err
	}

	var answer buildkite.Tokens

	for _, id := range ids {
		if w, known := s.jobs[id]; known && w.state == stateReserved {
			answer.Issued = append(answer.Issued, buildkite.Token{
				JobID: id, Token: "bkaj_component", ExpiresAt: s.now().Add(lifetime),
			})
		} else {
			answer.NotIssued = append(answer.NotIssued, id)
		}
	}

	return answer, nil
}

func (s *stacksFake) States(_ context.Context, ids []job.ID) (map[job.ID]string, error) {
	if err := s.called("states"); err != nil {
		return nil, err
	}

	s.lapse()

	states := map[job.ID]string{}

	for _, id := range ids {
		if w, known := s.jobs[id]; known {
			states[id] = w.state
			s.reported[id] = w.state
		}
	}

	return states, nil
}

// Finish ends a job that no agent took. Buildkite refuses it for any other job.
func (s *stacksFake) Finish(_ context.Context, id job.ID, _ int, _ string) error {
	if err := s.called("finish"); err != nil {
		return err
	}

	s.lapse()

	// I-5: the controller finishes no job after it learned that an agent took it.
	if s.reported[id] == stateRunning {
		s.violate("I-5: a finish of %s, whose last state showed an agent", id)
	}

	w, known := s.jobs[id]
	if !known || w.acquired || w.state != stateReserved {
		return buildkite.ErrTerminal
	}

	w.state = stateFinished
	s.finished = append(s.finished, id)

	return nil
}

func (s *stacksFake) Notify(_ context.Context, lines []buildkite.Notification) error {
	if err := s.called("notify"); err != nil {
		return err
	}

	s.lines = append(s.lines, lines...)

	return nil
}
