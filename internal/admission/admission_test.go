// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package admission_test

import (
	"maps"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

var now = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

// pilot is the profile of the tests: two machines of this size fill one worker below.
var pilot = profile.Profile{
	Name:      "xcode-27",
	Match:     map[string]string{"xcode": "27"},
	CPU:       8,
	MemoryMiB: 28672,
}

// worker gives a Mac host that advertises room for two machines of the pilot profile.
func worker(name string, labels map[string]string) orchard.Worker {
	return orchard.Worker{
		Name:     name,
		Labels:   labels,
		LastSeen: now,
		Resources: map[string]uint64{
			orchard.ResourceVMs:       2,
			orchard.ResourceCores:     16,
			orchard.ResourceMemoryMiB: 57344,
		},
	}
}

// machine gives one machine of the pilot size on a worker. An empty worker means that Orchard
// holds it and nothing runs it yet.
func machine(name, on string) orchard.VM {
	return orchard.VM{
		Name:   name,
		Worker: on,
		Resources: map[string]uint64{
			orchard.ResourceVMs:       1,
			orchard.ResourceCores:     8,
			orchard.ResourceMemoryMiB: 28672,
		},
	}
}

// waiting gives one scheduled job that the pilot profile matches.
func waiting(id string, priority int, age time.Duration) buildkite.ScheduledJob {
	return buildkite.ScheduledJob{
		ID:          job.ID(id),
		Priority:    priority,
		Rules:       []string{"queue=macos-prod", "xcode=27"},
		ScheduledAt: now.Add(-age),
	}
}

// identifiers are jobs that differ only in their last digit.
func identifiers(count int) []string {
	ids := make([]string, 0, count)
	for i := range count {
		ids = append(ids, "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a"+string(rune('0'+i%10))+"d")
	}

	return ids
}

// online accepts every worker that carries the labels of the profile.
func online(w orchard.Worker, labels map[string]string) bool {
	for name, want := range labels {
		if w.Labels[name] != want {
			return false
		}
	}

	return true
}

func request(jobs []buildkite.ScheduledJob, fleet inventory.Snapshot) admission.Request {
	return admission.Request{
		Jobs:     jobs,
		Profiles: []profile.Profile{pilot},
		Fleet:    fleet,
		Eligible: online,
		Now:      now,
	}
}

// One tick starts no more jobs than the launcher can start inside the time budget of its step.
// The jobs past that wait for the next tick, in the order of admission, and they still count as
// waiting.
func TestAdmissionStopsAtTheMostOfOneTick(t *testing.T) {
	t.Parallel()

	ids := identifiers(3)
	jobs := []buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, 2*time.Minute),
		waiting(ids[2], 0, 3*time.Minute),
	}

	fleet := inventory.Snapshot{Workers: []orchard.Worker{
		worker("mac-01", nil), worker("mac-02", nil),
	}, At: now}

	decide := request(jobs, fleet)
	decide.Most = 2

	result := admission.Decide(decide)

	if len(result.Admit) != 2 || result.Admit[0].Job.ID != jobs[2].ID ||
		result.Admit[1].Job.ID != jobs[1].ID {
		t.Errorf("admission started %+v, want the two oldest jobs", result.Admit)
	}

	if result.Waiting[pilot.Name] != 3 {
		t.Errorf("%d jobs wait, want 3", result.Waiting[pilot.Name])
	}
}

// T-004. The fleet carries what the workers advertise and no more. One job beyond the room of
// the fleet waits, and it does not become a machine that Orchard can never place.
func TestAdmissionStopsAtTheRoomOfTheFleet(t *testing.T) {
	t.Parallel()

	ids := identifiers(3)
	jobs := []buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, time.Minute),
		waiting(ids[2], 0, time.Minute),
	}

	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	result := admission.Decide(request(jobs, fleet))

	// The worker advertises two machine slots, sixteen cores, and the memory of two machines.
	if len(result.Admit) != 2 {
		t.Fatalf("admission started %d jobs, want the 2 that the worker can carry",
			len(result.Admit))
	}

	if result.Waiting[pilot.Name] != 3 {
		t.Errorf("the demand is %d jobs, want 3", result.Waiting[pilot.Name])
	}
}

// A machine of another controller takes the same room as one of this controller, so the
// accounting must count every machine of the worker and not only its own.
func TestAdmissionCountsTheMachinesOfEveryOwner(t *testing.T) {
	t.Parallel()

	ids := identifiers(2)
	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{worker("mac-01", nil)},
		VMs:     []orchard.VM{machine("someone-elses-vm", "mac-01")},
		At:      now,
	}

	result := admission.Decide(request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, time.Minute),
	}, fleet))

	if len(result.Admit) != 1 {
		t.Errorf("admission started %d jobs, want 1 beside the machine of the other owner",
			len(result.Admit))
	}
}

// Orchard keeps the worker name of a machine that failed, and sets its scheduled condition to
// false. Such a machine holds no room, and it gets no placement of its own either, because Orchard
// does not place a machine that failed.
func TestAFailedVMOfAnotherOwnerHoldsNoSlot(t *testing.T) {
	t.Parallel()

	failed := machine("someone-elses-failed-vm", "mac-01")
	failed.Status = orchard.StatusFailed
	failed.Conditions = []orchard.Condition{{Type: "scheduled", State: "false"}}

	ids := identifiers(3)
	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{worker("mac-01", nil)},
		VMs:     []orchard.VM{failed, machine("someone-elses-vm", "mac-01")},
		At:      now,
	}

	result := admission.Decide(request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, time.Minute),
		waiting(ids[2], 0, time.Minute),
	}, fleet))

	if len(result.Admit) != 1 {
		t.Errorf("admission started %d jobs, want 1: the failed machine holds no room",
			len(result.Admit))
	}
}

// A machine that Orchard holds and no worker runs will land somewhere. Its room is promised, and
// an accounting that ignored it would start a job for room that is already gone.
func TestAdmissionCountsTheMachinesThatWaitForAWorker(t *testing.T) {
	t.Parallel()

	ids := identifiers(2)
	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{worker("mac-01", nil)},
		VMs:     []orchard.VM{machine("waiting-for-a-worker", "")},
		At:      now,
	}

	result := admission.Decide(request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, time.Minute),
	}, fleet))

	if len(result.Admit) != 1 {
		t.Errorf("admission started %d jobs, want 1 beside the machine that waits for a worker",
			len(result.Admit))
	}
}

// sized gives a worker with room for one machine, and the cores and the labels given.
func sized(name string, cores uint64, labels map[string]string) orchard.Worker {
	w := worker(name, labels)
	w.Resources[orchard.ResourceVMs] = 1
	w.Resources[orchard.ResourceCores] = cores

	return w
}

// Orchard puts a machine that waits only on a worker whose labels contain its labels. Counted on
// another worker, the machine leaves the worker that it will take looking free, and a candidate
// that needs that worker is admitted into room that Orchard has promised.
func TestAMachineThatWaitsTakesTheRoomOfTheWorkerThatItsLabelsSelect(t *testing.T) {
	t.Parallel()

	release := pilot
	release.WorkerLabels = map[string]string{"pool": "release"}

	promised := machine("someone-elses-release-vm", "")
	promised.Labels = map[string]string{"pool": "release"}

	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{
			sized("mac-01", 16, nil),
			sized("mac-02", 16, map[string]string{"pool": "release"}),
		},
		VMs: []orchard.VM{promised},
		At:  now,
	}

	settings := request([]buildkite.ScheduledJob{waiting(identifiers(1)[0], 0, time.Minute)}, fleet)
	settings.Profiles = []profile.Profile{release}

	if result := admission.Decide(settings); len(result.Admit) != 0 {
		t.Errorf("admission started %v, want none: the only release worker is promised to the "+
			"machine that waits", result.Admit)
	}
}

// A machine whose labels no worker carries never lands, so it takes no room at all.
func TestAMachineThatNoWorkerCanTakeTakesNoRoom(t *testing.T) {
	t.Parallel()

	stranded := machine("someone-elses-gpu-vm", "")
	stranded.Labels = map[string]string{"pool": "gpu"}

	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{sized("mac-01", 16, nil)},
		VMs:     []orchard.VM{stranded},
		At:      now,
	}

	result := admission.Decide(request([]buildkite.ScheduledJob{
		waiting(identifiers(1)[0], 0, time.Minute),
	}, fleet))

	if len(result.Admit) != 1 {
		t.Errorf("admission started %d jobs, want 1: no worker can take the machine that waits",
			len(result.Admit))
	}
}

// Orchard takes the machines that wait oldest first. In another order a small machine can take
// the worker that only the large one fits, and the large one then looks as if it takes nothing.
func TestTheMachinesThatWaitArePlacedOldestFirst(t *testing.T) {
	t.Parallel()

	// The list of Orchard is in the order of the names, which is not the order of creation.
	newer := machine("a-newer-vm", "")
	newer.CreatedAt = now.Add(-time.Minute)

	older := machine("b-older-vm", "")
	older.CreatedAt = now.Add(-2 * time.Minute)
	older.Resources = map[string]uint64{
		orchard.ResourceVMs:       1,
		orchard.ResourceCores:     16,
		orchard.ResourceMemoryMiB: 28672,
	}

	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{sized("mac-01", 16, nil), sized("mac-02", 8, nil)},
		VMs:     []orchard.VM{newer, older},
		At:      now,
	}

	// Orchard puts the older machine on mac-01, which is the only worker with 16 cores, and the
	// newer one on mac-02. Nothing is left.
	result := admission.Decide(request([]buildkite.ScheduledJob{
		waiting(identifiers(1)[0], 0, time.Minute),
	}, fleet))

	if len(result.Admit) != 0 {
		t.Errorf("admission started %v, want none: the two machines that wait fill both workers",
			result.Admit)
	}
}

// A worker may have room for a smaller machine, so one candidate that does not fit must not stop
// the candidates after it.
func TestACandidateThatDoesNotFitDoesNotStopTheOnesAfterIt(t *testing.T) {
	t.Parallel()

	small := profile.Profile{
		Name:      "small",
		Match:     map[string]string{"size": "small"},
		CPU:       2,
		MemoryMiB: 4096,
	}

	large := profile.Profile{
		Name:      "large",
		Match:     map[string]string{"size": "large"},
		CPU:       32,
		MemoryMiB: 131072,
	}

	ids := identifiers(2)
	big := buildkite.ScheduledJob{
		ID: job.ID(ids[0]), Priority: 9, Rules: []string{"size=large"}, ScheduledAt: now,
	}
	little := buildkite.ScheduledJob{
		ID: job.ID(ids[1]), Priority: 1, Rules: []string{"size=small"}, ScheduledAt: now,
	}

	result := admission.Decide(admission.Request{
		Jobs:     []buildkite.ScheduledJob{big, little},
		Profiles: []profile.Profile{small, large},
		Fleet:    inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now},
		Eligible: online,
		Now:      now,
	})

	if len(result.Admit) != 1 || result.Admit[0].Profile.Name != "small" {
		t.Errorf("admission started %+v, want only the small job", result.Admit)
	}
}

// The order is the one that a person sees in Buildkite: the most important job first, and the
// oldest of an equal importance first.
func TestAdmissionTakesThePriorityThenTheOldest(t *testing.T) {
	t.Parallel()

	ids := identifiers(3)
	jobs := []buildkite.ScheduledJob{
		waiting(ids[0], 1, time.Hour),
		waiting(ids[1], 5, time.Minute),
		waiting(ids[2], 1, 2*time.Hour),
	}

	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	result := admission.Decide(request(jobs, fleet))

	if len(result.Admit) != 2 {
		t.Fatalf("admission started %d jobs, want 2", len(result.Admit))
	}

	if result.Admit[0].Job.ID != job.ID(ids[1]) {
		t.Errorf("the first job is %s, want the one with the highest priority", result.Admit[0].Job.ID)
	}

	if result.Admit[1].Job.ID != job.ID(ids[2]) {
		t.Errorf("the second job is %s, want the oldest of the equal priorities",
			result.Admit[1].Job.ID)
	}
}

// A second machine for one job would be a second agent for one job.
func TestAJobThatHasAMachineIsNotStartedAgain(t *testing.T) {
	t.Parallel()

	ids := identifiers(1)
	running := machine("bk-macos-prod-"+ids[0], "mac-01")

	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{worker("mac-01", nil)},
		VMs:     []orchard.VM{running},
		Owned:   []inventory.Owned{{VM: running, Job: job.ID(ids[0])}},
		At:      now,
	}

	result := admission.Decide(request([]buildkite.ScheduledJob{waiting(ids[0], 0, time.Minute)},
		fleet))

	if len(result.Admit) != 0 {
		t.Errorf("admission started %+v, want nothing for a job that has a machine", result.Admit)
	}

	if result.Waiting[pilot.Name] != 0 {
		t.Errorf("the demand counts %d jobs, want none: the job has a machine",
			result.Waiting[pilot.Name])
	}
}

// A job that no profile carries is not ours to run. The launcher leaves it alone and writes the
// summary on the job page, so that a pipeline author learns why this stack does not start it.
func TestAJobThatNoProfileCarriesIsUnmatched(t *testing.T) {
	t.Parallel()

	ids := identifiers(1)
	odd := buildkite.ScheduledJob{
		ID: job.ID(ids[0]), Rules: []string{"xcode=99"}, ScheduledAt: now,
	}

	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	result := admission.Decide(request([]buildkite.ScheduledJob{odd}, fleet))

	if len(result.Unmatched) != 1 || result.Unmatched[0].Job.ID != odd.ID {
		t.Fatalf("admission reported %+v, want the job as unmatched", result.Unmatched)
	}

	if result.Unmatched[0].Reason() == "" {
		t.Error("the unmatched job has no reason, and the log needs one")
	}

	if result.Unmatched[0].Summary() == "" {
		t.Error("the unmatched job has no summary, and the job page needs one")
	}

	if len(result.Admit) != 0 {
		t.Errorf("admission started %+v, want nothing", result.Admit)
	}
}

// A limit more than zero is what a pilot uses: the fleet has room, and the operator does not
// want all of it yet.
func TestALimitMoreThanZeroHoldsTheFleetBack(t *testing.T) {
	t.Parallel()

	ids := identifiers(2)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	settings := request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, time.Minute),
	}, fleet)
	settings.MaxInFlight = 1

	if result := admission.Decide(settings); len(result.Admit) != 1 {
		t.Errorf("admission started %d jobs, want the 1 that the limit allows", len(result.Admit))
	}
}

// The limit of zero follows the hardware, so a worker that no job may use raises nothing.
func TestAWorkerThatIsNotEligibleGivesNoSlots(t *testing.T) {
	t.Parallel()

	ids := identifiers(1)
	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{worker("mac-01", map[string]string{"pool": "other"})},
		At:      now,
	}

	settings := request([]buildkite.ScheduledJob{waiting(ids[0], 0, time.Minute)}, fleet)
	settings.Profiles = []profile.Profile{{
		Name:         pilot.Name,
		Match:        pilot.Match,
		CPU:          pilot.CPU,
		MemoryMiB:    pilot.MemoryMiB,
		WorkerLabels: map[string]string{"pool": "pilot"},
	}}

	if result := admission.Decide(settings); len(result.Admit) != 0 {
		t.Errorf("admission started %+v, want nothing on a worker that the profile excludes",
			result.Admit)
	}
}

// The age of the oldest waiting job is the number that the alert "Jobs wait too long" reads.
func TestTheDemandCarriesTheAgeOfTheOldestJob(t *testing.T) {
	t.Parallel()

	ids := identifiers(2)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	result := admission.Decide(request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, 20*time.Minute),
	}, fleet))

	if got := result.OldestWaiting[pilot.Name]; got != 20*time.Minute {
		t.Errorf("the oldest job waited %s, want 20m", got)
	}
}

// A job that this process just started still looks like a waiting job with no machine, because
// the inventory learns of a launch only at its next refresh. Admission must not start it again:
// a second start is a second reservation, a second token, and a second line on the job page.
func TestAJobThatThisProcessHoldsIsNotStartedAgain(t *testing.T) {
	t.Parallel()

	ids := identifiers(1)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	settings := request([]buildkite.ScheduledJob{waiting(ids[0], 0, time.Minute)}, fleet)
	settings.Held = map[job.ID]string{job.ID(ids[0]): pilot.Name}

	result := admission.Decide(settings)

	if len(result.Admit) != 0 {
		t.Errorf("admission started %+v, want nothing for a job that this process holds",
			result.Admit)
	}

	if result.Waiting[pilot.Name] != 0 {
		t.Errorf("the demand counts %d jobs, want none: the held job is not waiting",
			result.Waiting[pilot.Name])
	}
}

// A machine that this process asked for takes its room before the inventory shows it. Without
// this, a full fleet starts jobs for room that is already promised.
func TestAMachineThatThisProcessAskedForTakesItsRoom(t *testing.T) {
	t.Parallel()

	ids := identifiers(3)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	settings := request([]buildkite.ScheduledJob{
		waiting(ids[1], 0, time.Minute),
		waiting(ids[2], 0, time.Minute),
	}, fleet)
	settings.Held = map[job.ID]string{job.ID(ids[0]): pilot.Name}

	// A limit that does not bind, so that only the room of the worker decides. The machine on its
	// way also counts against the limit, and a limit of the fleet slots would give the same answer
	// without proving anything about the room.
	settings.MaxInFlight = 10

	// The worker has room for two machines, and one of them is already on its way.
	if result := admission.Decide(settings); len(result.Admit) != 1 {
		t.Errorf("admission started %d jobs, want the 1 beside the machine on its way",
			len(result.Admit))
	}
}

// A machine that this process asked for counts against the limit, as one that runs does.
func TestAMachineThatThisProcessAskedForCountsAgainstTheLimit(t *testing.T) {
	t.Parallel()

	ids := identifiers(2)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	settings := request([]buildkite.ScheduledJob{waiting(ids[1], 0, time.Minute)}, fleet)
	settings.Held = map[job.ID]string{job.ID(ids[0]): pilot.Name}
	settings.MaxInFlight = 1

	if result := admission.Decide(settings); len(result.Admit) != 0 {
		t.Errorf("admission started %+v, want nothing beyond the limit of 1", result.Admit)
	}
}

// A job that this process reserved without asking for a machine, because Buildkite gave it no
// token, is still not a candidate. It takes no room, because no machine is on its way.
func TestAHeldJobWithNoMachineTakesNoRoom(t *testing.T) {
	t.Parallel()

	ids := identifiers(3)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	settings := request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, time.Minute),
		waiting(ids[2], 0, time.Minute),
	}, fleet)
	settings.Held = map[job.ID]string{job.ID(ids[0]): ""}

	result := admission.Decide(settings)

	if len(result.Admit) != 2 {
		t.Fatalf("admission started %d jobs, want the 2 that the worker can carry",
			len(result.Admit))
	}

	for _, candidate := range result.Admit {
		if candidate.Job.ID == job.ID(ids[0]) {
			t.Error("admission started the job that this process holds")
		}
	}
}

// The decision is a pure function (target design, section 12): the same observation gives the
// same jobs. The machines that this process asked for take their room one after another, on the
// first worker with room, so their order decides what is left. Here the 8-core machine first
// leaves room for the candidate, and the 28 GiB machine first does not.
func TestTheMachinesThatThisProcessAskedForTakeTheirRoomInAFixedOrder(t *testing.T) {
	t.Parallel()

	small := profile.Profile{Name: "small", Match: map[string]string{"xcode": "26"}, CPU: 8, MemoryMiB: 6144}
	large := profile.Profile{Name: "large", Match: map[string]string{"xcode": "25"}, CPU: 6, MemoryMiB: 28672}

	slots := func(name string, cores, memoryMiB uint64) orchard.Worker {
		w := worker(name, nil)
		w.Resources[orchard.ResourceCores] = cores
		w.Resources[orchard.ResourceMemoryMiB] = memoryMiB

		return w
	}

	ids := identifiers(3)
	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{slots("mac-01", 16, 32768), slots("mac-02", 8, 65536)},
		At:      now,
	}

	candidate := waiting(ids[2], 0, time.Minute)
	candidate.Rules = []string{"queue=macos-prod", "xcode=26"}

	settings := request([]buildkite.ScheduledJob{candidate}, fleet)
	settings.Profiles = []profile.Profile{pilot, small, large}
	settings.MaxInFlight = 10
	settings.Held = map[job.ID]string{job.ID(ids[0]): small.Name, job.ID(ids[1]): large.Name}

	// In the order of the jobs, the small machine takes mac-01, the large one mac-02, and mac-01
	// keeps 8 cores and 26 GiB for the candidate.
	for range 100 {
		if result := admission.Decide(settings); len(result.Admit) != 1 {
			t.Fatalf("admission started %d jobs, want the candidate in the room that the machines "+
				"on their way leave in the order of their jobs", len(result.Admit))
		}
	}
}

// While the gate of admission is closed, the loop still counts the jobs that wait, so the demand
// that the fleet faces stays visible. Waiting gives the demand and decides nothing.
func TestWaitingCountsTheDemandAndAdmitsNothing(t *testing.T) {
	t.Parallel()

	ids := identifiers(2)
	fleet := inventory.Snapshot{Workers: []orchard.Worker{worker("mac-01", nil)}, At: now}

	result := admission.Waiting(request([]buildkite.ScheduledJob{
		waiting(ids[0], 0, time.Minute),
		waiting(ids[1], 0, 20*time.Minute),
	}, fleet))

	if len(result.Admit) != 0 {
		t.Errorf("Waiting() admitted %v, want nothing", result.Admit)
	}

	if result.Waiting[pilot.Name] != 2 {
		t.Errorf("Waiting() counts %d jobs of the profile, want 2", result.Waiting[pilot.Name])
	}

	if got := result.OldestWaiting[pilot.Name]; got != 20*time.Minute {
		t.Errorf("the oldest job waited %s, want 20m", got)
	}
}

// The slots of a pool say what admission can use. A slot of a paused worker, or of a worker that
// no profile can use, is unavailable. A slot that a machine waiting for a worker, or a machine that
// this process asked for, will take is reserved. Only the rest is free, so a full fleet reads 0
// free while a host is paused or a machine is on its way.
func TestTheSlotsOfAPoolAreTheRoomThatAdmissionCanUse(t *testing.T) {
	t.Parallel()

	paused := worker("mac-02", map[string]string{"pool": "pilot"})
	paused.SchedulingPaused = true

	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{
			worker("mac-01", map[string]string{"pool": "pilot"}),
			paused,
			worker("mac-03", map[string]string{"pool": "other"}),
			worker("mac-04", map[string]string{"pool": "pilot"}),
		},
		VMs: []orchard.VM{
			machine("on-one", "mac-01"),
			machine("waiting", ""),
		},
		At: now,
	}

	settings := request(nil, fleet)
	settings.Profiles = []profile.Profile{{
		Name: pilot.Name, Match: pilot.Match, CPU: pilot.CPU, MemoryMiB: pilot.MemoryMiB,
		WorkerLabels: map[string]string{"pool": "pilot"},
	}}
	settings.Eligible = func(w orchard.Worker, labels map[string]string) bool {
		return !w.SchedulingPaused && online(w, labels)
	}
	settings.Held = map[job.ID]string{"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d": pilot.Name}

	got := admission.Slots(settings, func(orchard.Worker) bool { return true })

	want := map[telemetry.Slot]int64{
		// mac-01 carries one and has one open, which the machine that waits takes. mac-04 has
		// two open, and the machine that this process asked for takes one.
		{Pool: "pilot", State: telemetry.SlotsReserved}:    2,
		{Pool: "pilot", State: telemetry.SlotsUnavailable}: 2,
		{Pool: "pilot", State: telemetry.SlotsFree}:        1,
		// No profile carries a machine to a worker of another pool.
		{Pool: "other", State: telemetry.SlotsReserved}:    0,
		{Pool: "other", State: telemetry.SlotsUnavailable}: 2,
		{Pool: "other", State: telemetry.SlotsFree}:        0,
	}

	if !maps.Equal(got, want) {
		t.Errorf("Slots() = %v, want %v", got, want)
	}
}

// A worker that carries more machines than it advertises has nothing open, and not less.
func TestAFullWorkerHasNoFreeSlotAndNotLess(t *testing.T) {
	t.Parallel()

	fleet := inventory.Snapshot{
		Workers: []orchard.Worker{worker("mac-01", nil)},
		VMs: []orchard.VM{
			machine("one", "mac-01"), machine("two", "mac-01"), machine("three", "mac-01"),
		},
		At: now,
	}

	got := admission.Slots(request(nil, fleet), func(orchard.Worker) bool { return true })
	if got[telemetry.Slot{State: telemetry.SlotsFree}] != 0 ||
		got[telemetry.Slot{State: telemetry.SlotsReserved}] != 0 {
		t.Errorf("Slots() = %v, want nothing free or reserved", got)
	}
}
