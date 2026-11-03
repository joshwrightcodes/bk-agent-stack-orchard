// SPDX-License-Identifier: AGPL-3.0-or-later

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
