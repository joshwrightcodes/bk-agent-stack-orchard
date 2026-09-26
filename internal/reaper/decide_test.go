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

// machine gives a machine created an hour ago that runs on a worker, whose job state the
// controller asked for a second ago and Buildkite named. A case changes what it needs.
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
		State:         state,
		Named:         true,
		Observed:      true,
		ObservedAt:    now.Add(-time.Second),
		ReservedUntil: now.Add(4 * time.Minute),
		Timeouts:      limits,
	}

	for _, f := range change {
		f(&m)
	}

	return m
}

// The ways a case changes the machine.
func status(s string) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.VM.Status = s }
}

func created(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.VM.CreatedAt = now.Add(-ago) }
}

func placed(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.VM.ScheduledAt = now.Add(-ago) }
}

func started(ago time.Duration) func(*reaper.Machine) {
	return func(m *reaper.Machine) { m.VM.StartedAt = now.Add(-ago) }
}

func unplaced(m *reaper.Machine) { m.VM.Worker = ""; m.VM.ScheduledAt = time.Time{} }

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
		"a state asked for before the machine existed decides nothing": {
			machine("finished", created(10*time.Second), func(m *reaper.Machine) {
				m.ObservedAt = now.Add(-11 * time.Second)
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
		"a job that Buildkite does not name goes": {
			machine("", func(m *reaper.Machine) { m.Named = false }),
			5, reaper.Delete, reaper.ReasonJobUnknown,
		},

		// Row 6.
		"a job that is scheduled again lost its reservation": {
			machine("scheduled"), 6, reaper.Delete, reaper.ReasonReservationLapsed,
		},
		"a lapsed reservation wins over a failed machine": {
			machine("scheduled", status(orchard.StatusFailed)), 6, reaper.Delete,
			reaper.ReasonReservationLapsed,
		},

		// Rows 7 and 8.
		"a failed machine of an acquired job goes with a line on the job page": {
			machine("running", status(orchard.StatusFailed)), 7, reaper.NotifyAndDelete,
			reaper.ReasonVMFailed,
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
			machine("reserved", status(orchard.StatusFailed), unplaced, created(time.Hour)),
			9, reaper.FinishAndDelete, reaper.ReasonVMFailed,
		},

		// Row 10.
		"a machine that no worker took in time goes and the job waits again": {
			machine("reserved", status(orchard.StatusPending), unplaced, created(3*time.Minute)),
			10, reaper.Delete, reaper.ReasonUnplacedTimeout,
		},
		"a machine that waits for a worker exactly as long as the limit stays": {
			machine("reserved", status(orchard.StatusPending), unplaced, created(2*time.Minute)),
			13, reaper.Keep, "",
		},

		// Row 11.
		"a placed machine that did not boot in time fails the job": {
			machine("reserved", status(orchard.StatusPending), placed(16*time.Minute)),
			11, reaper.FinishAndDelete, reaper.ReasonBootTimeout,
		},
		"the boot limit counts from the placement and not from the creation": {
			machine("reserved", status(orchard.StatusPending), created(20*time.Minute),
				placed(time.Minute)),
			13, reaper.Keep, "",
		},

		// Row 12.
		"a running machine whose agent did not acquire in time fails the job": {
			machine("reserved", started(6*time.Minute)), 12, reaper.FinishAndDelete,
			reaper.ReasonAcquireTimeout,
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

	got := reaper.Decide(machine("", func(m *reaper.Machine) { m.Named = false }), now, rules)
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

// The lifetime is a limit: a machine at exactly the lifetime stays, and a machine with no creation
// time has no age at all.
func TestTheLifetimeIsALimitAndNeedsATime(t *testing.T) {
	t.Parallel()

	if got := reaper.Decide(machine("running", created(25*time.Hour)), now, rules); got.Row == 2 {
		t.Errorf("Decide() = %+v, want no deletion at exactly the lifetime", got)
	}

	undated := machine("running", func(m *reaper.Machine) { m.VM.CreatedAt = time.Time{} })
	if got := reaper.Decide(undated, now, rules); got.Action != reaper.Keep {
		t.Errorf("Decide() = %+v, want no action for a machine with no age", got)
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
