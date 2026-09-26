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

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"

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

	// FinishAndDelete ends the job with a failure and a message, then removes the machine. Only a
	// job that no agent has acquired can end this way, because after that the lost-agent
	// procedure of Buildkite decides the result of the job (target design, section 7.1).
	FinishAndDelete Action = "finish_and_delete"

	// NotifyAndDelete writes the reason on the job page, then removes the machine. It is for a
	// job that an agent owns, which the controller must not end.
	NotifyAndDelete Action = "notify_and_delete"

	// Extend holds the reservation of the job for longer, while its machine starts.
	Extend Action = "extend"
)

// The reasons that a deletion carries. They are the values of the deletion metric and of the
// decision record, so a person can count them.
const (
	ReasonOrphan            = "orphan"
	ReasonLifetime          = "lifetime"
	ReasonJobTerminal       = "job_terminal"
	ReasonJobUnknown        = "job_unknown"
	ReasonReservationLapsed = "reservation_lapsed"
	ReasonVMFailed          = "vm_failed"
	ReasonUnplacedTimeout   = "unplaced_timeout"
	ReasonBootTimeout       = "boot_timeout"
	ReasonAcquireTimeout    = "acquire_timeout"
)

// The Buildkite states that the table reads.
const (
	stateScheduled = "scheduled"
	stateReserved  = "reserved"
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

	// Orphan reports a machine whose name has the prefix of this controller and no job
	// identifier. Nothing else will remove it.
	Orphan bool

	// Job is the job that the machine carries, and empty for an orphan.
	Job job.ID

	// State is the Buildkite state of the job, when Observed and Named are true.
	State string

	// Named reports that Buildkite named the job when the controller asked. A job that Buildkite
	// leaves out of its answer is unknown to it, which is a fact about the job and not a gap in
	// what the controller knows.
	Named bool

	// Observed reports that the controller has a state for the job. A state that the controller
	// could not read is not a state, and a decision must not read the empty text as one.
	Observed bool

	// ObservedAt is when the controller asked for the state.
	ObservedAt time.Time

	// ReservedUntil is when the reservation of the job ends, and the zero time when the
	// controller does not know, which is every job after a restart.
	ReservedUntil time.Time

	// Timeouts are the limits of the profile of the machine.
	Timeouts profile.Timeouts
}

// Rules are the limits of the table that hold for every machine.
type Rules struct {
	// Freshness is how old a job state may be and still count.
	Freshness time.Duration

	// RenewBefore is how long before its end a reservation is extended.
	RenewBefore time.Duration
}

// Decision is what the table gives for one machine.
type Decision struct {
	// Row is the row of the table that matched, and zero when none did.
	Row int

	// Action is what to do.
	Action Action

	// Reason is the value that a deletion carries, and empty text when nothing is deleted.
	Reason string

	// Ignore marks the job as one that the controller must not call about again, because every
	// call would give the same refusal.
	Ignore bool
}

// Decide reads the table of the target design, section 7.2, for one machine. The first row that
// matches applies.
func Decide(m Machine, now time.Time, rules Rules) Decision {
	// Row 1. A machine with the prefix of this controller and no job has no reason to exist, and
	// no other part of the controller will remove it.
	if m.Orphan {
		return Decision{Row: 1, Action: Delete, Reason: ReasonOrphan}
	}

	// Row 2. A machine that outlives every other limit goes, whatever its job says. The row comes
	// before the freshness rule on purpose: it reads only the machine, so a controller that
	// cannot reach Buildkite still removes a machine that nothing else will.
	if older(now, m.VM.CreatedAt, m.Timeouts.MaxVMLifetime) {
		return Decision{Row: 2, Action: Delete, Reason: ReasonLifetime}
	}

	// Row 3. A job state that the controller did not read, that it asked for before the machine
	// existed, or that it asked for too long ago, decides nothing. The next refresh gives a better
	// one, and every row below reads the state.
	if !m.Observed || m.ObservedAt.Before(m.VM.CreatedAt) || now.Sub(m.ObservedAt) > rules.Freshness {
		return Decision{Row: 3, Action: Keep}
	}

	// Row 4. A job that ended needs no machine.
	if terminal[m.State] {
		return Decision{Row: 4, Action: Delete, Reason: ReasonJobTerminal}
	}

	// Row 5. A job that Buildkite does not know gives the same answer to every call, so the
	// machine goes and the controller asks about the job no more.
	if !m.Named {
		return Decision{Row: 5, Action: Delete, Reason: ReasonJobUnknown, Ignore: true}
	}

	// Row 6. A job that is scheduled again lost its reservation, so another machine or another
	// stack may take it, and this machine can no longer acquire it.
	if m.State == stateScheduled {
		return Decision{Row: 6, Action: Delete, Reason: ReasonReservationLapsed}
	}

	failed := m.VM.Status == orchard.StatusFailed

	// Rows 7 and 8. An agent owns the job, so Buildkite decides its result. A machine that failed
	// goes with a line on the job page; one that runs is doing its work.
	if buildkite.Acquired(m.State) {
		if failed {
			return Decision{Row: 7, Action: NotifyAndDelete, Reason: ReasonVMFailed}
		}

		return Decision{Row: 8, Action: Keep}
	}

	// The rows below need a job that is reserved and that no agent has acquired yet.
	if m.State != stateReserved {
		return Decision{Action: Keep}
	}

	return reserved(m, now, rules, failed)
}

// reserved reads rows 9 to 13, for a machine whose job is reserved.
func reserved(m Machine, now time.Time, rules Rules, failed bool) Decision {
	// Row 9. The machine failed before any agent acquired the job, so the job fails too, and the
	// job page says why.
	if failed {
		return Decision{Row: 9, Action: FinishAndDelete, Reason: ReasonVMFailed}
	}

	pending := m.VM.Status == orchard.StatusPending

	// Row 10. No worker took the machine in time, which means that the fleet is full. The machine
	// goes and the reservation is left to lapse, so the job returns to the queue and admission
	// starts it again when there is room. The job does not fail.
	if pending && !m.VM.Placed() && older(now, m.VM.CreatedAt, m.Timeouts.Unplaced) {
		return Decision{Row: 10, Action: Delete, Reason: ReasonUnplacedTimeout}
	}

	// Row 11. A worker took the machine and it did not start in time. The limit counts from the
	// placement, because a machine that waited for a worker has not had its chance to boot yet.
	if pending && m.VM.Placed() && older(now, m.VM.ScheduledAt, m.Timeouts.Boot) {
		return Decision{Row: 11, Action: FinishAndDelete, Reason: ReasonBootTimeout}
	}

	running := m.VM.Status == orchard.StatusRunning

	// Row 12. The machine runs and no agent acquired the job in time. The limit counts from the
	// start of the machine process, which is when macOS begins to boot.
	if running && older(now, m.VM.StartedAt, m.Timeouts.Acquire) {
		return Decision{Row: 12, Action: FinishAndDelete, Reason: ReasonAcquireTimeout}
	}

	// Row 13. The machine is starting, so its job must stay held for it. The controller extends
	// the reservation before too little of it is left, and at once when it does not know how much
	// is left.
	if pending || running {
		if m.ReservedUntil.IsZero() || m.ReservedUntil.Sub(now) < rules.RenewBefore {
			return Decision{Row: 13, Action: Extend}
		}

		return Decision{Row: 13, Action: Keep}
	}

	return Decision{Action: Keep}
}

// older reports a time that lies more than a limit before now. A zero time has no age: treating it
// as ancient would end a machine at once, and a machine of a job in progress is not worth that
// risk.
func older(now, since time.Time, limit time.Duration) bool {
	return !since.IsZero() && now.Sub(since) > limit
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
