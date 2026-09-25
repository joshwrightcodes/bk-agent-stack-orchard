// Package reaper decides what happens to each machine of this controller, and carries the
// decision out.
//
// The decision is a table (target design, section 7.2). Each row states a condition on the
// machine and a condition on its job, and the first row that matches applies. The function that
// reads the table is pure, so a test of every row needs no API and no clock, and the executor is
// the only part that calls anything.
//
// Two rules from the design govern every row. A decision uses observed state only, and never a
// counter of this process, so it survives a restart. And a job state counts only when the
// controller observed it after the machine existed and not long ago: a state from before the
// machine describes a job that the machine never saw, and an old state describes a job that may
// have moved on.
package reaper

import (
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

// Action is what the reaper does to one machine.
type Action string

// The actions of the table.
const (
	// Keep leaves the machine alone.
	Keep Action = "keep"

	// Delete removes the machine from Orchard.
	Delete Action = "delete"
)

// The reasons that a deletion carries. They are the values of the deletion metric and of the
// decision record, so a person can count them.
const (
	ReasonLifetime    = "lifetime"
	ReasonJobTerminal = "job_terminal"
)

// terminal holds the Buildkite states from which a job never returns. A machine of such a job has
// nothing left to do.
var terminal = map[string]bool{
	"finished":  true,
	"canceled":  true,
	"expired":   true,
	"timed_out": true,
	"skipped":   true,
	"broken":    true,
}

// Machine is everything that the decision reads about one machine of this controller.
type Machine struct {
	// VM is the machine as Orchard holds it.
	VM orchard.VM

	// Job is the job that the machine carries.
	Job job.ID

	// State is the Buildkite state of the job, when Observed is true.
	State string

	// Observed reports that the controller has a state for the job. A state that the controller
	// could not read is not a state, and a decision must not read the empty text as one.
	Observed bool

	// ObservedAt is when the controller read the state.
	ObservedAt time.Time

	// Timeouts are the limits of the profile of the machine.
	Timeouts profile.Timeouts
}

// Decision is what the table gives for one machine.
type Decision struct {
	// Row is the row of the table that matched, and zero when none did.
	Row int

	// Action is what to do.
	Action Action

	// Reason is the value that a deletion carries, and empty text when nothing is deleted.
	Reason string
}

// Decide reads the table for one machine. The first row that matches applies.
//
// Freshness is how old a job state may be and still count.
func Decide(m Machine, now time.Time, freshness time.Duration) Decision {
	// Row 2. A machine that outlives every other limit goes, whatever its job says. The row comes
	// before the freshness rule on purpose: it reads only the machine, so a controller that
	// cannot reach Buildkite still removes a machine that nothing else will.
	//
	// A machine with no creation time has no age. Treating it as ancient would delete it at
	// once, and a machine of a job in progress is not worth that risk.
	if !m.VM.CreatedAt.IsZero() && now.Sub(m.VM.CreatedAt) > m.Timeouts.MaxVMLifetime {
		return Decision{Row: 2, Action: Delete, Reason: ReasonLifetime}
	}

	// Row 3. A job state that the controller did not read, that it read before the machine
	// existed, or that it read too long ago, decides nothing. The next refresh gives a better one.
	if !m.Observed || m.ObservedAt.Before(m.VM.CreatedAt) || now.Sub(m.ObservedAt) > freshness {
		return Decision{Row: 3, Action: Keep}
	}

	// Row 4. A job that ended needs no machine.
	if terminal[m.State] {
		return Decision{Row: 4, Action: Delete, Reason: ReasonJobTerminal}
	}

	// The table is not whole yet, so a state that matches no row here is expected: a machine
	// whose job runs is the common case. It keeps its machine.
	return Decision{Action: Keep}
}

// Timeouts gives the limits of a machine.
//
// A machine does not carry the name of its profile (target design, section 6.1), so after a
// restart the controller cannot name the profile of a machine that it did not start. The limits
// are then the largest of each limit across the profiles. A restart must never end a machine
// sooner than its own profile allows: a stray machine that lives an hour longer costs a slot,
// and a job that the controller ends early costs a build.
func Timeouts(name string, profiles []profile.Profile, defaults profile.Timeouts) profile.Timeouts {
	for _, p := range profiles {
		if p.Name == name {
			return p.EffectiveTimeouts(defaults)
		}
	}

	widest := defaults

	for _, p := range profiles {
		limits := p.EffectiveTimeouts(defaults)

		widest.Unplaced = max(widest.Unplaced, limits.Unplaced)
		widest.Boot = max(widest.Boot, limits.Boot)
		widest.Acquire = max(widest.Acquire, limits.Acquire)
		widest.MaxVMLifetime = max(widest.MaxVMLifetime, limits.MaxVMLifetime)
	}

	return widest
}
