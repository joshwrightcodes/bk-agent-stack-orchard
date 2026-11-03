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
	"cmp"
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
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	"pgregory.net/rapid"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/app"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

const controllerID = "prop"

// TestMain searches wider than the defaults of rapid, which are 100 sequences of about 30
// actions: 400 sequences of about a hundred actions take about a minute with the race
// detector, and find the orders of events that a short sequence cannot reach. The nightly run
// searches far wider. A flag on the command line still wins.
//
// VerifyTestMain fails the run when a goroutine outlives the tests: a controller that leaves a
// loop behind after the suite shuts it down is a leak that a long-lived process pays for.
func TestMain(m *testing.M) {
	flag.Parse()

	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })

	for name, value := range map[string]string{"rapid.checks": "400", "rapid.steps": "100"} {
		if !given[name] {
			_ = flag.Set(name, value)
		}
	}

	goleak.VerifyTestMain(m)
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

// shape is the configuration and the hardware of one sequence. Every field is drawn, and the
// configuration goes through config.Load, so a drawn input that the rules refuse fails the
// sequence and is never repaired.
type shape struct {
	maxInFlight uint64
	profiles    string
	hosts       []hostShape
}

// drawShape draws one to three profiles with distinct names and match maps, one to three
// workers, and a limit that is sometimes below the slots and sometimes above them. A limit above
// the slots leaves the fit of a machine on a worker as the only guard of a full fleet.
func drawShape(rt *rapid.T) shape {
	var drawn shape

	drawn.maxInFlight = rapid.SampledFrom([]uint64{0, 1, 2, 3, 5, 8}).Draw(rt, "maxInFlight")

	for i := range rapid.IntRange(1, 3).Draw(rt, "profiles") {
		label := fmt.Sprintf("profile%d", i)
		suffix := rapid.StringMatching(`[a-z0-9]{1,8}`).Draw(rt, label+" name")
		cpu := rapid.IntRange(1, 8).Draw(rt, label+" cpu")
		memory := rapid.IntRange(4, 28).Draw(rt, label+" memory GiB") * 1024
		disk := rapid.IntRange(20, 150).Draw(rt, label+" disk GB")

		// The xcode tag differs for each profile, so no two match maps are equal.
		match := fmt.Sprintf("xcode: %q", fmt.Sprint(20+i))
		if rapid.Bool().Draw(rt, label+" has a second tag") {
			match += fmt.Sprintf(", os: %q", rapid.StringMatching(`[a-z0-9]{1,6}`).Draw(rt, label+" os"))
		}

		drawn.profiles += fmt.Sprintf(`  - name: p%d-%s
    match: {%s}
    image: ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0
    cpu: %d
    memoryMiB: %d
    diskSizeGB: %d
`, i, suffix, match, cpu, memory, disk)

		if rapid.Bool().Draw(rt, label+" uses ssh") {
			drawn.profiles += "    startupTransport: ssh\n" +
				"    vmCredentials: {usernameEnv: VM_USER, passwordEnv: VM_PASSWORD}\n"
		}
	}

	for i := range rapid.IntRange(1, 3).Draw(rt, "workers") {
		label := fmt.Sprintf("worker%d", i)

		drawn.hosts = append(drawn.hosts, hostShape{
			slots:     rapid.Uint64Range(1, 2).Draw(rt, label+" slots"),
			cores:     rapid.SampledFrom([]uint64{8, 16}).Draw(rt, label+" cores"),
			memoryMiB: rapid.SampledFrom([]uint64{32768, 65536}).Draw(rt, label+" memory MiB"),
		})
	}

	return drawn
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

		fleet := newFleetOf(t, rt, recorder, loaded, environment, nil)
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

	// asked holds the names of the machines that the controller asked for in the current tick.
	// The fake Orchard calls created under its lock, so the creates of the launcher add to it one
	// at a time.
	asked []string

	// foreign holds the names of the machines that another tool or controller owns. This
	// controller must never delete one.
	foreign []string
}

func newFleet(t *testing.T, rt *rapid.T, recorder *telemetry.Telemetry) *fleet {
	drawn := drawShape(rt)

	loaded, environment, err := loadProfiles(t, drawn.maxInFlight, drawn.profiles)
	if err != nil {
		rt.Fatalf("a drawn configuration does not load: %v\n%s", err, drawn.profiles)
	}

	return newFleetOf(t, rt, recorder, loaded, environment, drawn.hosts)
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

	return newFleetOf(t, fatal, recorder, loaded, environment, nil)
}

// newFleetOf builds the world around a configuration that loaded, and around workers of the
// shapes that the caller gives. No shape means the two workers of the fixed scenarios.
func newFleetOf(t *testing.T, fatal fataler, recorder *telemetry.Telemetry, loaded *config.Config,
	environment map[string]string, hosts []hostShape,
) *fleet {
	if len(hosts) == 0 {
		hosts = []hostShape{defaultHost, defaultHost}
	}

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
	f.orchard = newOrchardOf(clock, f.settings.Orchard.WorkerOfflineTimeout, hosts)
	f.orchard.created = f.created
	f.orchard.violations = &f.violations
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
		CallTimeout: callTimeout,
	})
	if err != nil {
		rt.Fatalf("building the controller: %v", err)
	}

	f.loop = loop
}

// tick moves the clock one second, lets Orchard do its work, runs one tick, and checks the room of
// the machines that the tick asked for.
func (f *fleet) tick() {
	f.now = f.now.Add(time.Second)
	f.orchard.sync()
	f.loop.Tick(f.ctx, f.now)
	f.checkRoom()
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

	// I-2: inside maxInFlight, and inside the slots of the workers that can take a machine. Only
	// scheduled machines take a slot.
	count := len(f.orchard.owned(vmspec.Prefix(controllerID))) + 1
	if f.maxInFlight > 0 && uint64(count) > f.maxInFlight {
		f.violate("I-2: %d machines, and maxInFlight is %d", count, f.maxInFlight)
	}

	// The room of the machine is a check of the whole tick, after its last create (checkRoom).
	f.asked = append(f.asked, spec.Name)
}

// checkRoom checks the room part of I-2 at the end of a tick: Orchard can place each machine that
// the tick asked for beside the machines that it holds.
//
// The launcher sends the creates of one tick at the same time, so they reach Orchard in any order.
// The check runs after the last of them, on the whole tick, because first-fit placement is not
// monotone: a part of a tick can fit nowhere where the whole tick fits. A verdict on a part would
// depend on the order of the goroutines, and rapid could not replay it.
func (f *fleet) checkRoom() {
	asked := f.asked
	f.asked = nil

	for _, name := range f.unplaced(asked) {
		f.violate("I-2: no worker that is online has room for %s beside the machines of Orchard", name)
	}
}

// unplaced gives the machines of a tick that Orchard cannot place beside the machines that it
// holds. It places the machines that wait, oldest first, on the first worker with room, as Orchard
// does. It does its own sums and not orchard.Fits, because the function under test must not mark
// its own work.
//
// The machines of one tick share their creation time, and the fake places them, as its scheduler
// does, in the order of their names, which is the order of admission. Every machine of the tick
// must fit in that order. A machine of an earlier tick that fits nowhere holds no room, and the
// machines after it still go to a worker with room (target design, section 6.3, step 5).
//
// A failed machine takes no room, because Orchard frees its resources. A worker that is not
// online, or that is paused, takes no machine.
func (f *fleet) unplaced(tick []string) []string {
	if len(tick) == 0 {
		return nil
	}

	free := map[string]map[string]uint64{}

	for _, h := range f.orchard.hosts {
		if !f.orchard.online(h.worker) || h.worker.SchedulingPaused {
			continue
		}

		free[h.worker.Name] = maps.Clone(h.worker.Resources)
	}

	var waiting []orchard.VM

	for _, vm := range f.orchard.vms {
		switch {
		case vm.Status == orchard.StatusFailed:
		case vm.Worker != "":
			// A worker that is not online or is paused has no entry, and a machine on it takes
			// no room from any other worker.
			if room, usable := free[vm.Worker]; usable {
				subtract(room, vm.Resources)
			}
		default:
			waiting = append(waiting, *vm)
		}
	}

	slices.SortFunc(waiting, func(a, b orchard.VM) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.Name, b.Name))
	})

	var left []string

	for _, vm := range waiting {
		if !f.place(free, vm) && slices.Contains(tick, vm.Name) {
			left = append(left, vm.Name)
		}
	}

	return left
}

// place takes the room of a machine on the first worker that has it, and reports whether one had.
func (f *fleet) place(free map[string]map[string]uint64, vm orchard.VM) bool {
	for _, h := range f.orchard.hosts {
		room, usable := free[h.worker.Name]
		if !usable || !contains(h.worker.Labels, vm.Labels) {
			continue
		}

		enough := true

		for name, need := range vm.Resources {
			if room[name] < need {
				enough = false
			}
		}

		if enough {
			subtract(room, vm.Resources)

			return true
		}
	}

	return false
}

// subtract takes resources from the room of one worker, and stops at zero.
func subtract(room, taken map[string]uint64) {
	for name, value := range taken {
		room[name] -= min(value, room[name])
	}
}

func (f *fleet) violate(format string, args ...any) {
	record(&f.violations, fmt.Sprintf(format, args...))
}

// recording guards the list of violations. The controller calls the fakes from several goroutines
// at once, and each fake can record a violation.
var recording sync.Mutex

// record adds a violation to the list.
func record(violations *[]string, violation string) {
	recording.Lock()
	defer recording.Unlock()

	*violations = append(*violations, violation)
}

// Check runs after every action, and fails the sequence at the first violation.
func (f *fleet) Check(rt *rapid.T) {
	// I-8: the controller leaves an unmatched job scheduled. It never reserves it, never
	// finishes it, and never gives it a machine, for another stack may carry it.
	for id, w := range f.stacks.jobs {
		if w.matched {
			continue
		}

		if w.state == stateReserved || slices.Contains(f.stacks.finished, id) {
			f.violate("I-8: the controller took the unmatched job %s (state %s)", id, w.state)
		}
	}

	// A machine of another owner is not the business of this controller, however long it waits or
	// whatever state it is in.
	for _, name := range f.foreign {
		if _, exists := f.orchard.vms[name]; !exists {
			f.violate("the controller deleted %s, a machine that it does not own", name)
		}
	}

	if len(f.violations) > 0 {
		rt.Fatalf("at %s: %v", f.now.Format(time.TimeOnly), f.violations)
	}
}

// ArriveMatched is a job that a profile carries. The job carries the tags of one profile.
func (f *fleet) ArriveMatched(rt *rapid.T) {
	chosen := f.settings.Profiles[rapid.IntRange(0, len(f.settings.Profiles)-1).Draw(rt, "profile")]

	rules := []string{"queue=" + f.settings.Buildkite.Queue}
	for _, key := range slices.Sorted(maps.Keys(chosen.Match)) {
		rules = append(rules, key+"="+chosen.Match[key])
	}

	f.stacks.scheduleRules(f.nextJob(), rules, true)
	f.tick()
}

// ArriveWithLostCreateAnswer is a job whose create reaches Orchard, and the answer gets lost. The
// controller sees an error, and the machine exists. The controller must not start a second
// machine for the job, and must count the first one.
func (f *fleet) ArriveWithLostCreateAnswer(rt *rapid.T) {
	f.orchard.answerLost[vmspec.Name(controllerID, jobID(f.jobs+1))] = true
	f.ArriveMatched(rt)
}

// ArriveWithConflictAtCreate is a job whose machine already exists when the controller creates it,
// as after the restart of the process that made it. Orchard answers 409, and the job has its
// machine.
func (f *fleet) ArriveWithConflictAtCreate(rt *rapid.T) {
	f.orchard.conflict[vmspec.Name(controllerID, jobID(f.jobs+1))] = true
	f.ArriveMatched(rt)
}

// ArriveUnmatched is a job whose tags match no profile. The drawn profiles carry the tags xcode=20
// to xcode=22, so xcode=99 matches none of them. The controller skips the job: it never reserves
// it, never finishes it, and never gives it a machine (I-8).
func (f *fleet) ArriveUnmatched(*rapid.T) {
	f.stacks.schedule(f.nextJob(), false)
	f.tick()
}

func (f *fleet) nextJob() job.ID {
	f.jobs++

	return jobID(f.jobs)
}

// jobID gives the identifier of the job that arrives in the place n of a sequence, from 1.
func jobID(n int) job.ID {
	return job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", n))
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

	fail(rapid.SampledFrom(running).Draw(rt, "failed"), "the machine failed")
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

// ToggleOrchardOutage is an Orchard controller that stops answering, or one that answers again.
// The machines on the workers run on while it is down.
func (f *fleet) ToggleOrchardOutage(*rapid.T) {
	if f.orchard.down == nil {
		f.orchard.down = errUnavailable
	} else {
		f.orchard.down = nil
	}

	f.tick()
}

// ToggleStacksFailure is a Stacks API that refuses every call, with 429 or with 503, or one that
// answers again.
func (f *fleet) ToggleStacksFailure(rt *rapid.T) {
	switch {
	case f.stacks.down != nil:
		f.stacks.down = nil
	case rapid.Bool().Draw(rt, "rate limited"):
		f.stacks.down = fmt.Errorf("%w: answered 429 Too Many Requests", buildkite.ErrRateLimited)
	default:
		f.stacks.down = errUnavailable
	}

	f.tick()
}

// ForeignMachine is a machine that another tool or another controller created. It takes a slot
// and it is none of the business of this controller.
func (f *fleet) ForeignMachine(rt *rapid.T) {
	var name string

	if rapid.Bool().Draw(rt, "of another controller") {
		name = "bk-other-" + string(f.nextJob())
	} else {
		name = fmt.Sprintf("manual-%d", len(f.foreign))
	}

	f.orchard.vms[name] = &orchard.VM{
		Name:      name,
		Status:    orchard.StatusPending,
		CreatedAt: f.now,
		Resources: map[string]uint64{
			orchard.ResourceVMs:       1,
			orchard.ResourceCores:     rapid.Uint64Range(1, 8).Draw(rt, "foreign cores"),
			orchard.ResourceMemoryMiB: rapid.Uint64Range(4, 28).Draw(rt, "foreign memory GiB") * 1024,
		},
	}
	f.foreign = append(f.foreign, name)
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
	f.stacks.down = nil
	f.stacks.hangs = false
	f.orchard.down = nil
	f.orchard.hangs = false
	f.orchard.deleteFails = nil

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

	return loadProfiles(t, maxInFlight, fmt.Sprintf(`  - name: xcode-27
    match: {xcode: "27"}
    image: ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
%s`, profileLines))
}

// loadProfiles loads the configuration of the suite with the profiles that the caller gives, as
// the lines of the profiles list.
func loadProfiles(t *testing.T, maxInFlight uint64, profiles string) (*config.Config,
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
%s`, controllerID, maxInFlight, profiles)

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
