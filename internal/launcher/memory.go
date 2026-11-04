// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

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

	// scheduledAt is when Buildkite scheduled the job, and reservedAt is when this process first
	// reserved it. The phase durations of the job start from them.
	scheduledAt time.Time
	reservedAt  time.Time

	// tokenAt is when this process sent the token call that got the token of the job, on the
	// clock of the controller. The unplaced timeout of the machine of the job counts from it.
	tokenAt time.Time

	// refusedAt is when this process sent an extension that Buildkite answered with
	// not_reserved, and the zero time after a reservation that Buildkite granted.
	refusedAt time.Time
}

// memory holds what only this process knows about the jobs that it started.
//
// When a reservation ends is in neither Orchard nor Buildkite: the reaper extends a reservation
// before it lapses, and nothing else records that time. The memory also holds the profile of each
// machine, which the tags of the machine name too, and when the token call of each job left.
//
// A restart loses them, which the design allows. The reaper then extends every reservation at
// once, it reads the profile of each machine of the process before the restart from its tags, and
// it counts the unplaced timeout from the first listing of the machine.
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
	entry.refusedAt = time.Time{}
	m.jobs[id] = entry
}

// refused records that Buildkite refused an extension of the reservation of a job, which this
// process sent at a time. The job keeps the end that it had.
func (m *memory) refused(id job.ID, sent time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := m.jobs[id]
	entry.refusedAt = sent
	m.jobs[id] = entry
}

// refusal gives when this process sent an extension of the reservation of a job that Buildkite
// refused, and the zero time when it sent none since the last one that Buildkite granted.
func (m *memory) refusal(id job.ID) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.jobs[id].refusedAt
}

// admitted records when a job that admission started was scheduled, and when this process
// reserved it.
func (m *memory) admitted(id job.ID, scheduledAt, reservedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := m.jobs[id]
	entry.scheduledAt = scheduledAt
	entry.reservedAt = reservedAt
	m.jobs[id] = entry
}

// tokened records when this process sent the token call that got the tokens of jobs.
func (m *memory) tokened(ids []job.ID, sent time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, id := range ids {
		entry := m.jobs[id]
		entry.tokenAt = sent
		m.jobs[id] = entry
	}
}

// tokenSent gives when this process sent the token call of a job, and the zero time for a job
// that got no token from this process.
func (m *memory) tokenSent(id job.ID) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.jobs[id].tokenAt
}

// timeline gives what this process knows of the start of a job.
func (m *memory) timeline(id job.ID) (held, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, known := m.jobs[id]

	return entry, known && !entry.reservedAt.IsZero()
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

	beyond := map[job.ID]string{}

	for id, entry := range m.jobs {
		if owned[id] {
			continue
		}

		if entry.name != "" || entry.until.After(now) {
			beyond[id] = entry.name
		}
	}

	return beyond
}
