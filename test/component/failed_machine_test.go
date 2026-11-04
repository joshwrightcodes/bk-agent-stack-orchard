// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package component_test

import (
	"testing"
)

// T-020, the accounting of the scheduler. Orchard 0.58.0 keeps the worker of a failed machine and
// sets its scheduled condition to false, and the resources of the machine are free from then on.
// The controller must count the same way, or a failed machine that waits for deletion keeps a
// full fleet closed to a waiting job.
func TestAFailedMachineThatWaitsForDeletionFreesItsSlotForAWaitingJob(t *testing.T) {
	// The in-flight limit is above the four slots, so only the slots can close the fleet. The
	// limit counts every owned machine, failed ones too, until the Reaper deletes them.
	f := newFleetWith(t, t, quiet(t), 8)
	s := &scenario{fleet: f, t: t, checks: f.readiness(t)}

	// Two workers with two slots each: four jobs fill the fleet, and the fifth has to wait.
	var running []string

	for range 4 {
		id := s.arrive()
		s.run(3)
		s.acquire(id)
		running = append(running, string(id))
	}

	s.run(5)

	waiting := s.arrive()
	s.run(5)

	if _, exists := s.machine(waiting); exists {
		t.Fatal("the fifth job has a machine, want it to wait for a free slot")
	}

	// The delete fails, so the failed machine stays in the listing and the controller keeps seeing
	// it, with its worker and a scheduled condition that is false.
	s.orchard.deleteFails = errUnavailable

	for _, vm := range s.orchard.vms {
		if vm.Status == "running" {
			fail(vm, "the machine failed")

			break
		}
	}

	s.run(40)

	if _, exists := s.machine(waiting); !exists {
		t.Errorf("the waiting job has no machine 40 s after a machine of %v failed, "+
			"want the slot that the failure freed to be counted", running)
	}

	for name, vm := range s.orchard.vms {
		if vm.Status == "failed" && len(vm.Conditions) == 0 {
			t.Errorf("the failed machine %s has no scheduled condition", name)
		}
	}
}
