//go:build integration

// Package integration runs the controller as a process, against a real Orchard in synthetic
// mode and the fake Stacks server, with no Mac and no Tart.
//
// The unit tests of each package prove a part against fakes that this project wrote. This test
// proves the parts together, through the binary that the release ships, against the Orchard
// scheduler itself: a fake of Orchard would agree with whatever this project believes about
// Orchard, and the real one does not have to.
//
// Run it with the orchard binary on PATH or in ORCHARD_BIN:
//
//	go test -tags integration -count=1 ./test/integration/...
package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

const (
	controllerID = "skeleton"
	queue        = "macos-skeleton"
	carried      = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"

	// orchardURL is where `orchard dev` listens. The port is fixed in Orchard, so two runs of
	// this test cannot share a machine.
	orchardURL = "http://127.0.0.1:6120"

	// resources pins what the synthetic worker advertises. Without it the worker advertises the
	// cores and memory of the machine that runs the test, and a small CI runner cannot carry a
	// machine of the profile below.
	resources = "org.cirruslabs.tart-vms=2,org.cirruslabs.logical-cores=16," +
		"org.cirruslabs.memory-mib=65536"
)

// controllerBinary is the binary that the release ships, built once for every test.
var controllerBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bk-agent-stack-orchard-integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	controllerBinary = filepath.Join(dir, "bk-agent-stack-orchard")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	// The race detector makes a data race in the running process a failure of this test: the
	// process then exits with a status that is not zero.
	//nolint:gosec // G204: the test builds the binary of this module into its own temp dir.
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", controllerBinary,
		"github.com/joshwrightcodes/bk-agent-stack-orchard/cmd/bk-agent-stack-orchard")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr

	err = build.Run()

	cancel()

	if err != nil {
		fmt.Fprintf(os.Stderr, "building the controller: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// T-060. One job goes from scheduled to a deleted machine, and no machine of this controller
// remains at the end (invariant I-6).
func TestOneJobGoesFromScheduledToADeletedMachine(t *testing.T) {
	startOrchard(t, orchard(t))

	stacks := fakestacks.New()
	stacks.StackKey = controllerID
	stacks.ScheduledJobs = stacksapi.ListScheduledJobsResponse{
		Jobs: []stacksapi.ScheduledJob{{
			ID:              carried,
			AgentQueryRules: []string{"queue=" + queue, "xcode=27"},
			ScheduledAt:     time.Now(),
			RunnableAt:      time.Now(),
		}},
	}
	t.Cleanup(stacks.Close)

	process := startController(t, writeConfig(t, stacks.URL()))
	name := "bk-" + controllerID + "-" + carried

	eventually(t, "the controller reserves the job", func() bool {
		return slices.Contains(stacks.Reserved(), carried)
	})

	eventually(t, "orchard runs the machine of the job", func() bool {
		machine, found := machineNamed(t, name)

		return found && machine.Status == "running"
	})

	// The agent inside the machine acquires the job, runs it, and ends it.
	stacks.SetJobState(carried, "running")
	stacks.SetJobState(carried, "finished")

	eventually(t, "the controller deletes the machine of the finished job", func() bool {
		_, found := machineNamed(t, name)

		return !found
	})

	// Invariant I-6: no machine of this controller remains.
	if owned := ownedMachines(t); len(owned) != 0 {
		t.Errorf("orchard still holds the machines %v of this controller", owned)
	}

	// One job, one reservation. A second one would mean that the controller started the job
	// twice, which is two agents for one job.
	if held := stacks.Reserved(); len(held) != 1 {
		t.Errorf("the controller reserved %v, want the job once", held)
	}

	// The job page shows the steps that this increment sends. The steps of the rest of the
	// machine life arrive with the unit that owns them, and this test grows with it.
	if !notified(stacks.Notifications(), carried, "VM requested") {
		t.Errorf("the job page reads %+v, want \"VM requested\"", stacks.Notifications())
	}

	stopController(t, process)

	if withdrawals := stacks.Deregistrations(); withdrawals != 1 {
		t.Errorf("the controller withdrew its stack %d times, want 1", withdrawals)
	}
}

// orchard finds the orchard binary. CI must run this test, because it is the evidence of the
// walking skeleton, so there a missing binary is a failure and not a skip.
func orchard(t *testing.T) string {
	t.Helper()

	if path := os.Getenv("ORCHARD_BIN"); path != "" {
		return path
	}

	path, err := exec.LookPath("orchard")
	if err == nil {
		return path
	}

	if os.Getenv("CI") != "" {
		t.Fatal("the orchard binary is not on PATH, and CI must run this test: mise install " +
			"puts the pinned version there")
	}

	t.Skip("the orchard binary is not on PATH; `mise install` puts the pinned version there, " +
		"or ORCHARD_BIN can name one")

	return ""
}

// startOrchard runs `orchard dev` in synthetic mode until the test ends.
func startOrchard(t *testing.T, binary string) {
	t.Helper()

	if answered(orchardURL + "/v1/workers") {
		t.Fatalf("something already answers at %s; stop it before this test runs", orchardURL)
	}

	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "orchard.log")

	//nolint:gosec // G204: the binary is the Orchard that mise pins, or the one that
	// ORCHARD_BIN names, and every argument is a constant or a temp dir of this test.
	command := exec.CommandContext(t.Context(), binary, "dev", "--synthetic", "--workers", "1",
		"--data-dir", filepath.Join(home, "data"), "--resources", resources)
	graceful(command)

	// `orchard dev` writes a context into the Orchard configuration of the user. A test that ran
	// with the real home would change the configuration of the developer who ran it.
	command.Env = environment(map[string]string{"HOME": home})
	command.Stdout, command.Stderr = output(t, log), output(t, log)

	if err := command.Start(); err != nil {
		t.Fatalf("starting orchard dev: %v", err)
	}

	t.Cleanup(func() { stop(t, command, "orchard dev", log) })

	var workers []struct {
		Name string `json:"name"`
		Arch string `json:"arch"`
	}

	eventually(t, "orchard dev has a worker", func() bool {
		return decode(orchardURL+"/v1/workers", &workers) == nil && len(workers) > 0
	})

	// Orchard gives a machine the architecture arm64 unless the specification states another, and
	// it places a machine only on a worker of the same architecture. A synthetic worker takes the
	// architecture of this host. On any other host the journey would wait for a placement that
	// never comes, so it stops here and says why.
	if workers[0].Arch != "arm64" {
		t.Fatalf("the synthetic worker is %s, and Orchard places a macOS machine only on an arm64 "+
			"worker; run this test on an arm64 host", workers[0].Arch)
	}
}

// writeConfig writes the configuration of the controller under test.
func writeConfig(t *testing.T, endpoint string) string {
	t.Helper()

	body := fmt.Sprintf(`controllerID: %s
buildkite:
  queue: %s
  endpoint: %s
orchard:
  url: %s
  serviceAccount: bk-agent-stack-orchard
profiles:
  - name: xcode-27
    match: {xcode: "27"}
    image: ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
`, controllerID, queue, endpoint, orchardURL)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	return path
}

// controller is a running controller, and the file that holds what it wrote.
//
// Done closes when the process exits, and err holds why. A closed channel can be read any number
// of times, which the stop of the test and the cleanup both need.
type controller struct {
	command *exec.Cmd
	log     string
	done    chan struct{}
	err     error
}

// startController runs the controller binary with a configuration.
func startController(t *testing.T, config string) *controller {
	t.Helper()

	log := filepath.Join(t.TempDir(), "controller.log")

	//nolint:gosec // G204: the binary is the one that TestMain built, with a config of this test.
	command := exec.CommandContext(t.Context(), controllerBinary, "-config", config)
	graceful(command)
	command.Env = environment(map[string]string{
		"HOME": t.TempDir(),
		"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN": "bkct_integration",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":         "integration",
		// The test has no collector. The records still reach stdout, which is the log above.
		"OTEL_TRACES_EXPORTER":  "none",
		"OTEL_METRICS_EXPORTER": "none",
		"OTEL_LOGS_EXPORTER":    "none",
	})
	command.Stdout, command.Stderr = output(t, log), output(t, log)

	if err := command.Start(); err != nil {
		t.Fatalf("starting the controller: %v", err)
	}

	running := &controller{command: command, log: log, done: make(chan struct{})}
	go func() {
		running.err = command.Wait()

		close(running.done)
	}()

	t.Cleanup(func() {
		// The end of the test cancels the context, which sends SIGTERM to a controller that a
		// failed test left running. Waiting here lets it stop before Orchard does.
		select {
		case <-running.done:
		case <-time.After(15 * time.Second):
		}

		if t.Failed() {
			t.Logf("the controller wrote:\n%s", tail(log))
		}
	})

	return running
}

// stopController sends the signal that a pod gets, and expects a clean stop.
func stopController(t *testing.T, running *controller) {
	t.Helper()

	if err := running.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the controller: %v", err)
	}

	select {
	case <-running.done:
		// A data race in the process shows here too, as a status that is not zero.
		if running.err != nil {
			t.Errorf("the controller stopped with %v, want a clean stop", running.err)
		}
	case <-time.After(20 * time.Second):
		t.Error("the controller did not stop inside 20s of SIGTERM")
	}
}

// machine is what this test reads of an Orchard machine. It decodes the answer itself, so that a
// mistake in the client of this project cannot agree with itself here.
type machine struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Worker string `json:"worker"`
}

func machineNamed(t *testing.T, name string) (machine, bool) {
	t.Helper()

	for _, found := range machines(t) {
		if found.Name == name {
			return found, true
		}
	}

	return machine{}, false
}

func ownedMachines(t *testing.T) []string {
	t.Helper()

	var owned []string

	for _, found := range machines(t) {
		if strings.HasPrefix(found.Name, "bk-"+controllerID+"-") {
			owned = append(owned, found.Name)
		}
	}

	return owned
}

func machines(t *testing.T) []machine {
	t.Helper()

	var found []machine
	if err := decode(orchardURL+"/v1/vms", &found); err != nil {
		t.Fatalf("listing the machines of orchard: %v", err)
	}

	return found
}

func notified(lines []stacksapi.StackNotification, id, detail string) bool {
	for _, line := range lines {
		if line.JobUUID == id && line.Detail == detail {
			return true
		}
	}

	return false
}

// eventually waits for a condition, and fails the test with what it waited for.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(250 * time.Millisecond)
	}

	t.Fatalf("waited 60s, and still not: %s", what)
}

func decode(url string, into any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, response.Status)
	}

	return json.NewDecoder(response.Body).Decode(into)
}

func answered(url string) bool {
	var ignored any

	err := decode(url, &ignored)

	return err == nil || !errors.Is(err, syscall.ECONNREFUSED) && !strings.Contains(
		err.Error(), "connection refused")
}

// environment gives the environment of this process with some variables replaced.
func environment(replace map[string]string) []string {
	var out []string

	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if _, replaced := replace[name]; !replaced {
			out = append(out, pair)
		}
	}

	for name, value := range replace {
		out = append(out, name+"="+value)
	}

	return out
}

func output(t *testing.T, path string) *os.File {
	t.Helper()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: a temp dir of this test
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}

	t.Cleanup(func() { _ = file.Close() })

	return file
}

// graceful makes the end of the test stop a process the way that a pod stops: SIGTERM first, and
// SIGKILL only when the process has not stopped ten seconds later.
func graceful(command *exec.Cmd) {
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 10 * time.Second
}

// stop waits for a process that the test started, whose context the end of the test cancelled,
// and shows what it wrote when the test failed.
func stop(t *testing.T, command *exec.Cmd, name, log string) {
	t.Helper()

	_ = command.Wait()

	if t.Failed() {
		t.Logf("%s wrote:\n%s", name, tail(log))
	}
}

// tail gives the last lines of a log, which is what a person needs from a failed run.
func tail(path string) string {
	file, err := os.Open(path) //nolint:gosec // the test created this path
	if err != nil {
		return err.Error()
	}
	defer func() { _ = file.Close() }()

	var lines []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > 60 {
			lines = lines[1:]
		}
	}

	return strings.Join(lines, "\n")
}
