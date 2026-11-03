// SPDX-License-Identifier: AGPL-3.0-or-later

package reaper_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const (
	ended   = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a70")
	running = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a71")
	ancient = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a72")
)

// observed holds what the job states say. A state is one that the controller asked for a
// second ago, and Buildkite named it. A job that the map leaves out is one that the last two
// answers left out. A nil map is a controller that has asked about nothing.
type observed struct {
	states map[job.ID]string
	reads  int
}

func (b *observed) Observed(id job.ID) (jobstates.Observation, bool) {
	b.reads++

	if b.states == nil {
		return jobstates.Observation{}, false
	}

	state, named := b.states[id]

	observation := jobstates.Observation{State: state, Named: named, At: now.Add(-time.Second)}
	if !named {
		observation.Absent = 2
	}

	return observation, true
}

// pages answers finish and notify as Buildkite, and records what the job pages received.
type pages struct {
	mu        sync.Mutex
	finished  []job.ID
	details   []string
	notified  []buildkite.Notification
	requests  []int
	finishErr error
	notifyErr error
}

func (p *pages) Finish(_ context.Context, id job.ID, _ int, detail string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.finished = append(p.finished, id)
	p.details = append(p.details, detail)

	return p.finishErr
}

// Notify records the lines, and the number of lines of each request.
func (p *pages) Notify(_ context.Context, lines []buildkite.Notification) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.notified = append(p.notified, lines...)
	p.requests = append(p.requests, len(lines))

	return p.notifyErr
}

// held answers as the launcher: when each reservation ends, and the calls that extend them.
type held struct {
	until     map[job.ID]time.Time
	calls     [][]job.ID
	extendErr error
}

func (h *held) ReservedUntil(id job.ID) (time.Time, bool) {
	until, known := h.until[id]

	return until, known
}

func (h *held) Extend(_ context.Context, ids []job.ID) error {
	if len(ids) > 0 {
		h.calls = append(h.calls, ids)
	}

	return h.extendErr
}

// fleet answers as Orchard: it holds the snapshot, and it records the deletions.
type fleet struct {
	mu        sync.Mutex
	snapshot  inventory.Snapshot
	read      bool
	refuse    map[string]bool
	deleted   []string
	forgot    []string
	events    []orchard.Event
	eventsErr error
}

func (f *fleet) Events(context.Context, string) ([]orchard.Event, error) {
	return f.events, f.eventsErr
}

func (f *fleet) Latest() (inventory.Snapshot, bool) { return f.snapshot, f.read }

func (f *fleet) Forget(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.forgot = append(f.forgot, name)
}

// DeleteVM records the deletion. The reaper deletes several machines at the same time.
func (f *fleet) DeleteVM(_ context.Context, name string) error {
	if f.refuse[name] {
		return errors.New("orchard refused the deletion")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, name)

	return nil
}

// decisions keeps the records that the reaper writes at info level and above.
type decisions struct {
	mu      sync.Mutex
	records []slog.Record
}

func (d *decisions) Enabled(context.Context, slog.Level) bool { return true }
func (d *decisions) WithAttrs([]slog.Attr) slog.Handler       { return d }
func (d *decisions) WithGroup(string) slog.Handler            { return d }

func (d *decisions) Handle(_ context.Context, record slog.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if record.Level >= slog.LevelInfo {
		d.records = append(d.records, record.Clone())
	}

	return nil
}

func (d *decisions) messages() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]string, 0, len(d.records))
	for _, record := range d.records {
		out = append(out, record.Message)
	}

	return out
}

// owned gives a machine of this controller for a job, created some time ago.
func owned(id job.ID, age time.Duration) inventory.Owned {
	return inventory.Owned{
		Job: id,
		VM: orchard.VM{
			Name:      "bk-macos-prod-" + string(id),
			CreatedAt: now.Add(-age),
			Worker:    "mac-01",
			Status:    orchard.StatusRunning,
		},
	}
}

func build(t *testing.T, api *observed, machines *fleet, log *decisions) *reaper.Reaper {
	t.Helper()

	return buildWith(t, api, machines, log, &pages{})
}

func buildWith(t *testing.T, api *observed, machines *fleet, log *decisions,
	jobPages *pages,
) *reaper.Reaper {
	t.Helper()

	return buildHolding(t, api, machines, log, jobPages, &held{})
}

func buildHolding(t *testing.T, api *observed, machines *fleet, log *decisions,
	jobPages *pages, reservations *held,
) *reaper.Reaper {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	built, err := reaper.New(reaper.Options{
		Stacks:       jobPages,
		States:       api,
		Reservations: reservations,
		RenewBefore:  120 * time.Second,
		CallTimeout:  time.Second,
		Orchard:      machines,
		Fleet:        machines,
		Profiles:     []profile.Profile{{Name: "xcode-27"}},
		Defaults:     limits,
		Freshness:    freshness,
		Metrics:      instruments,
		Logger:       slog.New(log),
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the reaper", err)
	}

	return built
}

// The walking skeleton ends here: a job finished, and its machine goes.
func TestTheMachineOfAFinishedJobGoesAndTheOthersStay(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "finished", running: "running"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Hour), owned(running, time.Hour)},
	}}
	log := &decisions{}
	jobPages := &pages{}

	if err := buildWith(t, api, machines, log, jobPages).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 || machines.deleted[0] != "bk-macos-prod-"+string(ended) {
		t.Errorf("orchard deleted %v, want only the machine of the finished job", machines.deleted)
	}

	// The last step on the job page (target design, section 10.5).
	if len(jobPages.notified) != 1 || jobPages.notified[0].Detail != "VM deleted: job_terminal" ||
		jobPages.notified[0].JobID != ended {
		t.Errorf("the job pages got %+v, want the removal on the page of the finished job",
			jobPages.notified)
	}

	// The design asks for exactly one decision record for each action.
	if got := log.messages(); len(got) != 1 {
		t.Errorf("the reaper wrote %v, want one decision record", got)
	}
}

// A controller that cannot reach Buildkite still removes a machine that outlived its limit, and
// deletes nothing on a state that it could not read.
func TestWithoutStatesOnlyTheLifetimeActs(t *testing.T) {
	t.Parallel()

	api := &observed{}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Hour), owned(ancient, 26*time.Hour)},
	}}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 || machines.deleted[0] != "bk-macos-prod-"+string(ancient) {
		t.Errorf("orchard deleted %v, want only the machine that outlived its limit",
			machines.deleted)
	}
}

// A job that Buildkite does not name is unknown to it, and its machine goes (row 5). A job that
// the controller has not asked about is a different thing, and the test above keeps its machine.
func TestAJobThatBuildkiteDoesNotNameLosesItsMachine(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(running, time.Hour)},
	}}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine of the job that Buildkite does not know",
			machines.deleted)
	}
}

// Row 5 with the job states that the controller runs. One answer that leaves a job out, beside a
// job that it names, can be a fault of the answer, so the machine stays. A second answer in a row
// that leaves the job out makes it unknown to Buildkite, and its machine goes.
func TestAJobIsUnknownOnlyAfterTwoAnswersInARowLeaveItOut(t *testing.T) {
	t.Parallel()

	c := &clock{at: now}
	api := &stacks{states: map[job.ID]string{running: "running"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(running, time.Hour), owned(ended, time.Hour)},
		At:    now.Add(-time.Second),
	}}

	states, reaping := joined(t, api, machines, c)

	for answer := 1; answer <= 2; answer++ {
		c.at = c.at.Add(5 * time.Second)

		if err := states.Refresh(t.Context()); err != nil {
			t.Fatalf("Refresh() = %v, want no error", err)
		}

		if err := reaping.Reap(t.Context()); err != nil {
			t.Fatalf("Reap() = %v, want no error", err)
		}

		if want := answer - 1; len(machines.deleted) != want {
			t.Fatalf("after answer %d orchard deleted %v, want %d machines", answer,
				machines.deleted, want)
		}
	}

	if machines.deleted[0] != "bk-macos-prod-"+string(ended) {
		t.Errorf("orchard deleted %v, want the machine of the job that Buildkite left out",
			machines.deleted)
	}
}

// One machine that Orchard will not delete must not stop the others. The next tick decides the
// same way and tries again, so nothing needs to remember the failure.
func TestADeletionThatFailsDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "finished", running: "finished"}}
	machines := &fleet{
		read:   true,
		refuse: map[string]bool{"bk-macos-prod-" + string(ended): true},
		snapshot: inventory.Snapshot{
			Owned: []inventory.Owned{owned(ended, time.Hour), owned(running, time.Hour)},
		},
	}

	err := build(t, api, machines, &decisions{}).Reap(t.Context())
	if err == nil {
		t.Error("Reap() = nil, want the failure to reach the tick")
	}

	if !slices.Contains(machines.deleted, "bk-macos-prod-"+string(running)) {
		t.Errorf("orchard deleted %v, want the machine beside the one that failed",
			machines.deleted)
	}
}

// A controller that has not read Orchard yet knows of no machine, and asks Buildkite nothing.
func TestNothingHappensBeforeTheFleetIsRead(t *testing.T) {
	t.Parallel()

	api := &observed{}
	machines := &fleet{read: false}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if api.reads != 0 || len(machines.deleted) != 0 {
		t.Error("the reaper acted before the fleet was read")
	}
}

func TestNewRefusesAnIncompleteReaper(t *testing.T) {
	t.Parallel()

	// The clock is one of the parts: a reaper without one would read the clock of the machine.
	if _, err := reaper.New(reaper.Options{}); err == nil || !strings.Contains(err.Error(), "needs a clock") {
		t.Errorf("New() with nothing = %v, want an error that names the clock", err)
	}
}

// clock is one time for the job states and the reaper, which a test moves.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

// stacks answers get-states for the job states under test.
type stacks struct {
	states map[job.ID]string
	err    error
}

func (s *stacks) States(context.Context, []job.ID) (map[job.ID]string, error) {
	return s.states, s.err
}

// joined builds the real job states and a reaper that reads them, on one clock.
func joined(t *testing.T, api *stacks, machines *fleet, c *clock) (*jobstates.States,
	*reaper.Reaper,
) {
	t.Helper()

	return joinedWith(t, api, machines, c, &pages{})
}

// joinedWith builds the real job states and a reaper that reads them, on one clock, with the job
// pages that the caller gives.
func joinedWith(t *testing.T, api *stacks, machines *fleet, c *clock, jobPages *pages) (
	*jobstates.States, *reaper.Reaper,
) {
	t.Helper()

	states, err := jobstates.New(jobstates.Options{
		Stacks: api, Fleet: machines, Interval: 5 * time.Second, Now: c.now,
	})
	if err != nil {
		t.Fatalf("jobstates.New() = %v, want the states", err)
	}

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	built, err := reaper.New(reaper.Options{
		Stacks:       jobPages,
		States:       states,
		Reservations: &held{},
		RenewBefore:  120 * time.Second,
		CallTimeout:  time.Second,
		Orchard:      machines,
		Fleet:        machines,
		Profiles:     []profile.Profile{{Name: "xcode-27"}},
		Defaults:     limits,
		Freshness:    freshness,
		Metrics:      instruments,
		Logger:       slog.New(slog.DiscardHandler),
		Now:          c.now,
	})
	if err != nil {
		t.Fatalf("reaper.New() = %v, want the reaper", err)
	}

	return states, built
}

// The causal rule, with the job states that the controller runs. A state that the controller asked
// for before a machine existed describes a job that the machine never saw, such as an earlier
// machine of the same job, and it must not end this one. The next state, asked for after the
// machine existed, may.
//
// Both times are on the clock of the controller: the machine exists once a snapshot lists it. The
// creation time of Orchard is on another clock, so the machine here says that it is an hour old.
func TestTheReaperWaitsForAStateAskedAfterTheMachine(t *testing.T) {
	t.Parallel()

	c := &clock{at: now}
	api := &stacks{states: map[job.ID]string{ended: "finished"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Hour)},
		At:    now.Add(2 * time.Second),
	}}

	states, reaping := joined(t, api, machines, c)

	// The controller asks at now, and a snapshot lists the machine two seconds later.
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	c.at = now.Add(3 * time.Second)
	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 0 {
		t.Fatalf("orchard deleted %v on a state from before the machine, want nothing",
			machines.deleted)
	}

	// The next refresh asks after the machine existed, and its answer may end the machine.
	c.at = now.Add(5 * time.Second)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine once a state after it said finished",
			machines.deleted)
	}
}

// The controller first lists a machine in one snapshot, and the later snapshots keep that time. A
// machine that a later snapshot lists with another creation time is a new machine with the same
// name, such as the second machine of a job, and its time starts again.
func TestAMachineIsListedFromTheFirstSnapshotThatShowsIt(t *testing.T) {
	t.Parallel()

	c := &clock{at: now}
	api := &stacks{states: map[job.ID]string{ended: "scheduled"}}
	first := owned(ended, time.Hour)
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{first}, At: now,
	}}

	states, reaping := joined(t, api, machines, c)

	// The first pass sees the machine, and has no state for its job yet.
	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	// A state asked for after the first snapshot counts, while a later snapshot lists the
	// machine again.
	c.at = now.Add(time.Second)
	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	machines.snapshot.At = now.Add(2 * time.Second)

	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 {
		t.Fatalf("orchard deleted %v, want the machine of the lapsed reservation", machines.deleted)
	}

	// A new machine with the same name and the old state. The state was asked for before the
	// snapshot that first listed the new machine, so it must not end it.
	second := owned(ended, time.Minute)
	machines.snapshot = inventory.Snapshot{Owned: []inventory.Owned{second}, At: now.Add(3 * time.Second)}
	c.at = now.Add(4 * time.Second)

	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want no deletion of the new machine on the old state",
			machines.deleted)
	}
}

// The stale rule, with the job states that the controller runs. When Buildkite stops answering,
// the last state grows old, and past the freshness limit it no longer ends a machine. A fresh
// state that says the same thing may.
func TestTheReaperStopsActingOnAStateThatGrewStale(t *testing.T) {
	t.Parallel()

	c := &clock{at: now}
	api := &stacks{states: map[job.ID]string{ended: "finished"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(ended, time.Minute)},
	}}

	states, reaping := joined(t, api, machines, c)

	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	// Buildkite stops answering, and the reaper runs only after the state is too old.
	api.err = errors.New("buildkite is not available")
	c.at = now.Add(freshness + time.Second)

	if err := states.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh() = nil, want the error of Buildkite")
	}

	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 0 {
		t.Fatalf("orchard deleted %v on a stale state, want nothing", machines.deleted)
	}

	// Buildkite answers again, and the fresh state may end the machine.
	api.err = nil

	if err := states.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine once a fresh state said finished",
			machines.deleted)
	}
}

// failing gives a machine of a job whose machine failed, with the status that Orchard gave.
func failing(id job.ID) inventory.Owned {
	machine := owned(id, time.Hour)
	machine.VM.Status = orchard.StatusFailed
	machine.VM.StatusMessage = "the image could not be pulled"

	return machine
}

// Row 9. A machine failed before any agent acquired its job, so the job fails with the reason, and
// then the machine goes.
func TestAFailedMachineFailsItsReservedJobAndThenGoes(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "reserved"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{failing(ended)},
	}}
	jobPages := &pages{}

	if err := buildWith(t, api, machines, &decisions{}, jobPages).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(jobPages.finished) != 1 || jobPages.finished[0] != ended {
		t.Fatalf("buildkite finished %v, want the job of the failed machine", jobPages.finished)
	}

	// The pipeline author reads the reason on the job page.
	if !strings.Contains(jobPages.details[0], "the image could not be pulled") {
		t.Errorf("the job page reads %q, want the status of the machine", jobPages.details[0])
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine after its job ended", machines.deleted)
	}
}

// A job that did not finish keeps its machine, so that the next tick finishes it before the
// machine goes. Without that the job would wait for the lost-agent timeout of Buildkite.
func TestAMachineStaysWhenItsJobDidNotFinish(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "reserved"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{failing(ended)},
	}}
	jobPages := &pages{finishErr: errors.New("buildkite is not available")}

	if err := buildWith(t, api, machines, &decisions{}, jobPages).Reap(t.Context()); err == nil {
		t.Error("Reap() = nil, want the failure to reach the tick")
	}

	if len(machines.deleted) != 0 {
		t.Errorf("orchard deleted %v, want the machine kept until its job ends", machines.deleted)
	}
}

// pass runs one tick of the parts under test: a refresh of the job states when the tick asks
// for one, then the pass of the reaper.
func pass(t *testing.T, c *clock, states *jobstates.States, reaping *reaper.Reaper, refresh bool) {
	t.Helper()

	c.at = c.at.Add(time.Second)

	if refresh {
		if err := states.Refresh(t.Context()); err != nil {
			t.Fatalf("Refresh() = %v, want no error", err)
		}
	}

	_ = reaping.Reap(t.Context())
}

// timedOut gives a machine whose agent did not acquire the job inside the acquire limit.
func timedOut(id job.ID) inventory.Owned {
	machine := owned(id, time.Hour)
	machine.VM.StartedAt = now.Add(-10 * time.Minute)

	return machine
}

// A finish acts only on a state of this tick. A state from an earlier tick can be the reserved
// state of a job that an agent has acquired since, and a finish of it would fail a running build.
// A tick that did not ask again leaves the job and its machine alone, and the next tick that asks
// finishes the job.
func TestAFinishWaitsForAStateOfThisTick(t *testing.T) {
	t.Parallel()

	c := &clock{at: now}
	api := &stacks{states: map[job.ID]string{ended: "reserved"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{timedOut(ended)}, At: now,
	}}
	jobPages := &pages{finishErr: errors.New("buildkite is not available")}
	states, reaping := joinedWith(t, api, machines, c, jobPages)

	// The tick asks, and the finish fails.
	pass(t, c, states, reaping, true)

	if len(jobPages.finished) != 1 {
		t.Fatalf("buildkite saw %d finishes, want 1 on a state of this tick", len(jobPages.finished))
	}

	// The next tick does not ask, so the state is from an earlier tick.
	jobPages.finishErr = nil
	pass(t, c, states, reaping, false)

	if len(jobPages.finished) != 1 || len(machines.deleted) != 0 {
		t.Fatalf("buildkite saw %d finishes and orchard deleted %v on a state of an earlier "+
			"tick, want neither", len(jobPages.finished), machines.deleted)
	}

	pass(t, c, states, reaping, true)

	if len(jobPages.finished) != 2 || len(machines.deleted) != 1 {
		t.Errorf("buildkite saw %d finishes and orchard deleted %v, want the finish and the "+
			"deletion on a state of this tick", len(jobPages.finished), machines.deleted)
	}
}

// Rule 4 of the design. Buildkite refuses a finish with a 422 or a 404, which can mean that the
// job ended, and also that an agent acquired it after the state that the finish acted on. The
// refusal is not a reason to delete the machine, so the pass keeps it, and the next state
// decides: an agent that owns the job keeps the machine (row 8), and a job that ended loses it
// (row 4). A job that is still reserved loses its machine without a second finish, which
// Buildkite would refuse the same way, so a second finish after a restart costs one tick and no
// loop of refusals.
func TestARefusedFinishKeepsTheMachineUntilTheNextState(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		next    string
		deleted int
	}{
		"an agent acquired the job": {next: "running", deleted: 0},
		"the job ended":             {next: "finished", deleted: 1},
		"the job is still reserved": {next: "reserved", deleted: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := &clock{at: now}
			api := &stacks{states: map[job.ID]string{ended: "reserved"}}
			machines := &fleet{read: true, snapshot: inventory.Snapshot{
				Owned: []inventory.Owned{timedOut(ended)}, At: now,
			}}
			jobPages := &pages{finishErr: fmt.Errorf("finish: %w", buildkite.ErrTerminal)}
			states, reaping := joinedWith(t, api, machines, c, jobPages)

			pass(t, c, states, reaping, true)

			if len(jobPages.finished) != 1 || len(machines.deleted) != 0 {
				t.Fatalf("buildkite saw %d finishes and orchard deleted %v, want one refused "+
					"finish and the machine kept", len(jobPages.finished), machines.deleted)
			}

			// A tick that does not ask again changes nothing.
			pass(t, c, states, reaping, false)

			api.states[ended] = test.next
			pass(t, c, states, reaping, true)

			if len(jobPages.finished) != 1 {
				t.Errorf("buildkite saw %d finishes, want 1: a refused job gets no second call",
					len(jobPages.finished))
			}

			if len(machines.deleted) != test.deleted {
				t.Errorf("orchard deleted %v, want %d machines", machines.deleted, test.deleted)
			}
		})
	}
}

// Row 7. An agent owns the job, so Buildkite decides its result. The machine goes with a line on the
// job page, and the job is not finished.
func TestAFailedMachineOfAnAcquiredJobGoesWithALineAndNoFinish(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "running"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{failing(ended)},
	}}
	jobPages := &pages{}

	if err := buildWith(t, api, machines, &decisions{}, jobPages).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(jobPages.finished) != 0 {
		t.Errorf("buildkite finished %v, want no finish of a job that an agent owns",
			jobPages.finished)
	}

	// The two steps of target design, section 10.5: why the machine failed, then its removal.
	var lines []string
	for _, line := range jobPages.notified {
		lines = append(lines, line.Detail)
	}

	want := []string{"failed: the image could not be pulled", "VM deleted: vm_failed"}
	if !slices.Equal(lines, want) {
		t.Errorf("the job page got %q, want %q", lines, want)
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine", machines.deleted)
	}
}

// A line on a job page is worth less than the removal of the machine.
func TestALineThatDoesNotArriveDoesNotKeepTheMachine(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "running"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{failing(ended)},
	}}
	jobPages := &pages{notifyErr: errors.New("buildkite refused the notification")}

	if err := buildWith(t, api, machines, &decisions{}, jobPages).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine although its line did not arrive",
			machines.deleted)
	}
}

// Row 1. Nothing else will remove a machine with the prefix of this controller and no job.
func TestAnOrphanGoes(t *testing.T) {
	t.Parallel()

	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Orphans: []orchard.VM{{Name: "bk-macos-prod-not-a-job", CreatedAt: now}},
	}}

	if err := build(t, &observed{}, machines, &decisions{}).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 1 || machines.deleted[0] != "bk-macos-prod-not-a-job" {
		t.Errorf("orchard deleted %v, want the orphan", machines.deleted)
	}
}

// A state that matches no row keeps the machine and gives one warning, and not one at every tick
// while the job stays in that state. A new state gives a new warning.
func TestAStateThatMatchesNoRowWarnsOnce(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{running: "blocked"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(running, time.Hour)},
	}}
	log := &decisions{}
	reaping := build(t, api, machines, log)

	for range 3 {
		if err := reaping.Reap(t.Context()); err != nil {
			t.Fatalf("Reap() = %v, want no error", err)
		}
	}

	api.states[running] = "waiting"
	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	warnings := 0
	for _, message := range log.messages() {
		if message == "no row of the decision table matches this machine" {
			warnings++
		}
	}

	if warnings != 2 {
		t.Errorf("the reaper warned %d times, want 2: once for each state", warnings)
	}

	if len(machines.deleted) != 0 {
		t.Errorf("orchard deleted %v, want nothing", machines.deleted)
	}
}

// An extension waits for the renewal, which sends every extension of a tick in one call. The
// executor must neither delete the machine nor call for it alone.
func TestAnExtensionIsLeftToTheRenewal(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{running: "reserved"}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: []inventory.Owned{owned(running, time.Minute)},
	}}
	jobPages := &pages{}

	if err := buildWith(t, api, machines, &decisions{}, jobPages).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(machines.deleted) != 0 || len(jobPages.finished) != 0 || len(jobPages.notified) != 0 {
		t.Error("the executor acted on a machine whose reservation only needs an extension")
	}
}

// Admission reads the answer of the inventory after the reaper in the same tick, so the reaper
// tells the inventory about each machine that it deleted. A machine that stays must stay in it.
func TestADeletedMachineIsForgottenAndARefusedDeletionIsNot(t *testing.T) {
	t.Parallel()

	api := &observed{states: map[job.ID]string{ended: "finished", ancient: "finished"}}
	machines := &fleet{
		read: true,
		snapshot: inventory.Snapshot{
			Owned: []inventory.Owned{owned(ended, time.Hour), owned(ancient, time.Hour)},
		},
		refuse: map[string]bool{"bk-macos-prod-" + string(ancient): true},
	}

	if err := build(t, api, machines, &decisions{}).Reap(t.Context()); err == nil {
		t.Fatal("Reap() = nil, want the error of the refused deletion")
	}

	if len(machines.forgot) != 1 || machines.forgot[0] != "bk-macos-prod-"+string(ended) {
		t.Errorf("the reaper told the fleet to forget %v, want only the machine that went",
			machines.forgot)
	}
}
