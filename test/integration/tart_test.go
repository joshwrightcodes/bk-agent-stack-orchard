//go:build integration && tart

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// The Tart test runs one job on a real virtual machine of this Mac, through the controller, a
// real Orchard worker, and the Tart guest agent. It needs an Apple silicon Mac with Tart 2.39.0 or
// later, Softnet with the setuid bit, and an agent image (images/agent). Run it with:
//
//	BKASO_TART_IMAGE=bkaso-agent go test -tags integration,tart -count=1 -timeout 30m \
//	    -run TestARealMachineRunsTheStartupScript ./test/integration/
//
// The fake Stacks server issues a token that Buildkite does not know, so the agent in the machine
// cannot register. That refusal is the evidence: only the startup script of the controller, run
// in the guest, calls Buildkite with that token, and it reaches Buildkite through Softnet. The job
// then ends by the path of a failed machine.

// tartMachineWait is the limit for each phase of a real machine: the clone of a 50 GB image, the
// boot of macOS, and the agent.
const tartMachineWait = 10 * time.Minute

// TestARealMachineRunsTheStartupScript: the Tart guest agent runs the startup script of the
// controller in a real machine, with the environment of the job (open item O-2), the token of the
// job stays out of the log, and the controller ends the job and deletes the machine.
func TestARealMachineRunsTheStartupScript(t *testing.T) {
	image := os.Getenv("BKASO_TART_IMAGE")
	if image == "" {
		t.Skip("BKASO_TART_IMAGE names no agent image; build one with `mise run agent-image`")
	}

	requireTart(t)
	startRealOrchard(t, orchardBinary(t))

	const id = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7e"

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

	process := startController(t, writeTartConfig(t, stacks.URL(), image))
	name := "bk-" + controllerID + "-" + id

	within(t, time.Minute, "the controller reserves the job", func() bool {
		return slices.Contains(stacks.Reserved(), id)
	})

	within(t, tartMachineWait, "the machine of the job runs on the worker of this Mac", func() bool {
		found, ok := machineNamed(t, name)

		return ok && found.Status == "running"
	})

	// The log of the startup script lives only as long as the machine, so the test keeps each
	// line while the machine exists.
	var log []string

	within(t, tartMachineWait, "the controller ends the job and deletes the machine", func() bool {
		log = merge(log, scriptLog(name))
		_, ok := machineNamed(t, name)

		return !ok && finished(stacks.Finished(), id)
	})

	text := strings.Join(log, "\n")
	t.Logf("the log of the startup script:\n%s", text)

	// buildkite-agent wrote this log in the guest, so the guest agent ran the script. The agent
	// named the machine from BUILDKITE_AGENT_NAME, which only the environment of the script gives.
	if !strings.Contains(text, "buildkite-agent") && !strings.Contains(text, name) {
		t.Errorf("the log has no line of buildkite-agent, so the script did not run in the guest")
	}

	// Buildkite answered the agent, so the machine reached the Internet through Softnet, and the
	// agent used the job token, which Buildkite does not know.
	if !strings.Contains(strings.ToLower(text), "401") &&
		!strings.Contains(strings.ToLower(text), "unauthorized") {
		t.Errorf("the log shows no refusal from Buildkite, so the agent did not reach it")
	}

	if strings.Contains(text, "bkjat_"+id) {
		t.Errorf("the log of the machine carries the job token")
	}

	for _, call := range stacks.Finished() {
		if call.JobUUID == id && call.ExitStatus != -1 {
			t.Errorf("the controller finished the job with %d, want -1", call.ExitStatus)
		}
	}

	if owned := ownedMachines(t); len(owned) != 0 {
		t.Errorf("orchard still holds the machines %v of this controller", owned)
	}

	// Orchard removes a machine from its API before the worker deletes it from Tart, so the test
	// waits for the worker. Otherwise the end of the test stops the worker first, and the machine
	// stays on the disk of this Mac.
	within(t, 2*time.Minute, "the worker deletes the machine from Tart", func() bool {
		return !tartHolds(t, "orchard-"+name)
	})

	stopController(t, process)
}

// tartHolds reports a local Tart machine whose name starts with a prefix.
func tartHolds(t *testing.T, prefix string) bool {
	t.Helper()

	// A cleanup calls this too, after the context of the test has ended.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// tart list can fail while the worker deletes a machine, so a failed list counts as a machine
	// that is still there, and the wait goes on.
	out, err := exec.CommandContext(ctx, "tart", "list", "--source", "local",
		"--format", "json").Output()
	if err != nil {
		t.Logf("listing the machines of Tart: %v", err)

		return true
	}

	var local []tartMachine

	if err := json.Unmarshal(out, &local); err != nil {
		t.Fatalf("reading the machines of Tart: %v", err)
	}

	return slices.ContainsFunc(local, func(vm tartMachine) bool {
		return strings.HasPrefix(vm.Name, prefix)
	})
}

// tartMachine is one machine of `tart list`.
type tartMachine struct {
	Name string `json:"Name"`
}

// requireTart stops the test when this Mac cannot run the machine. Tart before 2.39.0 cannot list
// the machines while one with an ASIF disk runs, and the worker then fails.
func requireTart(t *testing.T) {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "tart", "--version").Output()
	if err != nil {
		t.Fatalf("tart is not on PATH: %v", err)
	}

	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d.%d", &major, &minor); err != nil ||
		major < 2 || (major == 2 && minor < 39) {
		t.Fatalf("tart is %s, and the worker needs 2.39.0 or later", strings.TrimSpace(string(out)))
	}

	path, err := exec.LookPath("softnet")
	if err != nil {
		t.Fatal("softnet is not on PATH, and every machine of the controller uses it")
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSetuid == 0 {
		t.Fatalf("%s has no setuid bit, so Tart would ask for a password", path)
	}
}

// startRealOrchard runs `orchard dev` with one worker that runs real machines with Tart.
func startRealOrchard(t *testing.T, binary string) {
	t.Helper()

	if answered(orchardURL + "/v1/workers") {
		t.Fatalf("something already answers at %s; stop it before this test runs", orchardURL)
	}

	// The worker runs Tart with the home of Orchard, which is a temp dir of this test, so Tart
	// needs the home of its own machines to find the image.
	tartHome := os.Getenv("TART_HOME")
	if tartHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("finding the home of Tart: %v", err)
		}

		tartHome = filepath.Join(home, ".tart")
	}

	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "orchard.log")

	//nolint:gosec // G204: the binary is the Orchard that mise pins, or the one that
	// ORCHARD_BIN names, and every argument is a temp dir of this test.
	command := exec.CommandContext(t.Context(), binary, "dev", "--data-dir",
		filepath.Join(home, "data"))
	graceful(command)
	command.Env = environment(map[string]string{"HOME": home, "TART_HOME": tartHome})
	command.Stdout, command.Stderr = output(t, log), output(t, log)

	if err := command.Start(); err != nil {
		t.Fatalf("starting orchard dev: %v", err)
	}

	t.Cleanup(func() { stop(t, command, "orchard dev", log) })

	var workers []struct {
		Runtime string `json:"runtime"`
	}

	within(t, time.Minute, "the worker of orchard dev reports", func() bool {
		return decode(orchardURL+"/v1/workers", &workers) == nil && len(workers) == 1 &&
			workers[0].Runtime == "tart"
	})
}

// writeTartConfig writes a configuration with one profile for the agent image.
func writeTartConfig(t *testing.T, endpoint, image string) string {
	t.Helper()

	return writeTartConfigWith(t, endpoint, image, 10*time.Minute, 5*time.Minute)
}

// writeTartConfigWith writes a configuration with one profile for an image, and its boot and
// acquire timeouts.
func writeTartConfigWith(t *testing.T, endpoint, image string, boot, acquire time.Duration,
) string {
	t.Helper()

	body := fmt.Sprintf(`controllerID: %s
healthAddress: %s
buildkite:
  queue: %s
  endpoint: %s
orchard:
  url: %s
  serviceAccount: bk-agent-stack-orchard
profiles:
  - name: agent
    match: {image: agent}
    image: %s
    cpu: 4
    memoryMiB: 8192
    diskSizeGB: 50
    timeouts:
      boot: %s
      acquire: %s
`, controllerID, freeAddress(t), queue, endpoint, orchardURL, image, boot, acquire)

	path := filepath.Join(t.TempDir(), "config.yaml")

	//nolint:gosec // G703: the path is a temp dir of this test; only the body holds the image name.
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	return path
}

// scriptLog gives the lines of the startup script that Orchard holds for a machine.
func scriptLog(name string) []string {
	var events []struct {
		Kind    string `json:"kind"`
		Payload string `json:"payload"`
	}

	if err := decode(orchardURL+"/v1/vms/"+name+"/events", &events); err != nil {
		return nil
	}

	var lines []string

	for _, event := range events {
		if event.Kind == "log_line" {
			lines = append(lines, event.Payload)
		}
	}

	return lines
}

// merge keeps the longer of two reads of the same log, because Orchard only adds lines.
func merge(kept, read []string) []string {
	if len(read) > len(kept) {
		return read
	}

	return kept
}

// within waits for a condition for a time, and fails the test with what it waited for.
func within(t *testing.T, limit time.Duration, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(limit)

	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(time.Second)
	}

	t.Fatalf("waited %s, and still not: %s", limit, what)
}
