package component_test

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/health"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// T-024. The outages of the target design, section 7.5, each from a steady fleet, on the clock of
// the suite. The invariants of T-020 hold throughout, and the request budget of section 3 holds.

var errUnavailable = errors.New("answered 503 Service Unavailable")

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
		Unauthorized: func() bool { return false },
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
	s.run(61)

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
