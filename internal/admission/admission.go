// Package admission decides which waiting jobs the fleet can start now.
//
// The decision is a pure function of what the controller observed: the queue, the machines that
// Orchard holds, the workers of the fleet, and the profiles. It starts nothing and it calls
// nothing, so a test of a full fleet needs no API and no clock.
//
// The accounting is hypothetical. Orchard makes the real placement, and this function only
// decides how many jobs to start, so a disagreement between the two costs a machine that waits
// and never a job that runs twice (target design, section 6.3).
package admission

import (
	"cmp"
	"slices"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/match"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Candidate is one job that the fleet can carry, and the profile that carries it.
type Candidate struct {
	Job     buildkite.ScheduledJob
	Profile profile.Profile
}

// Unmatched is one job of the queue that no profile of this controller can carry. The launcher
// reserves it and ends it with this reason, so that a pipeline author reads it on the job page
// instead of waiting for a machine that never comes.
type Unmatched struct {
	Job    buildkite.ScheduledJob
	Reason string
}

// Eligible reports a worker that may take a new machine with these labels.
type Eligible func(worker orchard.Worker, labels map[string]string) bool

// Request is everything that the decision reads.
type Request struct {
	// Jobs are the jobs of the queue, as the poll read them.
	Jobs []buildkite.ScheduledJob

	// Profiles are the machine types of this controller.
	Profiles []profile.Profile

	// Fleet is what Orchard held.
	Fleet inventory.Snapshot

	// Eligible decides whether a worker may take a machine of a profile.
	Eligible Eligible

	// Held are the jobs that this process holds and that Fleet does not show a machine for, each
	// with the profile of the machine that this process asked for, or empty text when it asked
	// for none. The inventory learns of a launch only at its next refresh, and without this a job
	// that this process just started looks like a waiting job with no machine.
	Held map[job.ID]string

	// MaxInFlight limits the machines of this controller. Zero means the slots of the fleet, so
	// that the limit follows the hardware and holds no slot empty.
	MaxInFlight uint64

	// Now is the time that the ages are measured from.
	Now time.Time
}

// Result is the decision.
type Result struct {
	// Admit are the jobs to start, in the order to start them.
	Admit []Candidate

	// Unmatched are the jobs to end with a reason.
	Unmatched []Unmatched

	// Matched are the jobs that a profile carries and that have no machine yet, in the order of
	// admission. Admit is the part of them that the fleet has room for.
	Matched []Candidate

	// Waiting is the jobs of each profile that have no machine yet, which includes the ones that
	// this decision admits. It is the demand that the fleet faces.
	Waiting map[string]int64

	// OldestWaiting is the age of the oldest waiting job of each profile.
	OldestWaiting map[string]time.Duration
}

// Observe adds what this decision saw to the observation of the fleet.
func (r Result) Observe(observation *telemetry.Observation) {
	observation.Waiting = r.Waiting
	observation.OldestWaiting = r.OldestWaiting
}

// Decide gives the jobs to start and the jobs to end.
func Decide(request Request) Result {
	result := Result{
		Waiting:       map[string]int64{},
		OldestWaiting: map[string]time.Duration{},
	}

	candidates := sift(request, &result)

	// Highest priority first, and the oldest of an equal priority first, which is the order that
	// Buildkite shows a person.
	slices.SortStableFunc(candidates, func(a, b Candidate) int {
		if order := cmp.Compare(b.Job.Priority, a.Job.Priority); order != 0 {
			return order
		}

		return a.Job.ScheduledAt.Compare(b.Job.ScheduledAt)
	})

	result.Matched = candidates

	free := freeResources(request)
	limit := inFlightLimit(request)
	running := uint64(len(request.Fleet.Owned)) + uint64(len(requested(request)))

	for _, candidate := range candidates {
		if running >= limit {
			break
		}

		if !place(request, free, candidate.Profile) {
			// A worker may have room for a smaller machine, so one candidate that does not fit
			// does not stop the ones after it.
			continue
		}

		result.Admit = append(result.Admit, candidate)
		running++
	}

	return result
}

// sift splits the queue into the jobs that this controller can carry and the jobs that it
// cannot, and it counts what waits.
func sift(request Request, result *Result) []Candidate {
	var candidates []Candidate

	for _, waiting := range request.Jobs {
		chosen, err := match.Match(waiting.Rules, request.Profiles)
		if err != nil {
			result.Unmatched = append(result.Unmatched, Unmatched{Job: waiting, Reason: err.Error()})

			continue
		}

		// A job that already has a machine is running, or about to. A second machine for it
		// would be a second agent for one job.
		if _, has := request.Fleet.Job(waiting.ID); has {
			continue
		}

		// A job that this process holds is its own, even when the inventory does not show its
		// machine yet. A poll that failed keeps an answer that still lists it.
		if _, held := request.Held[waiting.ID]; held {
			continue
		}

		candidates = append(candidates, Candidate{Job: waiting, Profile: chosen})

		result.Waiting[chosen.Name]++

		if age := request.Now.Sub(waiting.ScheduledAt); age > result.OldestWaiting[chosen.Name] {
			result.OldestWaiting[chosen.Name] = age
		}
	}

	return candidates
}

// inFlightLimit gives the number of machines that this controller may hold at one time.
func inFlightLimit(request Request) uint64 {
	if request.MaxInFlight > 0 {
		return request.MaxInFlight
	}

	// Zero means the slots of the fleet. The limit then follows the hardware: a worker that
	// joins raises it, and a worker that goes offline lowers it.
	var slots uint64

	for _, worker := range request.Fleet.Workers {
		if request.Eligible(worker, nil) {
			slots += worker.Resources[orchard.ResourceVMs]
		}
	}

	return slots
}

// freeResources gives what each worker has left, after the machines that the fleet already
// carries and after a hypothetical placement of the machines that wait for a worker.
func freeResources(request Request) map[string]map[string]uint64 {
	free := make(map[string]map[string]uint64, len(request.Fleet.Workers))

	for _, worker := range request.Fleet.Workers {
		free[worker.Name] = orchard.Free(worker, request.Fleet.Carried(worker.Name))
	}

	// A machine that Orchard holds and no worker runs will land somewhere. Its resources are
	// spent even though no worker carries it yet, and an accounting that ignored it would start
	// jobs for room that is already promised.
	//
	// Orchard takes the machines that wait in the order of their creation, and it puts each one
	// only on a worker whose labels contain the labels of the machine. The accounting does the
	// same: a machine counted where Orchard never puts it spends room that stays free, and leaves
	// the room that it does take looking free.
	unplaced := request.Fleet.Unplaced()
	slices.SortStableFunc(unplaced, func(a, b orchard.VM) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	for _, waiting := range unplaced {
		for _, worker := range request.Fleet.Workers {
			if !request.Eligible(worker, waiting.Labels) {
				continue
			}

			if orchard.Fits(free[worker.Name], waiting.Resources) {
				orchard.Take(free[worker.Name], waiting.Resources)

				break
			}
		}
	}

	// A machine that this process asked for is on its way, and the inventory does not show it
	// yet. Its room is promised in the same way.
	for _, chosen := range requested(request) {
		place(request, free, chosen)
	}

	return free
}

// requested gives the profiles of the machines that this process asked for and the inventory does
// not show yet.
func requested(request Request) []profile.Profile {
	var machines []profile.Profile

	for _, name := range request.Held {
		if name == "" {
			continue
		}

		for _, p := range request.Profiles {
			if p.Name == name {
				machines = append(machines, p)

				break
			}
		}
	}

	return machines
}

// place finds the first eligible worker with room for a machine of this profile, and takes the
// room. It reports whether it found one.
func place(request Request, free map[string]map[string]uint64, chosen profile.Profile) bool {
	wanted := map[string]uint64{
		orchard.ResourceVMs:       1,
		orchard.ResourceCores:     chosen.CPU,
		orchard.ResourceMemoryMiB: chosen.MemoryMiB,
	}

	for _, worker := range request.Fleet.Workers {
		if !request.Eligible(worker, chosen.WorkerLabels) {
			continue
		}

		if !orchard.Fits(free[worker.Name], wanted) {
			continue
		}

		orchard.Take(free[worker.Name], wanted)

		return true
	}

	return false
}
