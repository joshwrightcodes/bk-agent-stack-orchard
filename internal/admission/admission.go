// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

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
	"errors"
	"maps"
	"math"
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
// leaves it alone, because another stack or agent of the queue may carry it. It writes the
// summary on the job page, and the reason in the log.
type Unmatched struct {
	Job buildkite.ScheduledJob

	// Mismatch says why no profile carries the job, and is a *match.Mismatch when the matcher
	// gave one. The job stays in the queue, and meets admission at each tick, so the decision
	// keeps the error and writes no text: Reason and Summary write it out when a part needs it,
	// which is once for each job.
	Mismatch error
}

// Reason gives the full message, with the tags of the job and every profile.
func (u Unmatched) Reason() string {
	if u.Mismatch == nil {
		return ""
	}

	return u.Mismatch.Error()
}

// Summary gives the short line for the job page.
func (u Unmatched) Summary() string {
	var mismatch *match.Mismatch
	if errors.As(u.Mismatch, &mismatch) {
		return mismatch.Summary()
	}

	return u.Reason()
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

	// Most is the most jobs that one decision admits, which is what the launcher can start in
	// one tick. Zero means no limit. A job past it waits for the next tick.
	Most int

	// Truncated reports that the read of the queue stopped at the page cap. The decision passes
	// it on, because the launcher forgets a notified job only after a whole listing.
	Truncated bool

	// Now is the time that the ages are measured from.
	Now time.Time
}

// Result is the decision.
type Result struct {
	// Admit are the jobs to start, in the order to start them.
	Admit []Candidate

	// Unmatched are the jobs to leave alone and to say so once.
	Unmatched []Unmatched

	// Truncated is the flag of the request: the listing that this decision read was cut.
	Truncated bool

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

// Waiting gives the jobs of the queue that wait, and the jobs that no profile carries, and admits
// none. The loop reads it at a tick whose gate of admission is closed, so that the demand that the
// fleet faces stays visible while no job starts.
func Waiting(request Request) Result {
	result := Result{
		Truncated:     request.Truncated,
		Waiting:       map[string]int64{},
		OldestWaiting: map[string]time.Duration{},
	}

	result.Matched = sift(request, &result)

	return result
}

// Slots gives the open slots of each pool, for the online hosts, as admission counts them: the
// slots that a machine waiting for a host or a machine that this process asked for will take are
// reserved, the slots of a host that no profile may use are unavailable, and the rest are free. A
// host is one that no profile may use when an operator paused it, or when no profile has its
// labels.
//
// The slots are the machine slots of a host. A free slot also needs the cores and the memory of a
// profile, which a count of slots does not show.
func Slots(request Request, online func(orchard.Worker) bool) map[telemetry.Slot]int64 {
	slots := map[telemetry.Slot]int64{}
	room := freeResources(request)

	for _, worker := range request.Fleet.Workers {
		if !online(worker) {
			continue
		}

		pool := inventory.PoolOf(worker)
		open := orchard.Free(worker, request.Fleet.Carried(worker.Name))[orchard.ResourceVMs]
		left := min(room[worker.Name][orchard.ResourceVMs], open)

		reserved := telemetry.Slot{Pool: pool, State: telemetry.SlotsReserved}
		unavailable := telemetry.Slot{Pool: pool, State: telemetry.SlotsUnavailable}
		free := telemetry.Slot{Pool: pool, State: telemetry.SlotsFree}

		// Each pool has all three series, so that a pool with nothing open reads 0.
		slots[reserved] += 0
		slots[free] += 0

		if !usable(request, worker) {
			slots[unavailable] += count(open)

			continue
		}

		slots[unavailable] += 0
		slots[reserved] += count(open - left)
		slots[free] += count(left)
	}

	return slots
}

// usable reports a host that admission may give a machine of at least one profile.
func usable(request Request, worker orchard.Worker) bool {
	for _, p := range request.Profiles {
		if request.Eligible(worker, p.WorkerLabels) {
			return true
		}
	}

	return false
}

// count turns a number of slots into a number that an instrument carries.
func count(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}

	return int64(value)
}

// Decide gives the jobs to start and the jobs to end.
func Decide(request Request) Result {
	result := Result{
		Truncated:     request.Truncated,
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
		if running >= limit || (request.Most > 0 && len(result.Admit) >= request.Most) {
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

	// One set for the decision, and not one search of the machines for each job of the queue.
	owned := make(map[job.ID]bool, len(request.Fleet.Owned))
	for _, machine := range request.Fleet.Owned {
		owned[machine.Job] = true
	}

	for _, waiting := range request.Jobs {
		chosen, err := match.Match(waiting.Rules, request.Profiles)
		if err != nil {
			result.Unmatched = append(result.Unmatched, Unmatched{Job: waiting, Mismatch: err})

			continue
		}

		// A job that already has a machine is running, or about to. A second machine for it
		// would be a second agent for one job.
		if owned[waiting.ID] {
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
// not show yet, in the order of their jobs.
//
// Each machine takes its room on the first worker with room, so the order decides what is left.
// The order of a map changes from one read to the next, and the decision must be a pure function
// of what the controller observed (target design, section 12). Held does not carry the order of
// the creates, and the order of the job identifiers is fixed.
func requested(request Request) []profile.Profile {
	var machines []profile.Profile

	for _, id := range slices.Sorted(maps.Keys(request.Held)) {
		name := request.Held[id]
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
