// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package component_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
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
//
// The controller calls it from several goroutines at once, so each call holds mu while it reads or
// changes the state. The knobs (paused, down, failing, hangs) change only between ticks.
type stacksFake struct {
	mu sync.Mutex

	now    func() time.Time
	jobs   map[job.ID]*work
	order  []job.ID
	paused bool

	// down is the answer of every call while Buildkite is not available.
	down error

	// failing holds the answer of one endpoint that fails while the others answer.
	failing map[string]error

	// hangs makes every call wait for the end of its context, as an API that accepts a call and
	// never answers does.
	hangs bool

	// nameless makes each answer of the job states name no job, as an answer whose field
	// Buildkite renamed does. omit holds, for each job, how many more answers leave it out while
	// they name the other jobs, as a partial answer does.
	nameless bool
	omit     map[job.ID]int

	// takenAtFinish holds the jobs that an agent acquires just before a finish of the controller
	// arrives, after the state that the finish read.
	takenAtFinish map[job.ID]bool

	// afterTokens runs once, after the next token call has issued its tokens and before it
	// answers. A test uses it to make the rest of the tick slow, on the clock of the suite.
	afterTokens func()

	// expires holds when the token of each job expires.
	expires map[job.ID]time.Time

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
		now:      now,
		jobs:     map[job.ID]*work{},
		reported: map[job.ID]string{},
		omit:     map[job.ID]int{},

		takenAtFinish: map[job.ID]bool{},
		expires:       map[job.ID]time.Time{},
		failing:       map[string]error{},
		violations:    violations,
	}
}

// schedule adds a job to the queue. The tags of a matched job are those of the profile of the
// default configuration, and the tags of an unmatched job match no profile.
func (s *stacksFake) schedule(id job.ID, matched bool) {
	tag := "xcode=27"
	if !matched {
		tag = "xcode=99"
	}

	s.scheduleRules(id, []string{"queue=macos", tag}, matched)
}

// scheduleRules adds a job with the rules that the caller gives. The caller says whether a
// profile carries the job.
func (s *stacksFake) scheduleRules(id job.ID, rules []string, matched bool) {
	s.jobs[id] = &work{
		id:          id,
		rules:       rules,
		matched:     matched,
		scheduledAt: s.now(),
		state:       stateScheduled,
	}
	s.order = append(s.order, id)
}

// forget removes a job from Buildkite, as if the job never existed.
func (s *stacksFake) forget(id job.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.jobs, id)
	s.order = slices.DeleteFunc(s.order, func(other job.ID) bool { return other == id })
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
	record(s.violations, fmt.Sprintf(format, args...))
}

// call is one request of the controller.
type call struct {
	endpoint string
	at       time.Time
}

// called records a request, and gives the answer of an API that is not available, or waits for
// the end of the call when the API hangs.
func (s *stacksFake) called(ctx context.Context, endpoint string) error {
	s.mu.Lock()
	s.calls = append(s.calls, call{endpoint: endpoint, at: s.now()})
	s.mu.Unlock()

	if s.hangs {
		return hang(ctx, s.violations, endpoint)
	}

	if err := s.failing[endpoint]; err != nil {
		return err
	}

	return s.down
}

func (s *stacksFake) Register(ctx context.Context) error   { return s.called(ctx, "register") }
func (s *stacksFake) Deregister(ctx context.Context) error { return s.called(ctx, "deregister") }

// ScheduledJobs gives one page of the scheduled jobs, in the order of their arrival. The cursor is
// the number of jobs that earlier pages gave, and an empty cursor starts at the first job.
func (s *stacksFake) ScheduledJobs(ctx context.Context, cursor string, size int) (buildkite.Page, error) {
	if err := s.called(ctx, "list"); err != nil {
		return buildkite.Page{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lapse()

	skip := 0
	if cursor != "" {
		var err error
		if skip, err = strconv.Atoi(cursor); err != nil || skip < 0 {
			return buildkite.Page{}, fmt.Errorf("the cursor %q is not one that this fake gave", cursor)
		}
	}

	page := buildkite.Page{Paused: s.paused}
	seen := 0

	for _, id := range s.order {
		w := s.jobs[id]
		if w.state != stateScheduled {
			continue
		}

		seen++
		if seen <= skip {
			continue
		}

		if size > 0 && len(page.Jobs) == size {
			page.NextCursor = strconv.Itoa(skip + len(page.Jobs))

			break
		}

		page.Jobs = append(page.Jobs, buildkite.ScheduledJob{
			ID:          w.id,
			Rules:       slices.Clone(w.rules),
			ScheduledAt: w.scheduledAt,
			RunnableAt:  w.scheduledAt,
		})
	}

	return page, nil
}

// Reserve holds a scheduled job, and extends the reservation of a reserved one.
func (s *stacksFake) Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (
	buildkite.Reservation, error,
) {
	if err := s.called(ctx, "reserve"); err != nil {
		return buildkite.Reservation{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *stacksFake) Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (
	buildkite.Tokens, error,
) {
	if err := s.called(ctx, "tokens"); err != nil {
		return buildkite.Tokens{}, err
	}

	s.mu.Lock()

	var answer buildkite.Tokens

	for _, id := range ids {
		if w, known := s.jobs[id]; known && w.state == stateReserved {
			expires := s.now().Add(lifetime)
			s.expires[id] = expires
			answer.Issued = append(answer.Issued, buildkite.Token{
				JobID: id, Token: "bkaj_component", ExpiresAt: expires,
			})
		} else {
			answer.NotIssued = append(answer.NotIssued, id)
		}
	}

	after := s.afterTokens
	s.afterTokens = nil
	s.mu.Unlock()

	if after != nil {
		after()
	}

	return answer, nil
}

func (s *stacksFake) States(ctx context.Context, ids []job.ID) (map[job.ID]string, error) {
	if err := s.called(ctx, "states"); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lapse()

	states := map[job.ID]string{}

	if s.nameless {
		return states, nil
	}

	for _, id := range ids {
		if s.omit[id] > 0 {
			s.omit[id]--

			continue
		}

		if w, known := s.jobs[id]; known {
			states[id] = w.state
			s.reported[id] = w.state
		}
	}

	return states, nil
}

// Finish ends a job that no agent took. Buildkite refuses it for any other job.
func (s *stacksFake) Finish(ctx context.Context, id job.ID, _ int, _ string) error {
	if err := s.called(ctx, "finish"); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lapse()

	// I-5: the controller finishes no job after it learned that an agent took it.
	if s.reported[id] == stateRunning {
		s.violate("I-5: a finish of %s, whose last state showed an agent", id)
	}

	w, known := s.jobs[id]

	if known && s.takenAtFinish[id] {
		w.acquired, w.state = true, stateRunning
	}

	if !known || w.acquired || w.state != stateReserved {
		return buildkite.ErrTerminal
	}

	w.state = stateFinished
	s.finished = append(s.finished, id)

	return nil
}

func (s *stacksFake) Notify(ctx context.Context, lines []buildkite.Notification) error {
	if err := s.called(ctx, "notify"); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lines = append(s.lines, lines...)

	return nil
}
