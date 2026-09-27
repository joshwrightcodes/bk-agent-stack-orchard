//go:build integration && tart

package integration_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// The failure tests are a preview of T-103 on real Tart: a job whose machine cannot start, or
// whose agent does not start, fails with a message that tells the pipeline author why, and leaves
// no machine. They need what the startup script test needs (tart_test.go).

// failingJob is the job of each failure test.
const failingJob = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a80"

// TestAnImageThatDoesNotExistEndsTheJob: a profile names an image digest that no registry has.
// The job fails with a message about the machine, and does not wait for ever.
func TestAnImageThatDoesNotExistEndsTheJob(t *testing.T) {
	requireTartImage(t)

	image := "ghcr.io/cirruslabs/macos-golden-gate-base@sha256:" + strings.Repeat("0", 64)
	finish := runFailingJob(t, image, 3*time.Minute, 5*time.Minute)

	if !strings.Contains(finish.Detail, "virtual machine of this job") {
		t.Errorf("the job ended with %q, want a message about its virtual machine", finish.Detail)
	}
}

// TestAnAgentThatExitsEndsTheJobWithItsLastLines: the agent in the machine exits at once, here
// because Buildkite refuses the token of the fake Stacks server. The machine fails, and the job
// page shows the lines of the agent, which say why.
func TestAnAgentThatExitsEndsTheJobWithItsLastLines(t *testing.T) {
	image := requireTartImage(t)

	finish := runFailingJob(t, image, 10*time.Minute, 5*time.Minute)

	if !strings.Contains(finish.Detail, "Invalid job acquisition token") {
		t.Errorf("the job ended with %q, want the line of the agent that says why", finish.Detail)
	}
}

// TestAnAgentThatNeverAcquiresEndsTheJobAfterTheAcquireTimeout: the agent in the machine runs,
// and cannot reach its endpoint, so it acquires nothing. After the acquire timeout the job fails
// with the last lines of the startup script.
func TestAnAgentThatNeverAcquiresEndsTheJobAfterTheAcquireTimeout(t *testing.T) {
	image := unreachableAgentImage(t, requireTartImage(t))

	finish := runFailingJob(t, image, 10*time.Minute, 90*time.Second)

	for _, want := range []string{"no agent acquired this job within 1m30s", "Registering agent"} {
		if !strings.Contains(finish.Detail, want) {
			t.Errorf("the job ended with %q, want %q in it", finish.Detail, want)
		}
	}
}

// requireTartImage gives the agent image, or skips the test without one.
func requireTartImage(t *testing.T) string {
	t.Helper()

	image := os.Getenv("BKASO_TART_IMAGE")
	if image == "" {
		t.Skip("BKASO_TART_IMAGE names no agent image; build one with `mise run agent-image`")
	}

	requireTart(t)

	return image
}

// runFailingJob runs one job of a profile for an image, and gives the finish that the controller
// sent for it. It checks that the job failed and that no machine remains.
func runFailingJob(t *testing.T, image string, boot, acquire time.Duration,
) stacksapi.FinishJobRequest {
	t.Helper()

	startRealOrchard(t, orchardBinary(t))

	stacks := fakestacks.New()
	stacks.StackKey = controllerID
	stacks.ScheduledJobs = stacksapi.ListScheduledJobsResponse{
		Jobs: []stacksapi.ScheduledJob{{
			ID:              failingJob,
			AgentQueryRules: []string{"queue=" + queue, "image=agent"},
			ScheduledAt:     time.Now(),
			RunnableAt:      time.Now(),
		}},
	}
	t.Cleanup(stacks.Close)

	process := startController(t, writeTartConfigWith(t, stacks.URL(), image, boot, acquire))
	name := "bk-" + controllerID + "-" + failingJob
	started := time.Now()

	within(t, boot+acquire+2*time.Minute, "the controller ends the job", func() bool {
		return finished(stacks.Finished(), failingJob)
	})

	var finish stacksapi.FinishJobRequest

	for _, call := range stacks.Finished() {
		if call.JobUUID == failingJob {
			finish = call
		}
	}

	t.Logf("after %s the job ended with %d: %s", time.Since(started).Round(time.Second),
		finish.ExitStatus, finish.Detail)

	if finish.ExitStatus != -1 {
		t.Errorf("the job ended with %d, want -1", finish.ExitStatus)
	}

	within(t, 2*time.Minute, "the controller deletes the machine", func() bool {
		_, ok := machineNamed(t, name)

		return !ok && !tartHolds(t, "orchard-"+name)
	})

	stopController(t, process)

	return finish
}

// unreachableAgentImage gives a copy of an agent image whose agent calls an address that Softnet
// blocks, so that the agent runs and acquires nothing. The copy goes when the test ends.
func unreachableAgentImage(t *testing.T, base string) string {
	t.Helper()

	name := base + "-unreachable"
	tart := func(args ...string) {
		t.Helper()

		if out, err := tartCommand(t.Context(), args...).CombinedOutput(); err != nil {
			t.Fatalf("tart %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	_ = tartCommand(t.Context(), "delete", name).Run()
	tart("clone", base, name)
	// The context of the test has ended when a cleanup runs.
	t.Cleanup(func() { _ = tartCommand(context.Background(), "delete", name).Run() })

	run := tartCommand(t.Context(), "run", "--no-graphics", name)
	if err := run.Start(); err != nil {
		t.Fatalf("starting %s: %v", name, err)
	}

	within(t, 3*time.Minute, "the guest agent of "+name+" answers", func() bool {
		return tartCommand(t.Context(), "exec", name, "true").Run() == nil
	})

	// 192.0.2.1 is a documentation address, which is not global, so Softnet drops each packet
	// to it and the agent waits and tries again. The copy is shut down from inside, because a
	// stop from outside can lose the last writes.
	tart("exec", name, "/bin/sh", "-c",
		`echo 'endpoint="https://192.0.2.1/v3"' >> /Users/admin/.buildkite-agent/buildkite-agent.cfg`+
			` && grep -q 192.0.2.1 /Users/admin/.buildkite-agent/buildkite-agent.cfg && sync`)

	// The shutdown can end the call to the guest before it answers, so its result says nothing.
	_ = tartCommand(t.Context(), "exec", name, "/bin/sh", "-c", "sudo /sbin/shutdown -h now").Run()

	done := make(chan struct{})

	go func() {
		_ = run.Wait()

		close(done)
	}()

	within(t, 3*time.Minute, name+" stops", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})

	return name
}

// tartCommand runs tart with the names of machines that a test chose.
func tartCommand(ctx context.Context, args ...string) *exec.Cmd {
	//nolint:gosec // G204: the arguments are tart commands and the names of test machines.
	return exec.CommandContext(ctx, "tart", args...)
}
