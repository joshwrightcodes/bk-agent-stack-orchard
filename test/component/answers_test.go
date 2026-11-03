// SPDX-License-Identifier: AGPL-3.0-or-later

package component_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
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
