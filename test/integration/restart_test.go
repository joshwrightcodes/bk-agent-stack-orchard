//go:build integration

package integration_test

import (
	"slices"
	"testing"
	"time"
)

// T-021. The controller crashes, and a new process takes over the machines of the last one at
// each crash window of the target design, section 7.4. Each job keeps at most one machine, the
// machine of a job that ended goes, and every reservation is extended at once, because the new
// process does not know when any of them ends.
func TestARestartAtEachCrashWindowKeepsOneMachinePerJob(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	const (
		// kept has a machine when the process crashes, and its job is still reserved.
		kept = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01"

		// ended has a machine when the process crashes, and its job ends while no process runs.
		ended = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02"

		// unseen is reserved, and the process crashed before it created the machine.
		unseen = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a03"
	)

	stacks := stacksWith(t,
		scheduled(kept, "xcode=27", 2*time.Minute),
		scheduled(ended, "xcode=27", time.Minute),
	)
	stacks.SetJobState(unseen, "reserved")

	config := writeConfig(t, stacks.URL())
	first := startController(t, config)

	eventually(t, "both jobs have running machines", func() bool {
		return running(t, kept) && running(t, ended)
	})

	before, _ := machineNamed(t, machineOf(kept))

	crashController(t, first)

	// The agent of one job finishes it while no controller runs.
	stacks.SetJobState(ended, "running")
	stacks.SetJobState(ended, "finished")

	restarted := time.Now()
	second := startController(t, config)

	// Between the finish and the delete: the job ended, and the new process removes its machine.
	eventually(t, "the machine of the job that ended is gone", func() bool {
		_, found := machineNamed(t, machineOf(ended))

		return !found
	})

	// After the machine creation: the new process finds the machine by its name. It does not
	// know when the reservation ends, so its first pass extends it. A renewal by the clock would
	// come only when less than two minutes of the five are left.
	eventually(t, "the reservation of the kept job is extended", func() bool {
		return reservations(stacks.Reserved(), kept) == 2
	})

	if took := time.Since(restarted); took > 15*time.Second {
		t.Errorf("the reservation was extended %s after the restart, want it at the first pass",
			took)
	}

	// The same machine, not a second one: Orchard gives a machine a new UID when it is made
	// again.
	after, found := machineNamed(t, machineOf(kept))
	if !found || after.UID != before.UID {
		t.Errorf("the kept job has the machine %+v after the restart, want %+v", after, before)
	}

	// After the reservation and before the machine creation: no machine names the job, so the new
	// process cannot see it, and it starts nothing for it.
	never(t, "the job reserved before the crash gets a machine", func() bool {
		_, found := machineNamed(t, machineOf(unseen))

		return found
	})

	// Its reservation expires, and Buildkite schedules the job again. Admission then starts it.
	stacks.Schedule(scheduled(unseen, "xcode=27", 3*time.Minute))

	eventually(t, "the job whose reservation expired starts", func() bool {
		return running(t, unseen)
	})

	for _, id := range []string{kept, unseen} {
		stacks.SetJobState(id, "running")
		stacks.SetJobState(id, "finished")
	}

	eventually(t, "no machine of this controller remains", func() bool {
		return len(ownedMachines(t)) == 0
	})

	stopController(t, second)
}

// crashController ends the controller the way a crash does: no signal handler runs, and the stack
// is not withdrawn.
func crashController(t *testing.T, running *controller) {
	t.Helper()

	if err := running.command.Process.Kill(); err != nil {
		t.Fatalf("killing the controller: %v", err)
	}

	select {
	case <-running.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the controller did not stop inside 10s of SIGKILL")
	}
}

// reservations counts the reserve calls that named a job.
func reservations(reserved []string, id string) int {
	return len(slices.DeleteFunc(slices.Clone(reserved), func(held string) bool { return held != id }))
}
