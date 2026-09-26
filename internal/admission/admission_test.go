package admission_test

import (
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
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

// A job that no profile carries must not wait for a machine that never comes. The launcher ends
// it with the reason, which a pipeline author reads on the job page.
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

	if result.Unmatched[0].Reason == "" {
		t.Error("the unmatched job has no reason, and the job page needs one")
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

// The age of the oldest waiting job is the number that the alert of QA-02 reads.
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
