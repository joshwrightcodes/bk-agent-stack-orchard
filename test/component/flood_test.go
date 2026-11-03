// SPDX-License-Identifier: AGPL-3.0-or-later

package component_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
)

// T-026. A pipeline author floods the head of the queue with more jobs that no profile carries
// than one read of the queue holds. The jobs stay in the list (ADR-0016). A job that a profile
// carries arrives behind them. The controller must still find it and start its machine, inside
// the request budget of the Stacks API.
func TestAFloodOfUnmatchedJobsDoesNotStarveTheFleet(t *testing.T) {
	s := newScenario(t)

	// 6500 jobs. The head of each read holds 2000 of them, and the sweep reaches the job behind
	// them at the fifth read.
	const flooded = 2*stack.MaxPages*stack.PageSize + stack.PageSize/2

	for range flooded {
		s.stacks.schedule(s.nextJob(), false)
	}

	waiting := s.arrive()

	s.run(10)

	if _, exists := s.machine(waiting); !exists {
		t.Errorf("the job behind %d unmatched jobs has no machine after 10 s, want the sweep "+
			"of the queue to find it", flooded)
	}

	s.budget()
}
