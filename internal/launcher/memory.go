package launcher

import (
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

// held is what this process knows about one job that it started.
type held struct {
	// until is when the reservation of the job ends.
	until time.Time

	// name is the profile that carries the job.
	name string

	// at is when this process last acted on the job. A snapshot of the fleet older than this
	// cannot say whether the machine exists, because the launch came after it.
	at time.Time
}

// memory holds what only this process knows about the jobs that it started.
//
// Two things are in neither Orchard nor Buildkite. The first is when a reservation ends: the
// reaper extends a reservation before it lapses, and nothing else records that time. The second
// is the profile of a machine: a machine carries the worker labels of its profile and nothing
// that names the profile itself.
//
// A restart loses both, which the design allows. The reaper then extends every reservation at
// once, and the machines of the process before the restart count under an unknown profile.
type memory struct {
	mu   sync.RWMutex
	jobs map[job.ID]held
}

func newMemory() *memory {
	return &memory{jobs: map[job.ID]held{}}
}

// reserved records when the reservation of a job ends.
func (m *memory) reserved(id job.ID, until, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := m.jobs[id]
	entry.until = until
	entry.at = at
	m.jobs[id] = entry
}

// started records the profile that carries a job.
func (m *memory) started(id job.ID, name string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := m.jobs[id]
	entry.name = name
	entry.at = at
	m.jobs[id] = entry
}

// profile gives the profile of a job, and empty text for a job that this process did not start.
func (m *memory) profile(id job.ID) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.jobs[id].name
}

// reservedUntil gives when the reservation of a job ends, for a job that this process reserved.
func (m *memory) reservedUntil(id job.ID) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, known := m.jobs[id]
	if !known || entry.until.IsZero() {
		return time.Time{}, false
	}

	return entry.until, true
}

// keep forgets the jobs whose machines are gone.
//
// Without this, one entry of every job that this controller ever started would stay for the life
// of the process. The machines of the fleet are the jobs that still matter: a job whose machine
// the reaper deleted needs neither its reservation nor its profile again.
//
// A snapshot older than the launch of a job says nothing about that job, because the machine did
// not exist when the snapshot was taken. Only a later snapshot can forget it.
func (m *memory) keep(fleet inventory.Snapshot) {
	owned := make(map[job.ID]bool, len(fleet.Owned))
	for _, machine := range fleet.Owned {
		owned[machine.Job] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, entry := range m.jobs {
		if !owned[id] && fleet.At.After(entry.at) {
			delete(m.jobs, id)
		}
	}
}

// heldBeyond gives the jobs that this process holds and that a snapshot does not show a machine
// for, each with the profile of the machine that it asked for, or empty text when it asked for
// none.
//
// A job is held while its reservation lasts, and while a machine that this process asked for has
// not appeared in a snapshot. Either way the job is this process's own, and the inventory has not
// caught up with it yet.
func (m *memory) heldBeyond(fleet inventory.Snapshot, now time.Time) map[job.ID]string {
	owned := make(map[job.ID]bool, len(fleet.Owned))
	for _, machine := range fleet.Owned {
		owned[machine.Job] = true
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	held := map[job.ID]string{}

	for id, entry := range m.jobs {
		if owned[id] {
			continue
		}

		if entry.name != "" || entry.until.After(now) {
			held[id] = entry.name
		}
	}

	return held
}
