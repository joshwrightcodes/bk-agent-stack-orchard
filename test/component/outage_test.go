// SPDX-License-Identifier: AGPL-3.0-or-later

package component_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/health"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// T-024. The outages of the target design, section 7.5, each from a steady fleet, on the clock of
// the suite. The invariants of T-020 hold throughout, and the request budget of section 3 holds.

var (
	errUnavailable = errors.New("answered 503 Service Unavailable")
	errLostAnswer  = errors.New("the connection closed before Orchard answered")
)

// callTimeout is the time limit of each call in the suite. The fakes answer at once, so only a
// fake that hangs waits for it, in real time.
const callTimeout = 5 * time.Millisecond

// hangLimit is how long a fake that hangs waits for the end of a call. A call that no limit
// bounds would hold the suite for ever, so the fake gives up after this real time and records a
// violation.
const hangLimit = 2 * time.Second

// hang is a call to an API that accepts it and never answers: it ends only when its context ends.
func hang(ctx context.Context, violations *[]string, endpoint string) error {
	timer := time.NewTimer(hangLimit)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		record(violations, fmt.Sprintf("a call of %s waited %s, so no time limit bounds it",
			endpoint, hangLimit))

		return errUnavailable
	}
}

// scenario is a world of the component suite that a test drives step by step.
type scenario struct {
	*fleet

	t      *testing.T
	checks *health.Checks
}

func newScenario(t *testing.T) *scenario {
	t.Helper()

	f := newFleetWith(t, t, quiet(t), 0)

	return &scenario{fleet: f, t: t, checks: f.readiness(t)}
}

// readiness builds the probes of the binary for the loop of this world.
func (f *fleet) readiness(t *testing.T) *health.Checks {
	t.Helper()

	var sources []health.Source
	for _, source := range f.loop.Sources() {
		sources = append(sources, source)
	}

	checks, err := health.New(health.Options{
		Sources:      sources,
		Window:       60 * time.Second, // the default of stateFreshness
		Unauthorized: func() bool { return false },
		Heartbeat:    f.loop.Heartbeat,
		Stall:        15 * time.Minute,
		Now:          func() time.Time { return f.now },
		Logger:       slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("building the readiness checks: %v", err)
	}

	return checks
}

// run ticks for some seconds of the suite, and fails the test at the first violation of an
// invariant.
func (s *scenario) run(seconds int) {
	s.t.Helper()

	for range seconds {
		s.tick()

		if len(s.violations) > 0 {
			s.t.Fatalf("at %s: %v", s.now.Format(time.TimeOnly), s.violations)
		}
	}
}

// arrive schedules a job that the profile carries, and gives its identifier.
func (s *scenario) arrive() job.ID {
	id := s.nextJob()
	s.stacks.schedule(id, true)

	return id
}

// machine gives the machine of a job, when Orchard holds one.
func (s *scenario) machine(id job.ID) (*orchard.VM, bool) {
	vm, exists := s.orchard.vms[vmspec.Name(controllerID, id)]

	return vm, exists
}

// acquire is an agent in the machine of the job that takes it.
func (s *scenario) acquire(id job.ID) {
	w := s.stacks.jobs[id]
	w.acquired, w.state = true, stateRunning
}

func (s *scenario) ready() bool {
	return len(s.checks.Unready()) == 0
}

// budget checks that no second of the run asked more of an endpoint than the Stacks API permits:
// 10 requests each second for the list and for the reserve, and 20 for the job states (target
// design, section 3).
func (s *scenario) budget() {
	s.t.Helper()

	limits := map[string]int{"list": 10, "reserve": 10, "states": 20}
	each := map[string]map[time.Time]int{}

	for _, c := range s.stacks.calls {
		if each[c.endpoint] == nil {
			each[c.endpoint] = map[time.Time]int{}
		}

		each[c.endpoint][c.at.Truncate(time.Second)]++
	}

	for endpoint, limit := range limits {
		for second, count := range each[endpoint] {
			if count > limit {
				s.t.Errorf("%d calls of %s in the second %s, and the limit is %d", count, endpoint,
					second.Format(time.TimeOnly), limit)
			}
		}
	}
}

// Buildkite is not available. The controller reserves and finishes no job, and it deletes a
// machine only by rows 1 and 2, whose rules need no job state. A job that ends while Buildkite is
// not available keeps its machine, because the controller cannot know that it ended.
func TestABuildkiteOutageDeletesOnlyByRowsOneAndTwo(t *testing.T) {
	s := newScenario(t)

	ended, waiting := s.arrive(), s.arrive()
	s.run(5)
	s.acquire(ended)
	s.run(10)

	if !s.ready() {
		t.Fatalf("the controller is not ready before the outage: %v", s.checks.Unready())
	}

	// A machine with the prefix of this controller and no job (row 1), and one that outlived the
	// longest life of a machine (row 2).
	orphan := "bk-" + controllerID + "-not-a-job"
	ancient := vmspec.Name(controllerID, s.nextJob())

	for name, created := range map[string]time.Time{
		orphan: s.now, ancient: s.now.Add(-26 * time.Hour),
	} {
		s.orchard.vms[name] = &orchard.VM{Name: name, Status: orchard.StatusPending, CreatedAt: created}
	}

	s.stacks.down = errUnavailable
	s.stacks.jobs[ended].state = stateFinished
	reserved, finished := len(s.stacks.jobs), len(s.stacks.finished)

	s.run(180)

	for name := range map[string]bool{orphan: true, ancient: true} {
		if _, exists := s.orchard.vms[name]; exists {
			t.Errorf("the machine %s remains, want rows 1 and 2 to delete it without Buildkite", name)
		}
	}

	for _, id := range []job.ID{ended, waiting} {
		if _, exists := s.machine(id); !exists {
			t.Errorf("the machine of %s is gone, want no deletion on a job state that the "+
				"controller could not read", id)
		}
	}

	if len(s.stacks.finished) != finished || len(s.stacks.jobs) != reserved {
		t.Errorf("the controller finished %v while Buildkite was not available, want nothing",
			s.stacks.finished)
	}

	if s.ready() {
		t.Error("the controller is ready while Buildkite is not available, want not ready")
	}

	// Buildkite answers again. The job that ended loses its machine, and the other job keeps its
	// machine and its reservation.
	s.stacks.down = nil
	s.run(30)

	if _, exists := s.machine(ended); exists {
		t.Error("the machine of the job that ended remains after the outage, want row 4 to delete it")
	}

	if _, exists := s.machine(waiting); !exists || s.stacks.jobs[waiting].state != stateReserved {
		t.Errorf("the reserved job is %s with a machine %t, want it reserved and on its machine",
			s.stacks.jobs[waiting].state, exists)
	}

	if !s.ready() {
		t.Errorf("the controller is not ready after the outage: %v", s.checks.Unready())
	}

	s.budget()
}

// Only the job states fail, and the queue, the reservations, and Orchard still answer. The reaper
// cannot extend a reservation on old job states, so a machine started now would keep only its
// first reservation, and a boot longer than that loses its job. Admission therefore stops while
// the fleet holds machines and their job states are old, and it starts again once they answer.
func TestAJobStatesOutageStopsAdmission(t *testing.T) {
	s := newScenario(t)

	running := s.arrive()
	s.run(5)
	s.acquire(running)
	s.run(10)

	s.stacks.failing["states"] = errUnavailable

	// The last answer of the job states ages past the freshness limit.
	s.run(70)

	late := s.arrive()
	s.run(30)

	if state := s.stacks.jobs[late].state; state != stateScheduled {
		t.Errorf("the job that arrived while the job states failed is %s, want it scheduled", state)
	}

	if _, exists := s.machine(late); exists {
		t.Error("the job that arrived while the job states failed has a machine, want none")
	}

	delete(s.stacks.failing, "states")
	s.run(30)

	if vm, exists := s.machine(late); !exists || vm.Status != orchard.StatusRunning {
		t.Error("the job that arrived while the job states failed has no running machine after it")
	}

	s.budget()
}

// Buildkite applies a rate limit and names a wait of 20 s. The poll and the job states wait for it
// before they ask again, although their own intervals are 1 s and 5 s, and they ask again once
// the wait ends. The tick does not sleep for the wait.
func TestARateLimitWithRetryAfterPausesTheBuildkiteSources(t *testing.T) {
	s := newScenario(t)

	waiting := s.arrive()
	s.run(15)

	limited := retry.Again(
		fmt.Errorf("%w: answered 429 Too Many Requests", buildkite.ErrRateLimited), 20*time.Second)
	s.stacks.down = limited
	start := s.now

	s.run(19)

	asked := map[string]int{}

	for _, c := range s.stacks.calls {
		if c.at.After(start) {
			asked[c.endpoint]++
		}
	}

	for _, endpoint := range []string{"list", "states"} {
		if asked[endpoint] != 1 {
			t.Errorf("the controller asked %s %d times inside the wait that Buildkite named, "+
				"want once", endpoint, asked[endpoint])
		}
	}

	s.stacks.down = nil
	s.run(10)

	if !s.ready() {
		t.Errorf("the controller is not ready after the wait: %v", s.checks.Unready())
	}

	if _, exists := s.machine(waiting); !exists || s.stacks.jobs[waiting].state != stateReserved {
		t.Errorf("the reserved job is %s with a machine %t, want it reserved and on its machine",
			s.stacks.jobs[waiting].state, exists)
	}

	s.budget()
}

// The Orchard controller is not available. The inventory ages, admission stops, the reaper cannot
// delete, and the machines on the workers run on. Once Orchard answers, the controller catches up.
func TestAnOrchardOutageStopsAdmissionAndDeletion(t *testing.T) {
	s := newScenario(t)

	ended := s.arrive()
	s.run(5)
	s.acquire(ended)
	s.run(10)

	s.orchard.down = errUnavailable
	s.stacks.jobs[ended].state = stateFinished

	// The last answer of the inventory ages past the freshness limit, and admission stops.
	s.run(70)

	late := s.arrive()
	s.run(60)

	if state := s.stacks.jobs[late].state; state != stateScheduled {
		t.Errorf("the job that arrived during the outage is %s, want it scheduled", state)
	}

	if _, exists := s.machine(ended); !exists {
		t.Error("the machine of the job that ended is gone, want it to run on while Orchard is " +
			"not available")
	}

	if unready := strings.Join(s.checks.Unready(), "; "); !strings.Contains(unready, "inventory") {
		t.Errorf("readiness reads %q, want the inventory named", unready)
	}

	s.orchard.down = nil
	s.run(30)

	if _, exists := s.machine(ended); exists {
		t.Error("the machine of the job that ended remains after the outage, want it deleted")
	}

	if vm, exists := s.machine(late); !exists || vm.Status != orchard.StatusRunning {
		t.Error("the job that arrived during the outage has no running machine after it")
	}

	s.budget()
}

// Buildkite accepts each call and never answers, which is worse for the loop than an error: each
// call holds the one goroutine of the loop until its time limit. The loop still runs its ticks,
// and rows 1 and 2, whose rules need no job state, delete their machines while every call to
// Buildkite hangs.
func TestAHungBuildkiteStillDeletesByRowsOneAndTwo(t *testing.T) {
	s := newScenario(t)

	waiting := s.arrive()
	s.run(15)

	orphan := "bk-" + controllerID + "-not-a-job"
	ancient := vmspec.Name(controllerID, s.nextJob())

	for name, created := range map[string]time.Time{
		orphan: s.now, ancient: s.now.Add(-26 * time.Hour),
	} {
		s.orchard.vms[name] = &orchard.VM{Name: name, Status: orchard.StatusPending, CreatedAt: created}
	}

	s.stacks.hangs = true
	finished := len(s.stacks.finished)

	s.run(5)

	for name := range map[string]bool{orphan: true, ancient: true} {
		if _, exists := s.orchard.vms[name]; exists {
			t.Errorf("the machine %s remains, want rows 1 and 2 to delete it while Buildkite hangs",
				name)
		}
	}

	if _, exists := s.machine(waiting); !exists {
		t.Error("the machine of the reserved job is gone, want no deletion on a job state that " +
			"the controller could not read")
	}

	if len(s.stacks.finished) != finished {
		t.Errorf("the controller finished %v while Buildkite hung, want nothing", s.stacks.finished)
	}

	s.stacks.hangs = false
	s.run(10)

	if _, exists := s.machine(waiting); !exists || s.stacks.jobs[waiting].state != stateReserved {
		t.Errorf("the reserved job is %s with a machine %t, want it reserved and on its machine",
			s.stacks.jobs[waiting].state, exists)
	}
}

// Orchard accepts each call and never answers. The reaper still reads the job states and extends
// the reservation of a machine that boots, so the job stays with this stack while Orchard hangs.
func TestAHungOrchardStillExtendsTheReservations(t *testing.T) {
	s := newScenario(t)

	booting := s.arrive()
	s.run(10)

	first := s.stacks.jobs[booting].reservedUntil
	if first.IsZero() {
		t.Fatal("the job has no reservation before Orchard hangs")
	}

	s.orchard.hangs = true

	// Past the moment that the reservation needs its renewal, and inside the acquire timeout,
	// after which the reaper would end the job.
	renewal := first.Add(-s.settings.Buildkite.ReservationRenewBefore)
	s.run(int(renewal.Sub(s.now).Seconds()) + 20)

	w := s.stacks.jobs[booting]
	if w.state != stateReserved || !w.reservedUntil.After(first) {
		t.Errorf("the job is %s until %s, want it reserved past %s", w.state,
			w.reservedUntil.Format(time.TimeOnly), first.Format(time.TimeOnly))
	}

	s.orchard.hangs = false
	s.run(10)

	if vm, exists := s.machine(booting); !exists || vm.Status != orchard.StatusRunning {
		t.Error("the job has no running machine after Orchard answers again")
	}
}

// Orchard accepts each call and never answers while a burst of jobs arrives, more than one tick
// can start. The tick ends inside a few time limits of a call, and not one limit for each job: the
// launcher reserves only the jobs that one step can start, and it stops its creates at the first
// that hangs. The deletion of a machine whose job ended hangs too, and the reaper of each later
// tick still extends the reservation of a machine that boots. Once Orchard answers, every job of
// the burst gets its machine.
func TestAHungOrchardDuringABurstEndsTheTickInsideItsBudget(t *testing.T) {
	loaded, environment, err := loadSettings(t, 0, "")
	if err != nil {
		t.Fatalf("loading the configuration: %v", err)
	}

	// Room for 60 machines, so that admission has room for the whole burst.
	wide := hostShape{slots: 20, cores: 160, memoryMiB: 20 * 28672}
	f := newFleetOf(t, t, quiet(t), loaded, environment, []hostShape{wide, wide, wide})
	s := &scenario{fleet: f, t: t, checks: f.readiness(t)}

	booting, ended := s.arrive(), s.arrive()
	s.run(10)
	s.acquire(ended)

	first := s.stacks.jobs[booting].reservedUntil
	if _, exists := s.machine(booting); !exists || first.IsZero() {
		t.Fatal("the booting job has no machine or no reservation before Orchard hangs")
	}

	s.orchard.hangs = true
	s.stacks.jobs[ended].state = stateFinished

	burst := make([]job.ID, 0, 2*parallel.PerStep)
	for range cap(burst) {
		burst = append(burst, s.arrive())
	}

	creates := s.orchard.asked["create"]
	began := time.Now()
	s.run(1)

	// The calls that can hang in this tick: two inventory calls, the deletions of the reaper,
	// and the creates of the launcher, each step inside its budget of parallel.Rounds limits.
	// That is about a third of the bound below. A tick that sent the creates one at a time
	// would take at least one limit for each job of the burst, so the bound leaves room for a
	// slow test machine and still fails that tick. The counts below are the main check.
	if took, most := time.Since(began), time.Duration(cap(burst))*callTimeout; took > most {
		t.Errorf("the tick of the burst took %s, want it inside %s", took, most)
	}

	reserved := 0

	for _, id := range burst {
		if s.stacks.jobs[id].state == stateReserved {
			reserved++
		}
	}

	if reserved != parallel.PerStep {
		t.Errorf("the controller reserved %d jobs of the burst, want the %d that one step can "+
			"start", reserved, parallel.PerStep)
	}

	if asked := s.orchard.asked["create"] - creates; asked == 0 || asked > parallel.Limit {
		t.Errorf("orchard saw %d creates, want at most the %d that ran when the first hung",
			asked, parallel.Limit)
	}

	// Past the moment that the reservation of the booting job needs its renewal.
	renewal := first.Add(-s.settings.Buildkite.ReservationRenewBefore)
	s.run(int(renewal.Sub(s.now).Seconds()) + 20)

	if w := s.stacks.jobs[booting]; w.state != stateReserved || !w.reservedUntil.After(first) {
		t.Errorf("the booting job is %s until %s, want it reserved past %s", w.state,
			w.reservedUntil.Format(time.TimeOnly), first.Format(time.TimeOnly))
	}

	if _, exists := s.machine(ended); !exists {
		t.Error("the machine of the job that ended is gone while Orchard hangs")
	}

	// Orchard answers again. The reservations that the creates never used lapse, and admission
	// starts those jobs again.
	s.orchard.hangs = false
	s.run(int(s.settings.Buildkite.ReservationExpiry.Seconds()) + 30)

	if _, exists := s.machine(ended); exists {
		t.Error("the machine of the job that ended remains after Orchard answers again")
	}

	for _, id := range burst {
		if vm, exists := s.machine(id); !exists || vm.Status != orchard.StatusRunning {
			t.Errorf("the job %s of the burst has no running machine after Orchard answers", id)
		}
	}

	s.budget()
}

// A worker stops reporting. After the offline timeout, Orchard fails its machines. The machine of
// a job that an agent took goes with a line on the job page, and Buildkite decides that job (row
// 7). The job of a machine that no agent took ends with a reason (row 9). Neither job gets a
// second machine.
func TestAnOfflineWorkerGivesRowsSevenAndNine(t *testing.T) {
	s := newScenario(t)

	taken, booting := s.arrive(), s.arrive()
	s.run(5)
	s.acquire(taken)
	s.run(10)

	vm, _ := s.machine(taken)
	host := s.orchard.host(vm.Worker)

	if other, _ := s.machine(booting); other.Worker != vm.Worker {
		t.Fatalf("the machines are on %s and %s, want both on one worker", vm.Worker, other.Worker)
	}

	host.stopped = true

	// The offline timeout of Orchard, then the ticks that read it.
	s.run(int(s.settings.Orchard.WorkerOfflineTimeout.Seconds()) + 15)

	for _, id := range []job.ID{taken, booting} {
		if _, exists := s.machine(id); exists {
			t.Errorf("the failed machine of %s remains, want it deleted", id)
		}
	}

	if slices.Contains(s.stacks.finished, taken) {
		t.Error("the controller finished the job that an agent took, want Buildkite to decide it")
	}

	if !slices.Contains(s.stacks.finished, booting) {
		t.Error("the job whose machine failed before an agent took it did not end, want row 9")
	}

	var page []string

	for _, line := range s.stacks.lines {
		if line.JobID == taken {
			page = append(page, line.Detail)
		}
	}

	if !slices.ContainsFunc(page, func(line string) bool { return strings.HasPrefix(line, "failed: ") }) {
		t.Errorf("the page of the job that an agent took reads %q, want a line that says it failed",
			page)
	}

	s.budget()
}
