//go:build integration

package integration_test

import (
	"slices"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"
)

// T-022. An operator pauses the queue. The controller starts no machine and reserves or finishes
// no job, and the machines in progress continue (target design, section 7.5).
func TestAPausedQueueStartsNothingAndLetsMachinesContinue(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	const (
		// busy has a machine when the queue pauses.
		busy = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01"

		// held matches a profile and arrives while the queue is paused.
		held = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02"

		// stray matches no profile and arrives while the queue is paused.
		stray = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a03"
	)

	stacks := stacksWith(t, scheduled(busy, "xcode=27", time.Minute))
	process := startController(t, writeConfig(t, stacks.URL()))

	eventually(t, "the first job has a running machine", func() bool {
		return running(t, busy)
	})

	stacks.Pause(true)
	stacks.SetJobState(busy, "running")
	stacks.Schedule(scheduled(held, "xcode=27", time.Minute), scheduled(stray, "xcode=99", time.Minute))

	never(t, "the controller acts on a job while the queue is paused", func() bool {
		_, found := machineNamed(t, machineOf(held))

		return found || slices.Contains(stacks.Reserved(), held) ||
			slices.Contains(stacks.Reserved(), stray) || finished(stacks.Finished(), stray)
	})

	// The machine in progress continues: the pause is not a reason to end it.
	if !running(t, busy) {
		t.Error("the machine of the running job stopped while the queue was paused")
	}

	// The reaper runs while the queue is paused, so a job that ends still loses its machine.
	stacks.SetJobState(busy, "finished")

	eventually(t, "the machine of the finished job is gone while the queue is paused", func() bool {
		_, found := machineNamed(t, machineOf(busy))

		return !found
	})

	// The operator resumes the queue, and the controller catches up.
	stacks.Pause(false)

	eventually(t, "the waiting job starts and the unmatched job ends", func() bool {
		return running(t, held) && finished(stacks.Finished(), stray)
	})

	stacks.SetJobState(held, "running")
	stacks.SetJobState(held, "finished")

	eventually(t, "no machine of this controller remains", func() bool {
		return len(ownedMachines(t)) == 0
	})

	stopController(t, process)
}

// T-023. A job is cancelled while its machine boots. The controller deletes the machine, so no
// agent can acquire the job, and the cancellation stays the result of the job.
func TestACancellationWhileTheMachineBootsDeletesIt(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	stacks := stacksWith(t, scheduled(carried, "xcode=27", time.Minute))
	process := startController(t, writeConfig(t, stacks.URL()))

	// A synthetic machine runs at once and no agent starts in it, so the job stays reserved, as
	// it does while a real machine boots.
	eventually(t, "the job has a machine", func() bool {
		return running(t, carried)
	})

	stacks.SetJobState(carried, "canceled")

	eventually(t, "the machine of the cancelled job is gone", func() bool {
		_, found := machineNamed(t, machineOf(carried))

		return !found
	})

	// The controller ends nothing that Buildkite already ended, and it does not take the job
	// again: a second reservation would give a cancelled job an agent.
	never(t, "the controller finishes or reserves the cancelled job again", func() bool {
		return finished(stacks.Finished(), carried) || reservations(stacks.Reserved(), carried) > 1
	})

	stopController(t, process)
}

// finished reports a job that the controller finished.
func finished(calls []stacksapi.FinishJobRequest, id string) bool {
	return slices.ContainsFunc(calls, func(call stacksapi.FinishJobRequest) bool {
		return call.JobUUID == id
	})
}
