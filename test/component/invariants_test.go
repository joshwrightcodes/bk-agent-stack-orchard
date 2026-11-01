// SPDX-License-Identifier: AGPL-3.0-or-later

// Package component_test runs the controller that ships against in-memory fakes of both APIs, on
// a clock that the suite moves, and checks the invariants of the test strategy, section 5, over
// random sequences of events (T-020).
//
// The integration tests prove the controller against a real Orchard, one journey at a time. This
// suite proves it against the orders of events that nobody thought to write down: rapid draws a
// sequence, runs it, and when an invariant breaks, it shrinks the sequence to a short one and
// prints it.
package component_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/app"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

const controllerID = "prop"

// TestMain searches wider than the defaults of rapid, which are 100 sequences of about 30
// actions: a thousand sequences of about a hundred actions take a few seconds, and find the orders
// of events that a short sequence cannot reach. A flag on the command line still wins.
func TestMain(m *testing.M) {
	flag.Parse()

	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })

	for name, value := range map[string]string{"rapid.checks": "1000", "rapid.steps": "100"} {
		if !given[name] {
			_ = flag.Set(name, value)
		}
	}

	os.Exit(m.Run())
}

// T-020. Random sequences of events hold I-1, I-2, I-5, and I-8 after every event, and I-6 at the
// end of each sequence.
func TestRandomEventsHoldTheInvariants(t *testing.T) {
	recorder := quiet(t)

	rapid.Check(t, func(rt *rapid.T) {
		fleet := newFleet(t, rt, recorder)
		rt.Repeat(rapid.StateMachineActions(fleet))
		fleet.drain(rt)
	})
}

// I-3. Whatever variables a profile names, a configuration that loads gives no machine the value
// of a controller secret. A profile that names one of the secrets does not load.
//
// Configurations with refused names never load, so the tests of the configuration prove the
// refusal, and this property proves that no configuration that loads leaks a secret. The names
// include some that the rules refuse and some that they accept, with a value for each, so that a
// loosened rule shows as a leak.
func TestNoControllerSecretReachesAnyVMSpecification(t *testing.T) {
	recorder := quiet(t)
	names := append([]string{
		"VM_USER", "VM_PASSWORD", "VM_OTEL_HEADERS", "BK_AGENT_STACK_ORCHARD_OTHER",
		"bk_agent_stack_orchard_orchard_token", "otel_exporter_otlp_headers",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS", "HTTPS_PROXY",
	}, controllerSecrets...)

	loads := 0

	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.SampledFrom(names)

		lines := ""
		if rapid.Bool().Draw(rt, "ssh") {
			lines += fmt.Sprintf("    startupTransport: ssh\n    vmCredentials: {usernameEnv: %s, passwordEnv: %s}\n",
				name.Draw(rt, "usernameEnv"), name.Draw(rt, "passwordEnv"))
		}
		if rapid.Bool().Draw(rt, "otel") {
			lines += fmt.Sprintf("    otel: {enabled: true, endpoint: https://otel.example.com, headersEnv: %s}\n",
				name.Draw(rt, "headersEnv"))
		}

		loaded, environment, err := loadSettings(t, 0, lines)
		if err != nil {
			return
		}

		loads++

		fleet := newFleetOf(t, rt, recorder, loaded, environment)
		fleet.ArriveMatched(rt)
		fleet.Wait(rt)
		fleet.Check(rt)

		if len(fleet.orchard.vms) == 0 {
			rt.Fatalf("no machine was created, so the property proved nothing")
		}
	})

	if loads == 0 {
		t.Error("no drawn configuration loaded, so the property proved nothing")
	}
}

// fleet is the world of one sequence: Buildkite, Orchard, the clock, and the controller.
type fleet struct {
	ctx         context.Context
	recorder    *telemetry.Telemetry
	settings    *config.Config
	maxInFlight uint64

	// environment holds the values of the variables that the controller and the profile read.
	// The controller secrets in it are the values that I-3 looks for in each specification.
	environment map[string]string

	now        time.Time
	stacks     *stacksFake
	orchard    *orchardFake
	loop       *controller.Controller
	jobs       int
	violations []string
}

func newFleet(t *testing.T, rt *rapid.T, recorder *telemetry.Telemetry) *fleet {
	// Zero means the slots of the fleet, and three holds the controller below the four slots.
	maxInFlight := rapid.SampledFrom([]uint64{0, 3}).Draw(rt, "maxInFlight")

	return newFleetWith(t, rt, recorder, maxInFlight)
}

// fataler is what the world needs of a test: *testing.T and *rapid.T both have it.
type fataler interface {
	Fatalf(format string, args ...any)
}

// newFleetWith builds the world with a limit that the caller chose.
func newFleetWith(t *testing.T, fatal fataler, recorder *telemetry.Telemetry,
	maxInFlight uint64,
) *fleet {
	loaded, environment, err := loadSettings(t, maxInFlight, "")
	if err != nil {
		fatal.Fatalf("loading the configuration: %v", err)
	}

	return newFleetOf(t, fatal, recorder, loaded, environment)
}

// newFleetOf builds the world around a configuration that loaded.
func newFleetOf(t *testing.T, fatal fataler, recorder *telemetry.Telemetry, loaded *config.Config,
	environment map[string]string,
) *fleet {
	f := &fleet{
		ctx:         t.Context(),
		recorder:    recorder,
		settings:    loaded,
		maxInFlight: loaded.MaxInFlight,
		environment: environment,
		now:         time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	}

	clock := func() time.Time { return f.now }
	f.stacks = newStacks(clock, &f.violations)
	f.orchard = newOrchard(clock, f.settings.Orchard.WorkerOfflineTimeout, 2)
	f.orchard.created = f.created
	f.start(fatal)

	return f
}

// start builds the controller as the binary does, with the fakes and the clock of the suite. A
// restart is a second start: the new process knows nothing of the last one.
func (f *fleet) start(rt fataler) {
	loop, err := app.Build(app.Options{
		Config:    f.settings,
		Telemetry: f.recorder,
		Stacks:    f.stacks,
		Orchard:   f.orchard,
		Now:       func() time.Time { return f.now },

		Environment: func(name string) string { return f.environment[name] },
	})
	if err != nil {
		rt.Fatalf("building the controller: %v", err)
	}

	f.loop = loop
}

// tick moves the clock one second, lets Orchard do its work, and runs one tick.
func (f *fleet) tick() {
	f.now = f.now.Add(time.Second)
	f.orchard.sync()
	f.loop.Tick(f.ctx, f.now)
}

// created checks each machine that the controller asks for, before Orchard holds it.
func (f *fleet) created(spec orchard.VM) {
	id, owned := vmspec.OwnedJob(spec.Name, controllerID)
	if !owned {
		f.violate("a machine %s that this controller does not own", spec.Name)

		return
	}

	// I-3: no controller secret in the specification, whatever the profile names.
	// G117: the specification holds the image password on purpose, and this check reads it.
	encoded, err := json.Marshal(spec) //nolint:gosec
	if err != nil {
		f.violate("I-3: the specification of %s does not encode: %v", spec.Name, err)
	}

	for name, value := range f.environment {
		if !strings.HasPrefix(name, "VM_") && strings.Contains(string(encoded), value) {
			f.violate("I-3: the specification of %s holds the value of %s", spec.Name, name)
		}
	}

	// I-1: a machine only for a job that this stack holds, and never for a job that an agent
	// took, which would be a second agent for one job.
	w, known := f.stacks.jobs[id]

	switch {
	case !known:
		f.violate("I-1: a machine for %s, which Buildkite does not know", id)
	case w.acquired:
		f.violate("I-1: a machine for %s, which an agent took", id)
	case w.state != stateReserved:
		f.violate("I-1: a machine for %s in the state %s, not reserved", id, w.state)
	case !w.matched:
		// I-8: an unmatched job uses no slot.
		f.violate("I-8: a machine for the unmatched job %s", id)
	}

	// I-2: inside maxInFlight, and inside the slots of the workers that can take a machine.
	count := len(f.orchard.owned(vmspec.Prefix(controllerID))) + 1
	if f.maxInFlight > 0 && uint64(count) > f.maxInFlight {
		f.violate("I-2: %d machines, and maxInFlight is %d", count, f.maxInFlight)
	}

	var slots, taken uint64

	for _, h := range f.orchard.hosts {
		if f.orchard.online(h.worker) && !h.worker.SchedulingPaused {
			slots += h.worker.Resources[orchard.ResourceVMs]
		}
	}

	for _, vm := range f.orchard.vms {
		if vm.Worker == "" {
			taken++
		} else if h := f.orchard.host(vm.Worker); f.orchard.online(h.worker) {
			taken++
		}
	}

	if taken+1 > slots {
		f.violate("I-2: %d machines for the %d slots of the workers that are online", taken+1, slots)
	}
}

func (f *fleet) violate(format string, args ...any) {
	f.violations = append(f.violations, fmt.Sprintf(format, args...))
}

// Check runs after every action, and fails the sequence at the first violation.
func (f *fleet) Check(rt *rapid.T) {
	if len(f.violations) > 0 {
		rt.Fatalf("at %s: %v", f.now.Format(time.TimeOnly), f.violations)
	}
}

// ArriveMatched is a job that a profile carries.
func (f *fleet) ArriveMatched(*rapid.T) {
	f.stacks.schedule(f.nextJob(), true)
	f.tick()
}

// ArriveUnmatched is a job whose tags match no profile.
func (f *fleet) ArriveUnmatched(*rapid.T) {
	f.stacks.schedule(f.nextJob(), false)
	f.tick()
}

func (f *fleet) nextJob() job.ID {
	f.jobs++

	return job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", f.jobs))
}

// Cancel is a person who cancels a job, in any state that has not ended.
func (f *fleet) Cancel(rt *rapid.T) {
	open := f.pick(func(w *work) bool { return !w.terminal() })
	if len(open) == 0 {
		rt.Skip("no job to cancel")
	}

	rapid.SampledFrom(open).Draw(rt, "cancelled").state = stateCanceled
	f.tick()
}

// Acquire is an agent in a running machine that takes its job.
func (f *fleet) Acquire(rt *rapid.T) {
	ready := f.pick(func(w *work) bool {
		vm, exists := f.orchard.vms[vmspec.Name(controllerID, w.id)]

		return w.state == stateReserved && !w.acquired && exists &&
			vm.Status == orchard.StatusRunning
	})
	if len(ready) == 0 {
		rt.Skip("no machine with a job to acquire")
	}

	w := rapid.SampledFrom(ready).Draw(rt, "acquired")
	w.acquired = true
	w.state = stateRunning
	f.tick()
}

// End is an agent that finishes its job.
func (f *fleet) End(rt *rapid.T) {
	running := f.pick(func(w *work) bool { return w.state == stateRunning })
	if len(running) == 0 {
		rt.Skip("no running job")
	}

	rapid.SampledFrom(running).Draw(rt, "ended").state = stateFinished
	f.tick()
}

// FailMachine is a machine that stops, which Orchard reports as failed.
func (f *fleet) FailMachine(rt *rapid.T) {
	var running []*orchard.VM

	for _, name := range slices.Sorted(maps.Keys(f.orchard.vms)) {
		if vm := f.orchard.vms[name]; vm.Status == orchard.StatusRunning {
			running = append(running, vm)
		}
	}

	if len(running) == 0 {
		rt.Skip("no running machine")
	}

	rapid.SampledFrom(running).Draw(rt, "failed").Status = orchard.StatusFailed
	f.tick()
}

// StopWorker is a Mac that stops reporting: it loses power, or its network.
func (f *fleet) StopWorker(rt *rapid.T) {
	h := rapid.SampledFrom(f.orchard.hosts).Draw(rt, "stopped")
	h.stopped = true
	f.tick()
}

// ResumeWorker is a Mac that reports again.
func (f *fleet) ResumeWorker(rt *rapid.T) {
	h := rapid.SampledFrom(f.orchard.hosts).Draw(rt, "resumed")
	h.stopped = false
	f.tick()
}

// TogglePause is an operator who pauses the queue, or resumes it.
func (f *fleet) TogglePause(*rapid.T) {
	f.stacks.paused = !f.stacks.paused
	f.tick()
}

// Wait lets time pass, one tick each second, long enough for a reservation to need its renewal
// and for a timeout of the reaper to pass.
func (f *fleet) Wait(rt *rapid.T) {
	for range rapid.IntRange(1, 200).Draw(rt, "seconds") {
		f.tick()
	}
}

// Restart is a new process of the controller. The machines and the jobs stay.
func (f *fleet) Restart(rt *rapid.T) {
	f.start(rt)
	f.tick()
}

// drain ends every job, brings every worker back, and lets time pass. I-6: then no machine of the
// controller remains.
func (f *fleet) drain(rt *rapid.T) {
	f.stacks.paused = false

	for _, h := range f.orchard.hosts {
		h.stopped = false
	}

	for _, w := range f.stacks.jobs {
		switch {
		case w.acquired && !w.terminal():
			w.state = stateFinished
		case !w.terminal():
			w.state = stateCanceled
		}
	}

	for range 120 {
		f.tick()
		f.Check(rt)

		if len(f.orchard.owned(vmspec.Prefix(controllerID))) == 0 {
			return
		}
	}

	var left []string
	for _, vm := range f.orchard.owned(vmspec.Prefix(controllerID)) {
		left = append(left, vm.Name+" "+vm.Status)
	}

	rt.Fatalf("I-6: two minutes after every job ended, the machines %v remain", left)
}

// pick gives the jobs that a condition selects, in the order of their arrival, so that a
// sequence that rapid replays draws the same job.
func (f *fleet) pick(condition func(*work) bool) []*work {
	var selected []*work

	for _, id := range f.stacks.order {
		if w := f.stacks.jobs[id]; condition(w) {
			selected = append(selected, w)
		}
	}

	return selected
}

// controllerSecrets name the variables that hold a secret of the controller. The values of these
// variables never reach a machine.
var controllerSecrets = []string{
	"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN",
	"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN",
	"BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT",
	"OTEL_EXPORTER_OTLP_HEADERS",
}

// loadSettings loads the configuration of the suite, with lines that the caller adds to the
// profile. It gives the environment that the controller reads, which holds a value for every
// variable that a profile can name.
func loadSettings(t *testing.T, maxInFlight uint64, profileLines string) (*config.Config,
	map[string]string, error,
) {
	t.Helper()

	body := fmt.Sprintf(`controllerID: %s
maxInFlight: %d
buildkite:
  queue: macos
orchard:
  url: https://orchard.internal:6120
  serviceAccount: bk-agent-stack-orchard
profiles:
  - name: xcode-27
    match: {xcode: "27"}
    image: ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
%s`, controllerID, maxInFlight, profileLines)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	// G101: the values are fakes that no API accepts, which the configuration needs to load.
	environment := map[string]string{ //nolint:gosec
		"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN": "bkct_component",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":         "orchard_component",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT":       "ca_component",
		"OTEL_EXPORTER_OTLP_HEADERS":                   "authorization=collector_component",
		"VM_USER":                                      "image-user",
		"VM_PASSWORD":                                  "image-password",
		"VM_OTEL_HEADERS":                              "authorization=agent_component",

		// Look-alikes and other variables of the pod hold a value, so a rule that lets one
		// through would leak it.
		"BK_AGENT_STACK_ORCHARD_OTHER":         "other_component",
		"bk_agent_stack_orchard_orchard_token": "lower_token_component",
		"otel_exporter_otlp_headers":           "lower_headers_component",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS":    "traces_component",
		"HTTPS_PROXY":                          "https://user:proxy_component@proxy.internal",
	}

	loaded, err := config.Load(path, func(name string) string { return environment[name] })

	return loaded, environment, err
}

// quiet gives telemetry that exports nothing and writes no log, so that a run of thousands of
// ticks stays fast.
func quiet(t *testing.T) *telemetry.Telemetry {
	t.Helper()

	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		t.Setenv("OTEL_"+signal+"_EXPORTER", "none")
	}

	recorder, err := telemetry.Setup(t.Context(), telemetry.Options{Output: io.Discard})
	if err != nil {
		t.Fatalf("starting telemetry: %v", err)
	}

	t.Cleanup(func() { _ = recorder.Shutdown(context.Background()) })

	return recorder
}
