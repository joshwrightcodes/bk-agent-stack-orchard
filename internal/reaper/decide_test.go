// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package reaper_test

import (
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
)

var (
	now       = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	freshness = 60 * time.Second
	rules     = reaper.Rules{Freshness: freshness, RenewBefore: 120 * time.Second}
	limits    = profile.Timeouts{
		Unplaced:      2 * time.Minute,
		Boot:          15 * time.Minute,
		Acquire:       5 * time.Minute,
		MaxVMLifetime: 25 * time.Hour,
	}
)

const carried = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")

// machine gives a machine that Orchard created an hour ago and that the controller first listed
// and saw placed an hour ago, and saw running a minute ago, as a snapshot of a second ago shows it,
// whose job state the controller asked for a second ago, in this tick, and Buildkite named. A case
// changes what it needs.
func machine(state string, change ...func(*reaper.Machine)) reaper.Machine {
	m := reaper.Machine{
		VM: orchard.VM{
			Name:        "bk-macos-prod-" + string(carried),
			CreatedAt:   now.Add(-time.Hour),
			ScheduledAt: now.Add(-time.Hour),
			StartedAt:   now.Add(-time.Minute),
			Worker:      "mac-01",
			Status:      orchard.StatusRunning,
		},
		Job:           carried,
		SnapshotAt:    now.Add(-time.Second),
		ListedAt:      now.Add(-time.Hour),
		PlacedAt:      now.Add(-time.Hour),
		RunningAt:     now.Add(-time.Minute),
		State:         state,
		Named:         true,
		Observed:      true,
		Current:       true,
		ObservedAt:    now.Add(-time.Second),
		ReservedUntil: now.Add(4 * time.Minute),
		Timeouts:      limits,
	}

	for _, f := range change {
		f(&m)
	}

	return m
}

// The ways a case changes the machine. The age since Orchard created the machine (created) is on
// the clock of Orchard, which runs with the clock of the controller unless a case skews it. The
// other ages are those that the controller measures on its own clock: since a snapshot first
// listed the machine (listed), first showed it on a worker (placed), and first showed it running
// (started).
func status(s string) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.VM.Status = s }
}

func created(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.VM.CreatedAt = now.Add(-ago) }
}

func listed(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.ListedAt = now.Add(-ago) }
}

func placed(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.PlacedAt = now.Add(-ago) }
}

func started(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.RunningAt = now.Add(-ago) }
}

// tokened is a job whose token call this process sent some time ago. A job with no such call has
// the zero time, as every job after a restart has.
func tokened(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.TokenAt = now.Add(-ago) }
}

func unplaced(m *reaper.Machine) {
	m.VM.Worker = ""
	m.VM.ScheduledAt = time.Time{}
	m.PlacedAt = time.Time{}
}

// skewed gives the times that Orchard gave the machine, on an Orchard clock that runs ahead of the
// clock of the controller by the offset, or behind it for a negative offset.
func skewed(offset time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) {
		m.VM.CreatedAt = m.ListedAt.Add(offset)
		m.VM.ScheduledAt = m.PlacedAt.Add(offset)
		m.VM.StartedAt = m.RunningAt.Add(offset)
	}
}

// unknown is a job that the last answers, so many in a row, left out.
func unknown(answers int) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.Named = false; m.Absent = answers }
}

// frozen is a machine from a snapshot older than the freshness limit, as during an outage of the
// reads of Orchard.
func frozen(m *reaper.Machine) { m.SnapshotAt = now.Add(-2 * freshness) }

// earlier is a job state that the controller asked for before the last pass of the reaper.
func earlier(m *reaper.Machine) { m.Current = false }

// refused is a job whose last extension Buildkite refused with not_reserved, a time ago. The
// state of the case is one that the controller asked for a second ago.
func refused(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.RefusedAt = now.Add(-ago) }
}

func reservedFor(left time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.ReservedUntil = now.Add(left) }
}

// T-005. One case for each row of the table, a state that matches no row, a state that is not
// causally fresh, and a stale state. The first row that matches applies, so the cases that sit
// between two rows prove the order as well as the rows.
func TestTheDecisionTable(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		machine reaper.Machine
		row     int
		action  reaper.Action
		reason  string
	}{
		// Row 1.
		"a machine with no job is an orphan": {
			machine(""), 1, reaper.Delete, reaper.ReasonOrphan,
		},

		// Row 2, which reads only the machine.
		"a machine older than its lifetime goes": {
			machine("running", created(26*time.Hour)), 2, reaper.Delete, reaper.ReasonLifetime,
		},
		"the lifetime acts with no job state": {
			machine("", created(26*time.Hour), func(m *reaper.Machine) { m.Observed = false }),
			2, reaper.Delete, reaper.ReasonLifetime,
		},

		// Row 3, before every row that reads the state.
		"a state that was not read decides nothing": {
			machine("finished", func(m *reaper.Machine) { m.Observed = false }),
			3, reaper.Keep, "",
		},
		"a state asked for before the controller first listed the machine decides nothing": {
			machine("finished", func(m *reaper.Machine) {
				m.ListedAt = now.Add(-10 * time.Second)
				m.ObservedAt = now.Add(-11 * time.Second)
			}),
			3, reaper.Keep, "",
		},
		// The machine was made 4 s ago, after the state was asked for, and the clock of Orchard
		// runs 2 s behind the clock of the controller. On the clock of Orchard, the state looks
		// newer than the machine. The controller listed the machine after it asked.
		"a clock of Orchard that runs behind does not make an old state count": {
			machine("scheduled", created(6*time.Second), func(m *reaper.Machine) {
				m.ListedAt = now.Add(-3 * time.Second)
				m.ObservedAt = now.Add(-5 * time.Second)
			}),
			3, reaper.Keep, "",
		},
		"a stale state decides nothing": {
			machine("finished", func(m *reaper.Machine) { m.ObservedAt = now.Add(-2 * freshness) }),
			3, reaper.Keep, "",
		},

		// Row 4.
		"an ended job needs no machine": {
			machine("finished"), 4, reaper.Delete, reaper.ReasonJobTerminal,
		},
		"an ended job wins over a failed machine": {
			machine("canceled", status(orchard.StatusFailed)), 4, reaper.Delete,
			reaper.ReasonJobTerminal,
		},

		// Row 5.
		"a job that two answers in a row leave out goes": {
			machine("", unknown(2)), 5, reaper.Delete, reaper.ReasonJobUnknown,
		},
		"a job that one answer leaves out waits for the next answer": {
			machine("", unknown(1)), 5, reaper.Keep, "",
		},

		// Row 6.
		"a job that is scheduled again lost its reservation": {
			machine("scheduled"), 6, reaper.Delete, reaper.ReasonReservationLapsed,
		},
		"a lapsed reservation wins over a failed machine": {
			machine("scheduled", status(orchard.StatusFailed)), 6, reaper.Delete,
			reaper.ReasonReservationLapsed,
		},
		"a reserved job whose extension Buildkite refused lost its reservation": {
			machine("reserved", refused(10*time.Second)), 6, reaper.Delete,
			reaper.ReasonReservationLapsed,
		},
		"a refused extension wins over a failed machine and sends no finish": {
			machine("reserved", refused(10*time.Second), status(orchard.StatusFailed)),
			6, reaper.Delete, reaper.ReasonReservationLapsed,
		},
		"a refused extension wins over the timeouts that finish a job": {
			machine("reserved", refused(10*time.Second), started(6*time.Minute)),
			6, reaper.Delete, reaper.ReasonReservationLapsed,
		},
		"a refused extension waits for a state asked for after the refusal": {
			machine("reserved", refused(time.Millisecond), reservedFor(4*time.Minute)),
			13, reaper.Keep, "",
		},
		"a refused extension of a job that an agent acquired keeps the machine": {
			machine("running", refused(10*time.Second)), 8, reaper.Keep, "",
		},

		// Rows 7 and 8.
		"a failed machine of an acquired job goes with a line on the job page": {
			machine("running", status(orchard.StatusFailed)), 7, reaper.NotifyAndDelete,
			reaper.ReasonVMFailed,
		},
		"a failed machine of an acquired job waits for a fresh snapshot": {
			machine("running", status(orchard.StatusFailed), frozen), 7, reaper.Keep, "",
		},
		"a machine of an acquired job that runs stays": {
			machine("running"), 8, reaper.Keep, "",
		},

		// Row 9.
		"a failed machine of a reserved job fails the job": {
			machine("reserved", status(orchard.StatusFailed)), 9, reaper.FinishAndDelete,
			reaper.ReasonVMFailed,
		},
		"a failed machine wins over the timeouts": {
			machine("reserved", status(orchard.StatusFailed), unplaced, listed(time.Hour)),
			9, reaper.FinishAndDelete, reaper.ReasonVMFailed,
		},

		"a failed machine waits for a state of this tick before it fails the job": {
			machine("reserved", status(orchard.StatusFailed), earlier), 9, reaper.Keep, "",
		},

		"a failed machine waits for a fresh snapshot before it fails the job": {
			machine("reserved", status(orchard.StatusFailed), frozen), 9, reaper.Keep, "",
		},

		// Row 10.
		"a machine that no worker took in time goes and the job waits again": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(3*time.Minute)),
			10, reaper.Delete, reaper.ReasonUnplacedTimeout,
		},
		"an unplaced timeout on a stale snapshot extends the reservation instead": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(3*time.Minute),
				frozen, reservedFor(time.Minute)),
			13, reaper.Extend, "",
		},
		"a machine that waits for a worker exactly as long as the limit stays": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(2*time.Minute)),
			13, reaper.Keep, "",
		},
		"the unplaced limit counts from the token call and not from the first listing": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(time.Minute),
				tokened(3*time.Minute)),
			10, reaper.Delete, reaper.ReasonUnplacedTimeout,
		},
		"a machine inside the unplaced limit from the token call stays": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(10*time.Second),
				tokened(2*time.Minute)),
			13, reaper.Keep, "",
		},
		"the unplaced limit counts from the first listing without a token call": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(time.Minute)),
			13, reaper.Keep, "",
		},
		"a token call after the first listing belongs to a later launch": {
			machine("reserved", status(orchard.StatusPending), unplaced, listed(3*time.Minute),
				tokened(30*time.Second)),
			10, reaper.Delete, reaper.ReasonUnplacedTimeout,
		},

		// Row 11.
		"a placed machine that did not boot in time fails the job": {
			machine("reserved", status(orchard.StatusPending), placed(16*time.Minute)),
			11, reaper.FinishAndDelete, reaper.ReasonBootTimeout,
		},
		"a boot timeout waits for a state of this tick and extends meanwhile": {
			machine("reserved", status(orchard.StatusPending), placed(16*time.Minute), earlier,
				reservedFor(time.Minute)),
			13, reaper.Extend, "",
		},
		"a boot timeout on a stale snapshot extends the reservation instead": {
			machine("reserved", status(orchard.StatusPending), placed(16*time.Minute), frozen,
				reservedFor(time.Minute)),
			13, reaper.Extend, "",
		},
		"the boot limit counts from the placement and not from the first listing": {
			machine("reserved", status(orchard.StatusPending), listed(20*time.Minute),
				placed(time.Minute)),
			13, reaper.Keep, "",
		},

		// Row 12.
		"a running machine whose agent did not acquire in time fails the job": {
			machine("reserved", started(6*time.Minute)), 12, reaper.FinishAndDelete,
			reaper.ReasonAcquireTimeout,
		},
		"an acquire timeout waits for a state of this tick": {
			machine("reserved", started(6*time.Minute), earlier), 13, reaper.Keep, "",
		},
		"an acquire timeout on a stale snapshot extends the reservation instead": {
			machine("reserved", started(6*time.Minute), frozen, reservedFor(time.Minute)),
			13, reaper.Extend, "",
		},
		"the rows that need no phase act on a stale snapshot": {
			machine("finished", frozen), 4, reaper.Delete, reaper.ReasonJobTerminal,
		},
		"the lifetime acts on a stale snapshot": {
			machine("running", created(26*time.Hour), frozen), 2, reaper.Delete,
			reaper.ReasonLifetime,
		},
		"a running machine inside the acquire limit stays": {
			machine("reserved", started(4*time.Minute)), 13, reaper.Keep, "",
		},

		// Row 13.
		"a starting machine extends a reservation that ends soon": {
			machine("reserved", reservedFor(time.Minute)), 13, reaper.Extend, "",
		},
		"a starting machine extends a reservation whose end is not known": {
			machine("reserved", func(m *reaper.Machine) { m.ReservedUntil = time.Time{} }),
			13, reaper.Extend, "",
		},
		"a pending machine extends as a running one does": {
			machine("reserved", status(orchard.StatusPending), placed(time.Minute),
				reservedFor(time.Minute)),
			13, reaper.Extend, "",
		},
		"a reservation with time left is not extended yet": {
			machine("reserved", reservedFor(4*time.Minute)), 13, reaper.Keep, "",
		},

		// No row.
		"a state that the table does not name matches no row": {
			machine("blocked"), 0, reaper.Keep, "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if name == "a machine with no job is an orphan" {
				test.machine.Orphan = true
				test.machine.Job = ""
			}

			got := reaper.Decide(test.machine, now, rules)

			if got.Row != test.row || got.Action != test.action || got.Reason != test.reason {
				t.Errorf("Decide() = row %d, %s, %q; want row %d, %s, %q",
					got.Row, got.Action, got.Reason, test.row, test.action, test.reason)
			}
		})
	}
}

// Every call about a job that Buildkite does not know gives the same refusal, so the decision
// tells the executor to ask about it no more.
func TestAnUnknownJobIsIgnoredAfterwards(t *testing.T) {
	t.Parallel()

	got := reaper.Decide(machine("", unknown(2)), now, rules)
	if !got.Ignore {
		t.Error("Decide() for an unknown job does not mark it ignored")
	}

	if ended := reaper.Decide(machine("finished"), now, rules); ended.Ignore {
		t.Error("Decide() marks an ended job ignored, which is only for a job that Buildkite " +
			"does not know")
	}
}

// A job is failed only before an agent acquires it. After that the lost-agent procedure of
// Buildkite decides the result, so no state that an agent owns may give a finish.
func TestNoJobThatAnAgentOwnsIsFinished(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"assigned", "accepted", "running", "canceling", "timing_out"} {
		for _, vm := range []string{orchard.StatusPending, orchard.StatusRunning, orchard.StatusFailed} {
			got := reaper.Decide(machine(state, status(vm), started(time.Hour)), now, rules)
			if got.Action == reaper.FinishAndDelete {
				t.Errorf("Decide() finishes a job in %q on a %s machine, which an agent owns",
					state, vm)
			}
		}
	}
}

// The lifetime is a limit: a machine at exactly the lifetime stays. A machine without a creation
// time counts its age from the first listing of this process, and a machine with neither time has
// no age at all.
func TestTheLifetimeIsALimitAndNeedsATime(t *testing.T) {
	t.Parallel()

	if got := reaper.Decide(machine("running", created(25*time.Hour)), now, rules); got.Row == 2 {
		t.Errorf("Decide() = %+v, want no deletion at exactly the lifetime", got)
	}

	uncreated := func(m *reaper.Machine) { m.VM.CreatedAt = time.Time{} }

	old := machine("running", uncreated, listed(26*time.Hour))
	if got := reaper.Decide(old, now, rules); got.Row != 2 || got.Action != reaper.Delete {
		t.Errorf("Decide() = %+v, want row 2 from the first listing of a machine with no "+
			"creation time", got)
	}

	young := machine("running", uncreated, listed(time.Hour))
	if got := reaper.Decide(young, now, rules); got.Row == 2 {
		t.Errorf("Decide() = %+v, want no deletion inside the lifetime from the first listing", got)
	}

	undated := machine("running", uncreated, func(m *reaper.Machine) { m.ListedAt = time.Time{} })
	if got := reaper.Decide(undated, now, rules); got.Action != reaper.Keep {
		t.Errorf("Decide() = %+v, want no action for a machine with no age", got)
	}
}

// A restart does not extend the lifetime. The new process first lists a machine that Orchard
// created more than a lifetime ago, and the machine goes at once, whatever its job says.
func TestARestartDoesNotExtendTheLifetime(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"running", "reserved", ""} {
		restarted := machine(state, created(26*time.Hour), listed(time.Second),
			func(m *reaper.Machine) {
				m.PlacedAt = now.Add(-time.Second)
				m.RunningAt = now.Add(-time.Second)
				m.Observed = state != ""
			})

		got := reaper.Decide(restarted, now, rules)
		if got.Row != 2 || got.Action != reaper.Delete || got.Reason != reaper.ReasonLifetime {
			t.Errorf("job %q: Decide() = %+v, want row 2 on the first listing after a restart",
				state, got)
		}
	}
}

// The limits of a machine whose profile the controller knows are the limits of that profile.
func TestTimeoutsOfAKnownProfile(t *testing.T) {
	t.Parallel()

	long := profile.Profile{Name: "long", Timeouts: profile.Timeouts{MaxVMLifetime: 48 * time.Hour}}
	short := profile.Profile{Name: "short"}

	got := reaper.Timeouts("short", []profile.Profile{long, short}, limits)

	if got.MaxVMLifetime != limits.MaxVMLifetime {
		t.Errorf("the lifetime of the short profile is %s, want the default %s",
			got.MaxVMLifetime, limits.MaxVMLifetime)
	}
}

// After a restart the controller cannot name the profile of a machine that it did not start. A
// restart must never end a machine sooner than its own profile allows, so the limits are the
// largest of the profiles.
func TestTimeoutsOfAnUnknownProfileAreTheWidest(t *testing.T) {
	t.Parallel()

	long := profile.Profile{Name: "long", Timeouts: profile.Timeouts{MaxVMLifetime: 48 * time.Hour}}
	slow := profile.Profile{Name: "slow", Timeouts: profile.Timeouts{Boot: 40 * time.Minute}}

	got := reaper.Timeouts("", []profile.Profile{long, slow}, limits)

	if got.MaxVMLifetime != 48*time.Hour {
		t.Errorf("the lifetime is %s, want the 48h of the longest profile", got.MaxVMLifetime)
	}

	if got.Boot != 40*time.Minute {
		t.Errorf("the boot limit is %s, want the 40m of the slowest profile", got.Boot)
	}

	if got.Acquire != limits.Acquire {
		t.Errorf("the acquire limit is %s, want the default %s", got.Acquire, limits.Acquire)
	}
}

// The timeouts of rows 10 to 12 are on the clock of the controller. An Orchard clock that runs
// half a day behind makes no new machine old enough to end, and one that runs half a day ahead
// hides no timeout.
func TestNoStartTimeoutReadsTheClockOfOrchard(t *testing.T) {
	t.Parallel()

	for _, offset := range []time.Duration{-12 * time.Hour, 12 * time.Hour} {
		for name, test := range map[string]struct {
			machine reaper.Machine
			row     int
			action  reaper.Action
		}{
			"a new machine that waits for a worker": {
				machine("reserved", status(orchard.StatusPending), unplaced, listed(time.Minute),
					reservedFor(4*time.Minute), skewed(offset)),
				13, reaper.Keep,
			},
			"a machine that waited too long for a worker": {
				machine("reserved", status(orchard.StatusPending), unplaced, listed(3*time.Minute),
					skewed(offset)),
				10, reaper.Delete,
			},
			"a placed machine that boots": {
				machine("reserved", status(orchard.StatusPending), placed(time.Minute),
					reservedFor(4*time.Minute), skewed(offset)),
				13, reaper.Keep,
			},
			"a placed machine that did not boot in time": {
				machine("reserved", status(orchard.StatusPending), placed(16*time.Minute),
					skewed(offset)),
				11, reaper.FinishAndDelete,
			},
			"a running machine inside the acquire limit": {
				machine("reserved", started(time.Minute), reservedFor(4*time.Minute),
					skewed(offset)),
				13, reaper.Keep,
			},
			"a running machine past the acquire limit": {
				machine("reserved", started(6*time.Minute), skewed(offset)),
				12, reaper.FinishAndDelete,
			},
		} {
			got := reaper.Decide(test.machine, now, rules)
			if got.Row != test.row || got.Action != test.action {
				t.Errorf("%s, Orchard %s off: Decide() = row %d, %s; want row %d, %s", name, offset,
					got.Row, got.Action, test.row, test.action)
			}
		}
	}
}
