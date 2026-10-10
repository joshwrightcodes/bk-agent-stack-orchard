//go:build integration && tart

package integration_test

import (
	"testing"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// TestAControllerCrashKeepsTheRealMachineOfARunningJob (a preview of T-104 on real Tart): the
// controller crashes while the agent of a job runs in a real machine. The machine continues while
// no controller runs, the next process takes it over without a second machine or a second
// reservation, and it deletes the machine when the job ends.
//
// The agent image here reaches no endpoint, so its agent runs and acquires nothing, and the test
// gives the job the state that an agent gives it. An Orchard controller restart has no preview
// here: orchard dev runs its worker in its own process, so a restart of it also ends the machines.
func TestAControllerCrashKeepsTheRealMachineOfARunningJob(t *testing.T) {
	image := unreachableAgentImage(t, requireTartImage(t))

	startRealOrchard(t, orchardBinary(t))

	const id = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a90"

	stacks := fakestacks.New()
	stacks.StackKey = controllerID
	stacks.ScheduledJobs = stacksapi.ListScheduledJobsResponse{
		Jobs: []stacksapi.ScheduledJob{{
			ID:              id,
			AgentQueryRules: []string{"queue=" + queue, "image=agent"},
			ScheduledAt:     time.Now(),
			RunnableAt:      time.Now(),
		}},
	}
	t.Cleanup(stacks.Close)

	// The job is running before the acquire timeout ends, and from then on the timeout does not
	// apply. unplaced, boot, and acquire must not add to more than the token lifetime of 30 min.
	config := writeTartConfigWith(t, stacks.URL(), image, 10*time.Minute, 10*time.Minute)
	first := startController(t, config)
	name := machineOf(id)

	within(t, tartMachineWait, "the machine of the job runs", func() bool {
		requireAlive(t, first)

		return running(t, id)
	})

	// An agent acquired the job and runs it. From here the machine belongs to the agent until the
	// job ends (target design, section 7, row 8).
	stacks.SetJobState(id, "running")

	before, _ := machineNamed(t, name)

	crashController(t, first)
	crashed := time.Now()

	// The machine continues while no controller runs.
	throughout(t, 30*time.Second, "the machine runs while no controller runs", func() bool {
		found, ok := machineNamed(t, name)

		return ok && found.UID == before.UID && found.Status == "running" &&
			tartHolds(t, "orchard-"+name)
	})

	restarted := time.Now()
	second := startController(t, config)

	// The next process takes the machine over: the same machine, and no second one, and it does
	// not reserve the job again, because an agent runs it.
	throughout(t, 30*time.Second, "the next process keeps the one machine of the job", func() bool {
		requireAlive(t, second)

		found, ok := machineNamed(t, name)
		owned := ownedMachines(t)
		held := reservations(stacks.Reserved(), id)

		if !ok || found.UID != before.UID || len(owned) != 1 || held != 1 {
			t.Logf("the machine %+v (was %s), the machines %v, the reservations of the job %d, "+
				"the finishes %+v", found, before.UID, owned, held, stacks.Finished())

			return false
		}

		return true
	})

	stacks.SetJobState(id, "finished")

	within(t, 2*time.Minute, "the next process deletes the machine of the finished job", func() bool {
		_, ok := machineNamed(t, name)

		return !ok && !tartHolds(t, "orchard-"+name)
	})

	// T-104 allows 15 minutes for the recovery.
	if took := time.Since(restarted); took > 15*time.Minute {
		t.Errorf("the recovery took %s, want less than 15 minutes", took)
	}

	t.Logf("the next process started %s after the crash, and removed the machine %s after it "+
		"started", restarted.Sub(crashed).Round(time.Second), time.Since(restarted).Round(time.Second))

	stopController(t, second)
}

// throughout checks a condition for some time, and fails the test when it does not hold once.
func throughout(t *testing.T, limit time.Duration, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(limit)

	for time.Now().Before(deadline) {
		if !condition() {
			t.Fatalf("it stopped: %s", what)
		}

		time.Sleep(time.Second)
	}
}

// requireAlive stops the test at once when a controller has exited, for example because it
// refused its configuration, instead of a wait for a machine that it cannot start.
func requireAlive(t *testing.T, running *controller) {
	t.Helper()

	select {
	case <-running.done:
		t.Fatalf("the controller exited (%v):\n%s", running.err, tail(running.log))
	default:
	}
}
