package inventory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const (
	controller = "macos-prod"
	first      = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"
	second     = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7e"
)

var now = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

// fleet answers as Orchard.
type fleet struct {
	workers    []orchard.Worker
	vms        []orchard.VM
	workerErr  error
	vmErr      error
	workerRead int
	vmRead     int
}

func (f *fleet) ListWorkers(context.Context) ([]orchard.Worker, error) {
	f.workerRead++

	return f.workers, f.workerErr
}

func (f *fleet) ListVMs(context.Context) ([]orchard.VM, error) {
	f.vmRead++

	return f.vms, f.vmErr
}

func build(t *testing.T, of *fleet) *inventory.Inventory {
	t.Helper()

	held, err := inventory.New(inventory.Options{
		Orchard:        of,
		ControllerID:   controller,
		Interval:       time.Second,
		OfflineTimeout: 3 * time.Minute,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the inventory", err)
	}

	return held
}

// The name decides the owner. A machine of another controller, and a machine of a person who
// used Orchard by hand, must not become the business of this controller.
func TestRefreshSortsTheMachinesByOwner(t *testing.T) {
	t.Parallel()

	of := &fleet{vms: []orchard.VM{
		{Name: "bk-" + controller + "-" + first},
		{Name: "bk-" + controller + "-not-a-job"},
		{Name: "bk-another-controller-" + second},
		{Name: "somebody-elses-machine"},
	}}

	held := build(t, of)
	if err := held.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	snapshot, read := held.Latest()
	if !read {
		t.Fatal("Latest() reported no answer after a refresh")
	}

	if len(snapshot.Owned) != 1 || snapshot.Owned[0].Job != job.ID(first) {
		t.Errorf("the inventory owns %+v, want only the machine of the first job", snapshot.Owned)
	}

	// A name with the prefix and no identifier belongs to this controller, and nothing else will
	// remove it.
	if len(snapshot.Orphans) != 1 || snapshot.Orphans[0].Name != "bk-"+controller+"-not-a-job" {
		t.Errorf("the inventory found the orphans %+v, want the one with no identifier",
			snapshot.Orphans)
	}

	// Capacity accounting needs every machine, whoever owns it.
	if len(snapshot.VMs) != 4 {
		t.Errorf("the inventory holds %d machines, want all 4 of the fleet", len(snapshot.VMs))
	}
}

// A snapshot with the workers of now and the machines of a minute ago gives an accounting that
// matches neither.
func TestARefreshThatFailsKeepsTheAnswerBeforeIt(t *testing.T) {
	t.Parallel()

	of := &fleet{vms: []orchard.VM{{Name: "bk-" + controller + "-" + first}}}

	held := build(t, of)
	if err := held.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	of.vmErr = errors.New("orchard is not available")

	if err := held.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh() = nil, want the error of Orchard")
	}

	snapshot, read := held.Latest()
	if !read || len(snapshot.Owned) != 1 {
		t.Errorf("the answer is now %+v, want the answer that arrived before the failure",
			snapshot.Owned)
	}
}

// The machines are read only when the workers answered, because one without the other cannot
// give an accounting.
func TestARefreshThatCannotReadTheWorkersReadsNoMachines(t *testing.T) {
	t.Parallel()

	of := &fleet{workerErr: errors.New("orchard is not available")}

	held := build(t, of)
	if err := held.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh() = nil, want the error of Orchard")
	}

	if of.vmRead != 0 {
		t.Errorf("the inventory read the machines %d times, want none", of.vmRead)
	}

	if _, read := held.Latest(); read {
		t.Error("Latest() reported an answer after a refresh that failed")
	}
}

func TestTheInventoryIsTheInventorySource(t *testing.T) {
	t.Parallel()

	held := build(t, &fleet{})

	if held.Source() != telemetry.SourceInventory {
		t.Errorf("Source() = %q, want %q", held.Source(), telemetry.SourceInventory)
	}

	if held.Interval() != time.Second {
		t.Errorf("Interval() = %s, want the 1s that the options gave", held.Interval())
	}

	if !held.At().IsZero() {
		t.Errorf("At() = %s before the first refresh, want the zero time", held.At())
	}
}

// A worker that stopped answering cannot take a machine, and neither can one that an operator
// paused or one that the profile excludes.
func TestEligibleRefusesAWorkerThatCannotTakeAMachine(t *testing.T) {
	t.Parallel()

	held := build(t, &fleet{})

	ready := orchard.Worker{Name: "mac-01", LastSeen: now, Labels: map[string]string{"pool": "pilot"}}

	if !held.Eligible(ready, map[string]string{"pool": "pilot"}) {
		t.Error("Eligible() = false for a worker that answered and carries the label")
	}

	if held.Eligible(ready, map[string]string{"pool": "other"}) {
		t.Error("Eligible() = true for a worker whose labels the profile excludes")
	}

	paused := ready
	paused.SchedulingPaused = true

	if held.Eligible(paused, nil) {
		t.Error("Eligible() = true for a worker that an operator paused")
	}

	silent := ready
	silent.LastSeen = now.Add(-time.Hour)

	if held.Eligible(silent, nil) {
		t.Error("Eligible() = true for a worker that stopped answering")
	}
}

func TestTheSnapshotAnswersAboutOneWorkerAndOneJob(t *testing.T) {
	t.Parallel()

	on := orchard.VM{Name: "bk-" + controller + "-" + first, Worker: "mac-01"}
	off := orchard.VM{Name: "bk-" + controller + "-" + second}

	snapshot := inventory.Snapshot{
		VMs:   []orchard.VM{on, off},
		Owned: []inventory.Owned{{VM: on, Job: job.ID(first)}},
	}

	if carried := snapshot.Carried("mac-01"); len(carried) != 1 || carried[0].Name != on.Name {
		t.Errorf("Carried() = %+v, want the machine of that worker", carried)
	}

	if unplaced := snapshot.Unplaced(); len(unplaced) != 1 || unplaced[0].Name != off.Name {
		t.Errorf("Unplaced() = %+v, want the machine with no worker", unplaced)
	}

	if _, has := snapshot.Job(job.ID(first)); !has {
		t.Error("Job() found no machine for a job that has one")
	}

	if _, has := snapshot.Job(job.ID(second)); has {
		t.Error("Job() found a machine for a job that this controller does not own")
	}
}

func TestNewRefusesAnIncompleteInventory(t *testing.T) {
	t.Parallel()

	whole := inventory.Options{
		Orchard:        &fleet{},
		ControllerID:   controller,
		Interval:       time.Second,
		OfflineTimeout: time.Minute,
	}

	for name, change := range map[string]func(*inventory.Options){
		"no client":     func(o *inventory.Options) { o.Orchard = nil },
		"no identifier": func(o *inventory.Options) { o.ControllerID = "" },
		"no interval":   func(o *inventory.Options) { o.Interval = 0 },
		"no timeout":    func(o *inventory.Options) { o.OfflineTimeout = 0 },
	} {
		options := whole
		change(&options)

		if _, err := inventory.New(options); err == nil {
			t.Errorf("New() with %s = nil, want an error", name)
		}
	}
}

// The instruments carry the fleet as a person reads it on a dashboard: how many machines are in
// each phase, and how much room each pool has left.
func TestTheSnapshotFillsTheObservationOfTheInstruments(t *testing.T) {
	t.Parallel()

	running := orchard.VM{
		Name: "bk-" + controller + "-" + first, Worker: "mac-01",
		Status: orchard.StatusRunning,
	}
	failed := orchard.VM{
		Name: "bk-" + controller + "-" + second, Worker: "mac-01",
		Status: orchard.StatusFailed,
	}

	snapshot := inventory.Snapshot{
		Workers: []orchard.Worker{{
			Name:      "mac-01",
			Labels:    map[string]string{"pool": "pilot"},
			LastSeen:  now,
			Resources: map[string]uint64{orchard.ResourceVMs: 4},
		}},
		VMs: []orchard.VM{running, failed},
		Owned: []inventory.Owned{
			{VM: running, Job: job.ID(first)},
			{VM: failed, Job: job.ID(second)},
		},
	}

	observation := telemetry.Observation{}
	snapshot.Observe(&observation, func(orchard.Worker) bool { return true },
		func(id job.ID) string {
			if id == job.ID(first) {
				return "xcode-27"
			}

			// A machine that this process did not start has no profile that it can name.
			return ""
		})

	if got := observation.VMs[telemetry.VM{Profile: "xcode-27", Phase: telemetry.VMRunning}]; got != 1 {
		t.Errorf("the running machines of the profile count %d, want 1", got)
	}

	unknown := telemetry.VM{Profile: inventory.UnknownProfile, Phase: telemetry.VMFailed}
	if got := observation.VMs[unknown]; got != 1 {
		t.Errorf("the failed machines of an unknown profile count %d, want 1", got)
	}

	// The worker of the conventions is the machine that carries a job.
	if got := observation.Workers[telemetry.WorkerBusy]; got != 1 {
		t.Errorf("the busy workers count %d, want 1", got)
	}

	if got := observation.Workers[telemetry.WorkerOffline]; got != 1 {
		t.Errorf("the offline workers count %d, want 1", got)
	}

	pool := func(state telemetry.SlotState) int64 {
		return observation.Slots[telemetry.Slot{Pool: "pilot", State: state}]
	}

	if pool(telemetry.SlotsTotal) != 4 || pool(telemetry.SlotsUsed) != 2 ||
		pool(telemetry.SlotsFree) != 2 {
		t.Errorf("the pool has %d slots, %d used and %d free, want 4, 2 and 2",
			pool(telemetry.SlotsTotal), pool(telemetry.SlotsUsed), pool(telemetry.SlotsFree))
	}
}

// Orchard can place more machines on a worker than it advertises, because an operator can lower
// the number afterwards. The fleet then has nothing free, and not a negative amount.
func TestAFullWorkerHasNothingFreeAndNotLess(t *testing.T) {
	t.Parallel()

	on := func(name string) orchard.VM { return orchard.VM{Name: name, Worker: "mac-01"} }

	snapshot := inventory.Snapshot{
		Workers: []orchard.Worker{{
			Name:      "mac-01",
			LastSeen:  now,
			Resources: map[string]uint64{orchard.ResourceVMs: 1},
		}},
		VMs: []orchard.VM{on("one"), on("two"), on("three")},
	}

	observation := telemetry.Observation{}
	snapshot.Observe(&observation, func(orchard.Worker) bool { return true },
		func(job.ID) string { return "" })

	if got := observation.Slots[telemetry.Slot{State: telemetry.SlotsFree}]; got != 0 {
		t.Errorf("the pool has %d slots free, want 0", got)
	}
}

// A worker that stopped answering gives no slots, so the fleet does not look larger than it is.
func TestAnOfflineWorkerGivesNoSlots(t *testing.T) {
	t.Parallel()

	snapshot := inventory.Snapshot{
		Workers: []orchard.Worker{{
			Name:      "mac-01",
			Resources: map[string]uint64{orchard.ResourceVMs: 4},
		}},
	}

	observation := telemetry.Observation{}
	snapshot.Observe(&observation, func(orchard.Worker) bool { return false },
		func(job.ID) string { return "" })

	if len(observation.Slots) != 0 {
		t.Errorf("the observation holds %+v, want no slots for an offline worker",
			observation.Slots)
	}
}
