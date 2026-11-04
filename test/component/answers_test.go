// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package component_test

import (
	"slices"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// T-027. The job states answer, and the answer names no job, or it leaves some jobs out. Neither
// answer deletes the machine of a running build (I-9). An answer that names no job is a failed
// refresh, so the job states grow stale. A job that one answer leaves out keeps its machine, and a
// job that Buildkite no longer knows loses its machine once two answers in a row leave it out.
func TestAnEmptyOrPartialStatesAnswerDeletesNoMachineOfARunningBuild(t *testing.T) {
	s := newScenario(t)

	kept, forgotten := s.arrive(), s.arrive()
	s.run(5)
	s.acquire(kept)
	s.acquire(forgotten)
	s.run(10)

	if !s.ready() {
		t.Fatalf("the controller is not ready before the empty answers: %v", s.checks.Unready())
	}

	// Every answer names no job, for longer than the job states stay fresh.
	s.stacks.nameless = true
	s.run(90)

	for _, id := range []job.ID{kept, forgotten} {
		if _, exists := s.machine(id); !exists {
			t.Errorf("the machine of the running job %s is gone after answers that name no job", id)
		}
	}

	if s.ready() {
		t.Error("the controller is ready while every answer of the job states names no job, " +
			"want the job states stale")
	}

	// One answer leaves the job out, and names the other one.
	s.stacks.nameless = false
	s.run(10)
	s.stacks.omit[kept] = 1
	s.run(20)

	if _, exists := s.machine(kept); !exists {
		t.Error("the machine of the running job is gone after one answer left the job out")
	}

	if !s.ready() {
		t.Errorf("the controller is not ready after the answers name the jobs again: %v",
			s.checks.Unready())
	}

	// Buildkite forgets a job. Two answers in a row leave it out, and its machine goes (row 5).
	s.stacks.forget(forgotten)
	s.run(15)

	if _, exists := s.machine(forgotten); exists {
		t.Error("the machine of a job that Buildkite no longer knows remains, want row 5 to " +
			"delete it")
	}

	if _, exists := s.machine(kept); !exists {
		t.Error("the machine of the running job is gone, want it kept")
	}

	s.budget()
}

// T-028. An agent acquires a job after the state that a finish read, and just before the finish
// arrives, at the end of the acquire timeout. Buildkite refuses the finish. The refusal does not
// delete the machine of the running build (I-9): the next state shows the agent, and the machine
// stays until the job ends.
func TestAFinishThatMeetsAnAgentDeletesNoMachine(t *testing.T) {
	s := newScenario(t)

	late := s.arrive()
	s.run(5)

	vm, exists := s.machine(late)
	if !exists || vm.Status != orchard.StatusRunning {
		t.Fatal("the job has no running machine")
	}

	s.stacks.takenAtFinish[late] = true
	s.run(int(s.settings.Defaults.Timeouts.Acquire.Seconds()) + 30)

	if w := s.stacks.jobs[late]; !w.acquired || w.state != stateRunning {
		t.Fatalf("the job is %s, acquired %t, want the agent to run it", w.state, w.acquired)
	}

	if _, exists := s.machine(late); !exists {
		t.Fatal("the machine of the running job is gone after a refused finish")
	}

	s.stacks.jobs[late].state = stateFinished
	s.run(10)

	if _, exists := s.machine(late); exists {
		t.Error("the machine of the job that ended remains, want row 4 to delete it")
	}

	s.budget()
}

// T-029. The clock of Orchard runs ten minutes behind the clock of the controller, or ten minutes
// ahead. The timeouts count on the clock of the controller alone: a machine whose agent is slow
// keeps its job inside the acquire timeout, and the job fails when the timeout ends, whatever the
// clock of Orchard says.
func TestTheTimeoutsIgnoreTheClockOfOrchard(t *testing.T) {
	for name, skew := range map[string]time.Duration{
		"behind": -10 * time.Minute,
		"ahead":  10 * time.Minute,
	} {
		t.Run(name, func(t *testing.T) {
			s := newScenario(t)
			s.orchard.skew = skew

			slow := s.arrive()
			s.run(5)

			if vm, exists := s.machine(slow); !exists || vm.Status != orchard.StatusRunning {
				t.Fatal("the job has no running machine")
			}

			acquire := s.settings.Defaults.Timeouts.Acquire

			// Inside the acquire timeout, the job keeps its machine and its reservation.
			s.run(int((acquire - 30*time.Second).Seconds()))

			if w := s.stacks.jobs[slow]; w.state != stateReserved {
				t.Fatalf("the job is %s inside its acquire timeout, want it reserved", w.state)
			}

			if _, exists := s.machine(slow); !exists {
				t.Fatal("the machine is gone inside its acquire timeout")
			}

			// The timeout ends, and the job fails with the reason.
			s.run(45)

			if !slices.Contains(s.stacks.finished, slow) {
				t.Error("the job did not fail at the end of its acquire timeout")
			}

			if _, exists := s.machine(slow); exists {
				t.Error("the machine remains after the acquire timeout")
			}
		})
	}
}

// T-030. A slow tick puts two minutes between the token call of a job and the first listing of
// its machine, and no worker has room for the machine. The unplaced timeout counts from the token
// call, so row 10 ends the wait two minutes after the token call, and not two minutes after the
// first listing. The token then still outlives the boot and acquire timeouts that a placed machine
// would get, so a healthy machine cannot meet an expired token (target design, section 7.3).
func TestTheUnplacedTimeoutCountsFromTheTokenCall(t *testing.T) {
	s := newScenario(t)

	var sent time.Time

	// The token call answers, and the rest of the tick takes two minutes: the creates and the
	// notices at their time limits, and a poll of the next tick before its inventory.
	s.stacks.afterTokens = func() {
		sent = s.now
		s.now = s.now.Add(2 * time.Minute)
	}

	waiting := s.arrive()

	for range 10 {
		if !sent.IsZero() {
			break
		}

		s.run(1)
	}

	// Every worker stops taking machines after the tick that created the machine, so it waits
	// unplaced. The fake places a machine at the start of the next tick, after the room check of
	// the tick that asked for it.
	for _, h := range s.orchard.hosts {
		h.worker.SchedulingPaused = true
	}

	vm, exists := s.machine(waiting)
	if sent.IsZero() || !exists || vm.Worker != "" {
		t.Fatal("the job did not get an unplaced machine after its token call")
	}

	timeouts := s.settings.Defaults.Timeouts

	var ended time.Time

	for range int(2 * timeouts.Unplaced.Seconds()) {
		s.run(1)

		if _, exists := s.machine(waiting); !exists {
			ended = s.now

			break
		}
	}

	if ended.IsZero() {
		t.Fatalf("the unplaced machine remains %s after the token call", s.now.Sub(sent))
	}

	if late := ended.Sub(sent); late > timeouts.Unplaced+2*time.Second {
		t.Errorf("row 10 ended the wait %s after the token call, want about %s", late,
			timeouts.Unplaced)
	}

	if left := s.stacks.expires[waiting].Sub(ended); left < timeouts.Boot+timeouts.Acquire {
		t.Errorf("the token had %s left when the wait ended, want at least boot and acquire, %s",
			left, timeouts.Boot+timeouts.Acquire)
	}

	if slices.Contains(s.stacks.finished, waiting) {
		t.Error("the controller finished the job, want it back in the queue")
	}
}
