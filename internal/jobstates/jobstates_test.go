package jobstates_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const (
	first  = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a70")
	second = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a71")
)

// clock is a time that a test moves.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

// buildkite answers get-states, and moves the clock while it answers, as a call over a network
// takes time.
type buildkite struct {
	clock  *clock
	states map[job.ID]string
	err    error
	calls  [][]job.ID
}

func (b *buildkite) States(_ context.Context, ids []job.ID) (map[job.ID]string, error) {
	b.calls = append(b.calls, ids)
	b.clock.at = b.clock.at.Add(300 * time.Millisecond)

	return b.states, b.err
}

// fleet gives the machines of this controller.
type fleet struct {
	snapshot inventory.Snapshot
	read     bool
}

func (f *fleet) Latest() (inventory.Snapshot, bool) { return f.snapshot, f.read }

func owning(ids ...job.ID) *fleet {
	snapshot := inventory.Snapshot{}
	for _, id := range ids {
		snapshot.Owned = append(snapshot.Owned, inventory.Owned{Job: id})
	}

	return &fleet{snapshot: snapshot, read: true}
}

func build(t *testing.T, api *buildkite, machines *fleet, c *clock) *jobstates.States {
	t.Helper()

	states, err := jobstates.New(jobstates.Options{
		Stacks: api, Fleet: machines, Interval: 5 * time.Second, Now: c.now,
	})
	if err != nil {
		t.Fatalf("New() = %v, want the states", err)
	}

	return states
}

var start = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

// One call for every job, because one call for each machine would spend the rate limit of the
// whole organisation on a busy fleet.
func TestOneCallAsksAboutEveryJobThatHasAMachine(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c, states: map[job.ID]string{first: "running", second: "finished"}}

	states := build(t, api, owning(first, second), c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if len(api.calls) != 1 || len(api.calls[0]) != 2 {
		t.Fatalf("the states asked %v, want one call of two jobs", api.calls)
	}

	if got, _ := states.Observed(second); got.State != "finished" || !got.Named {
		t.Errorf("Observed() = %+v, want the state that Buildkite named", got)
	}
}

// The observation time is when the request left, which is the earliest moment that the answer can
// describe. A time taken from the answer would make a state look newer than it is, and both rules
// that read the time would then trust it sooner than they should.
func TestTheObservationTimeIsWhenTheRequestLeft(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c, states: map[job.ID]string{first: "running"}}

	states := build(t, api, owning(first), c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	got, _ := states.Observed(first)
	if !got.At.Equal(start) {
		t.Errorf("the observation is from %s, want %s, when the request left and not when the "+
			"answer came back", got.At, start)
	}

	if !states.At().Equal(start) {
		t.Errorf("At() = %s, want %s", states.At(), start)
	}
}

// A job that Buildkite leaves out of its answer is unknown to Buildkite. That is a fact about the
// job, and it is not the same as a job that the controller did not ask about.
func TestAJobThatBuildkiteLeavesOutIsAskedAndNotNamed(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c, states: map[job.ID]string{first: "running"}}

	states := build(t, api, owning(first, second), c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	got, asked := states.Observed(second)
	if !asked {
		t.Fatal("Observed() reports no question about a job that the controller asked about")
	}

	if got.Named || got.State != "" {
		t.Errorf("Observed() = %+v, want a job that Buildkite did not name", got)
	}
}

// A call that fails changes nothing. The states before it keep their times, so they grow old and
// then stale, and the reaper stops acting on them by its own rules. A failed call must never read
// as an answer that names no job, because every job would then look unknown to Buildkite.
func TestAFailedCallKeepsTheStatesBeforeItAndTheirTimes(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c, states: map[job.ID]string{first: "running"}}

	states := build(t, api, owning(first), c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	c.at = start.Add(10 * time.Second)
	api.err = errors.New("buildkite is not available")

	if err := states.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh() = nil, want the error of Buildkite")
	}

	got, asked := states.Observed(first)
	if !asked || !got.Named || got.State != "running" {
		t.Fatalf("Observed() = %+v, %v, want the state from before the failure", got, asked)
	}

	if !got.At.Equal(start) {
		t.Errorf("the state is from %s, want its own time %s: a failure must not make an old "+
			"state look new", got.At, start)
	}

	if !states.At().Equal(start) {
		t.Errorf("At() = %s, want the time of the last answer, %s", states.At(), start)
	}
}

// A job whose machine is gone needs no state. Keeping it would grow the map for the life of the
// process.
func TestAJobWhoseMachineIsGoneIsForgotten(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c, states: map[job.ID]string{first: "running", second: "running"}}
	machines := owning(first, second)

	states := build(t, api, machines, c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	machines.snapshot = owning(first).snapshot
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if _, asked := states.Observed(second); asked {
		t.Error("Observed() still has a state for a job whose machine is gone")
	}
}

// Before the controller has read the fleet it knows of no job, and it asks about none.
func TestNothingIsAskedBeforeTheFleetIsRead(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c}

	states := build(t, api, &fleet{read: false}, c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if len(api.calls) != 0 {
		t.Errorf("the states asked %v before the fleet was read, want nothing", api.calls)
	}
}

// A fleet with no machine of this controller has nothing to ask about, and the answer to that is
// fresh: no call is needed to know it.
func TestAFleetWithNoMachineAsksNothingAndIsFresh(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	api := &buildkite{clock: c}

	states := build(t, api, owning(), c)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if len(api.calls) != 0 {
		t.Errorf("the states asked %v about no job, want no call", api.calls)
	}

	if !states.At().Equal(start) {
		t.Errorf("At() = %s, want %s", states.At(), start)
	}
}

func TestTheStatesAreTheJobStatesSource(t *testing.T) {
	t.Parallel()

	c := &clock{at: start}
	states := build(t, &buildkite{clock: c}, owning(), c)

	if states.Source() != telemetry.SourceJobStates || states.Interval() != 5*time.Second {
		t.Errorf("the source is %q every %s, want %q every 5s",
			states.Source(), states.Interval(), telemetry.SourceJobStates)
	}

	if !states.At().IsZero() {
		t.Errorf("At() = %s before the first answer, want the zero time", states.At())
	}
}

func TestNewRefusesIncompleteStates(t *testing.T) {
	t.Parallel()

	// The clock is one of the parts: a states without one would read the clock of the machine.
	if _, err := jobstates.New(jobstates.Options{}); err == nil || !strings.Contains(err.Error(), "needs a clock") {
		t.Errorf("New() with nothing = %v, want an error that names the clock", err)
	}
}
