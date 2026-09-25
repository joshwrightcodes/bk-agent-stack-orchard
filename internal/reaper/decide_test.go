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
	limits    = profile.Timeouts{
		Unplaced:      2 * time.Minute,
		Boot:          15 * time.Minute,
		Acquire:       5 * time.Minute,
		MaxVMLifetime: 25 * time.Hour,
	}
)

const carried = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")

// machine gives a machine created some time ago, whose job state the controller read a moment ago.
func machine(age time.Duration, state string) reaper.Machine {
	return reaper.Machine{
		VM: orchard.VM{
			Name:      "bk-macos-prod-" + string(carried),
			CreatedAt: now.Add(-age),
			Worker:    "mac-01",
			Status:    orchard.StatusRunning,
		},
		Job:        carried,
		State:      state,
		Observed:   true,
		ObservedAt: now.Add(-time.Second),
		Timeouts:   limits,
	}
}

// Row 2. A machine that outlives every other limit goes, whatever its job says. Nothing else
// removes it, and it holds a slot of the fleet for as long as it stays.
func TestRow2DeletesAMachineThatOutlivesItsLimit(t *testing.T) {
	t.Parallel()

	got := reaper.Decide(machine(26*time.Hour, "running"), now, freshness)

	if got.Row != 2 || got.Action != reaper.Delete || got.Reason != reaper.ReasonLifetime {
		t.Errorf("Decide() = %+v, want row 2, delete, %q", got, reaper.ReasonLifetime)
	}
}

// Row 2 reads only the machine, so it acts when the controller cannot reach Buildkite. That is
// the reason that it comes before the freshness rule.
func TestRow2ActsWithoutAJobState(t *testing.T) {
	t.Parallel()

	old := machine(26*time.Hour, "")
	old.Observed = false

	if got := reaper.Decide(old, now, freshness); got.Action != reaper.Delete {
		t.Errorf("Decide() = %+v, want a deletion with no job state", got)
	}
}

// The limit is a limit: a machine at exactly the lifetime stays.
func TestRow2WaitsForTheWholeLifetime(t *testing.T) {
	t.Parallel()

	if got := reaper.Decide(machine(25*time.Hour, "running"), now, freshness); got.Row == 2 {
		t.Errorf("Decide() = %+v, want no deletion at exactly the lifetime", got)
	}
}

// A machine with no creation time has no age. Treating it as ancient would delete a machine of a
// job in progress at once.
func TestRow2LeavesAMachineWithNoCreationTime(t *testing.T) {
	t.Parallel()

	undated := machine(0, "running")
	undated.VM.CreatedAt = time.Time{}

	if got := reaper.Decide(undated, now, freshness); got.Action == reaper.Delete {
		t.Errorf("Decide() = %+v, want no deletion of a machine with no age", got)
	}
}

// Row 4. A job that ended needs no machine.
func TestRow4DeletesTheMachineOfEveryTerminalState(t *testing.T) {
	t.Parallel()

	for _, state := range []string{
		"finished", "canceled", "expired", "timed_out", "skipped", "broken",
	} {
		got := reaper.Decide(machine(time.Hour, state), now, freshness)

		if got.Row != 4 || got.Action != reaper.Delete || got.Reason != reaper.ReasonJobTerminal {
			t.Errorf("Decide() with %q = %+v, want row 4, delete, %q",
				state, got, reaper.ReasonJobTerminal)
		}
	}
}

// A job in progress keeps its machine. The rows that act on these states are not in the table
// yet, and a state that matches no row keeps the machine.
func TestAJobInProgressKeepsItsMachine(t *testing.T) {
	t.Parallel()

	for _, state := range []string{
		"scheduled", "reserved", "assigned", "accepted", "running", "canceling", "timing_out",
	} {
		if got := reaper.Decide(machine(time.Hour, state), now, freshness); got.Action != reaper.Keep {
			t.Errorf("Decide() with %q = %+v, want the machine kept", state, got)
		}
	}
}

// Row 3. A state that the controller could not read decides nothing. The empty text of a failed
// call must not read as a state.
func TestRow3IgnoresAStateThatWasNotRead(t *testing.T) {
	t.Parallel()

	unread := machine(time.Hour, "finished")
	unread.Observed = false

	if got := reaper.Decide(unread, now, freshness); got.Row != 3 || got.Action != reaper.Keep {
		t.Errorf("Decide() = %+v, want row 3 and the machine kept", got)
	}
}

// Row 3. A state read before the machine existed describes a job that the machine never saw.
func TestRow3IgnoresAStateFromBeforeTheMachine(t *testing.T) {
	t.Parallel()

	// The state is fresh, and it is older than the machine. Only the order of the two may decide.
	early := machine(10*time.Second, "finished")
	early.ObservedAt = early.VM.CreatedAt.Add(-time.Second)

	if got := reaper.Decide(early, now, freshness); got.Row != 3 || got.Action != reaper.Keep {
		t.Errorf("Decide() = %+v, want row 3 and the machine kept", got)
	}
}

// Row 3. An old state describes a job that may have moved on, so a terminal state that is too old
// must not delete the machine of a job that runs.
func TestRow3IgnoresAStateThatIsTooOld(t *testing.T) {
	t.Parallel()

	stale := machine(time.Hour, "finished")
	stale.ObservedAt = now.Add(-2 * freshness)

	if got := reaper.Decide(stale, now, freshness); got.Row != 3 || got.Action != reaper.Keep {
		t.Errorf("Decide() = %+v, want row 3 and the machine kept", got)
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
