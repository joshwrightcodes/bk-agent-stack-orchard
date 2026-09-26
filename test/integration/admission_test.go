//go:build integration

package integration_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// T-061. More matched jobs than free slots (QS-1). The synthetic worker has room for two machines
// of the profile, so two of three jobs start, the third stays scheduled, and it starts when a
// slot is free.
func TestAFullFleetHoldsAJobBackUntilASlotIsFree(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	ids := []string{
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01",
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02",
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a03",
	}

	// The jobs are scheduled a minute apart, so admission takes the two oldest first.
	var jobs []stacksapi.ScheduledJob
	for i, id := range ids {
		jobs = append(jobs, scheduled(id, "xcode=27", time.Duration(len(ids)-i)*time.Minute))
	}

	stacks := stacksWith(t, jobs...)
	process := startController(t, writeConfig(t, stacks.URL()))

	eventually(t, "the two oldest jobs have running machines", func() bool {
		return running(t, ids[0]) && running(t, ids[1])
	})

	// Several ticks pass, and the fleet stays full: the third job is neither reserved nor given a
	// machine, so Buildkite still shows it as scheduled.
	never(t, "the third job is reserved or gets a machine while the fleet is full", func() bool {
		_, found := machineNamed(t, machineOf(ids[2]))

		return found || slices.Contains(stacks.Reserved(), ids[2])
	})

	// The first job ends, and the controller deletes its machine, which frees a slot.
	stacks.SetJobState(ids[0], "running")
	stacks.SetJobState(ids[0], "finished")

	eventually(t, "the machine of the finished job is gone", func() bool {
		_, found := machineNamed(t, machineOf(ids[0]))

		return !found
	})

	eventually(t, "the third job starts in the free slot", func() bool {
		return running(t, ids[2])
	})

	for _, id := range ids[1:] {
		stacks.SetJobState(id, "running")
		stacks.SetJobState(id, "finished")
	}

	eventually(t, "no machine of this controller remains", func() bool {
		return len(ownedMachines(t)) == 0
	})

	// Each job was reserved once: a second reservation would be a second agent for one job.
	if held := stacks.Reserved(); len(held) != len(ids) {
		t.Errorf("the controller reserved %v, want each of the %d jobs once", held, len(ids))
	}

	stopController(t, process)
}

// T-062. A job whose tags match no profile fails at the next poll with a message that names the
// profiles, and no machine starts for it (QS-3).
func TestAJobThatNoProfileCarriesEndsAtOnce(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	stacks := stacksWith(t, scheduled(carried, "xcode=99", time.Minute))

	started := time.Now()
	process := startController(t, writeConfig(t, stacks.URL()))

	var finish stacksapi.FinishJobRequest

	eventually(t, "the controller finishes the job", func() bool {
		for _, call := range stacks.Finished() {
			if call.JobUUID == carried {
				finish = call

				return true
			}
		}

		return false
	})

	// The poll runs each second. The limit leaves room for the start of the process and a slow
	// runner, and it is still far inside the scheduled-job expiry that QS-3 measures against.
	if took := time.Since(started); took > 15*time.Second {
		t.Errorf("the job ended %s after the controller started, want it at the next poll", took)
	}

	if finish.ExitStatus != -1 {
		t.Errorf("the job ended with the exit status %d, want -1", finish.ExitStatus)
	}

	// The author of the pipeline reads this on the job page, so it names what the fleet offers.
	if !strings.Contains(finish.Detail, "xcode-27") {
		t.Errorf("the job page reads %q, want it to name the profile xcode-27", finish.Detail)
	}

	never(t, "a machine starts for the job", func() bool {
		_, found := machineNamed(t, machineOf(carried))

		return found
	})

	if calls := len(stacks.Finished()); calls != 1 {
		t.Errorf("the controller finished the job %d times, want once", calls)
	}

	stopController(t, process)
}

// stacksWith gives a fake Stacks server for this controller with the jobs scheduled.
func stacksWith(t *testing.T, jobs ...stacksapi.ScheduledJob) *fakestacks.Server {
	t.Helper()

	stacks := fakestacks.New()
	stacks.StackKey = controllerID
	stacks.ScheduledJobs = stacksapi.ListScheduledJobsResponse{Jobs: jobs}
	t.Cleanup(stacks.Close)

	return stacks
}

// scheduled gives one job of the queue with one more tag, scheduled some time ago.
func scheduled(id, tag string, ago time.Duration) stacksapi.ScheduledJob {
	at := time.Now().Add(-ago)

	return stacksapi.ScheduledJob{
		ID:              id,
		AgentQueryRules: []string{"queue=" + queue, tag},
		ScheduledAt:     at,
		RunnableAt:      at,
	}
}

func machineOf(id string) string {
	return "bk-" + controllerID + "-" + id
}

func running(t *testing.T, id string) bool {
	t.Helper()

	found, ok := machineNamed(t, machineOf(id))

	return ok && found.Status == "running"
}

// never checks a condition for some ticks of the controller, and fails the test if it holds once.
func never(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if condition() {
			t.Fatalf("it happened: %s", what)
		}

		time.Sleep(250 * time.Millisecond)
	}
}
