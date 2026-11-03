// SPDX-License-Identifier: AGPL-3.0-or-later

// Package inventory holds what Orchard has: the workers of the fleet, and the machines that run
// on them.
//
// The controller decides from observed state and never from a counter of its own (target design,
// section 7.1). This package is that observation. It records what Orchard answered and when, and
// it decides nothing: a machine that must go is the business of the reaper, and a job that may
// start is the business of admission.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// Orchard is what this package needs of the Orchard API.
type Orchard interface {
	ListVMs(ctx context.Context) ([]orchard.VM, error)
	ListWorkers(ctx context.Context) ([]orchard.Worker, error)
}

// Owned is one machine that this controller created, and the job that it carries.
type Owned struct {
	VM  orchard.VM
	Job job.ID
}

// Snapshot is what Orchard held at one moment.
type Snapshot struct {
	// Workers are every worker of the fleet, in the answer of Orchard.
	Workers []orchard.Worker

	// VMs are every machine of the fleet, of every owner. Capacity accounting needs all of
	// them, because a machine of another controller takes the same resources as one of this
	// controller.
	VMs []orchard.VM

	// Owned are the machines of this controller, with the job of each one.
	Owned []Owned

	// Orphans are machines whose name has the prefix of this controller and does not end in a
	// job identifier. A machine that ends in one belongs to the controller that its name
	// names, even when that identifier extends this one. Nothing else will remove an orphan, so
	// the reaper deletes it.
	Orphans []orchard.VM

	// At is when Orchard answered.
	At time.Time
}

// Job gives the machine of a job, when this controller has one.
func (s Snapshot) Job(id job.ID) (orchard.VM, bool) {
	for _, owned := range s.Owned {
		if owned.Job == id {
			return owned.VM, true
		}
	}

	return orchard.VM{}, false
}

// Carried gives the machines that one worker runs. A machine that Orchard has not scheduled
// does not count, even when it keeps the name of a worker: Orchard keeps the worker of a machine
// that failed and frees its resources through the scheduled condition.
func (s Snapshot) Carried(worker string) []orchard.VM {
	var carried []orchard.VM

	for _, vm := range s.VMs {
		if vm.Worker == worker && vm.Placed() {
			carried = append(carried, vm)
		}
	}

	return carried
}

// Unplaced gives the machines that Orchard holds and no worker runs yet. They are the sign that
// the fleet is full, and their resources are not spent on any worker.
//
// A machine that failed is left out. Orchard does not place it again: its unscheduled pass skips
// a machine in a terminal state, and the only terminal status is failed (scheduler.go and
// pkg/resource/v1/v1.go of Orchard 0.58.0, commit 22bd63c34674, checked 2026-11-01).
func (s Snapshot) Unplaced() []orchard.VM {
	var waiting []orchard.VM

	for _, vm := range s.VMs {
		if !vm.Placed() && vm.Status != orchard.StatusFailed {
			waiting = append(waiting, vm)
		}
	}

	return waiting
}

// Inventory reads Orchard and keeps the last answer.
type Inventory struct {
	orchard      Orchard
	controllerID string
	interval     time.Duration
	offline      time.Duration
	now          func() time.Time

	mu     sync.RWMutex
	latest Snapshot
	got    bool
}

// Options configures the inventory.
type Options struct {
	// Orchard is the client.
	Orchard Orchard

	// ControllerID decides which machines belong to this controller.
	ControllerID string

	// Interval is how often the inventory may read Orchard. The design states one second.
	Interval time.Duration

	// OfflineTimeout is how long a worker may be silent before the fleet counts it as offline.
	OfflineTimeout time.Duration

	// Now is the clock of the part. internal/app gives every part the same one.
	Now func() time.Time
}

// New builds the inventory.
func New(options Options) (*Inventory, error) {
	var problems []error

	if options.Now == nil {
		problems = append(problems, errors.New("the inventory needs a clock"))
	}

	if options.Orchard == nil {
		problems = append(problems, errors.New("the inventory needs an Orchard client"))
	}

	if options.ControllerID == "" {
		problems = append(problems, errors.New("the inventory needs the controller identifier"))
	}

	if options.Interval <= 0 {
		problems = append(problems, fmt.Errorf("the inventory interval is %s, and it must be "+
			"more than zero", options.Interval))
	}

	if options.OfflineTimeout <= 0 {
		problems = append(problems, fmt.Errorf("the worker offline timeout is %s, and it must "+
			"be more than zero", options.OfflineTimeout))
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	now := options.Now

	return &Inventory{
		orchard:      options.Orchard,
		controllerID: options.ControllerID,
		interval:     options.Interval,
		offline:      options.OfflineTimeout,
		now:          now,
	}, nil
}

// Source says which supply of observed state this is.
func (i *Inventory) Source() telemetry.Source {
	return telemetry.SourceInventory
}

// Interval is how often the source may be read. The control loop decides when that is.
func (i *Inventory) Interval() time.Duration {
	return i.interval
}

// At gives when the last answer arrived, and the zero time while none has.
func (i *Inventory) At() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if !i.got {
		return time.Time{}
	}

	return i.latest.At
}

// Refresh reads the workers and the machines, and keeps what it read.
//
// Both calls must succeed. A snapshot with the workers of now and the machines of a minute ago
// would give an accounting that matches neither, and a controller that acts on it would start
// machines that the fleet cannot carry.
func (i *Inventory) Refresh(ctx context.Context) error {
	workers, err := i.orchard.ListWorkers(ctx)
	if err != nil {
		return fmt.Errorf("reading the workers: %w", err)
	}

	vms, err := i.orchard.ListVMs(ctx)
	if err != nil {
		return fmt.Errorf("reading the machines: %w", err)
	}

	snapshot := Snapshot{Workers: workers, VMs: vms, At: i.now()}

	for _, vm := range vms {
		if id, owned := vmspec.OwnedJob(vm.Name, i.controllerID); owned {
			snapshot.Owned = append(snapshot.Owned, Owned{VM: vm, Job: id})
		} else if vmspec.IsOrphan(vm.Name, i.controllerID) {
			snapshot.Orphans = append(snapshot.Orphans, vm)
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	i.latest = snapshot
	i.got = true

	return nil
}

// Latest gives the last answer, and reports whether one arrived.
func (i *Inventory) Latest() (Snapshot, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	return i.latest, i.got
}

// Forget takes a machine that Orchard no longer holds out of the last answer. The reaper calls it
// after a delete that Orchard accepted, so that admission, which runs after the reaper in the
// same tick, counts the room that the delete freed (target design, section 4). The next refresh
// replaces the answer, as before.
//
// A reader that already holds the answer keeps what it read: Forget builds new lists and does not
// change the old ones.
func (i *Inventory) Forget(name string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	kept := i.latest

	kept.VMs = slices.DeleteFunc(slices.Clone(kept.VMs), func(vm orchard.VM) bool {
		return vm.Name == name
	})
	kept.Owned = slices.DeleteFunc(slices.Clone(kept.Owned), func(owned Owned) bool {
		return owned.VM.Name == name
	})
	kept.Orphans = slices.DeleteFunc(slices.Clone(kept.Orphans), func(vm orchard.VM) bool {
		return vm.Name == name
	})

	i.latest = kept
}

// Eligible reports a worker that can take a new machine: it answered inside the offline timeout,
// an operator has not paused it, and it carries the labels that a profile asks for.
func (i *Inventory) Eligible(worker orchard.Worker, labels map[string]string) bool {
	if worker.Offline(i.now(), i.offline) || worker.SchedulingPaused {
		return false
	}

	for name, want := range labels {
		if worker.Labels[name] != want {
			return false
		}
	}

	return true
}

// Online reports a worker that answered inside the offline timeout.
func (i *Inventory) Online(worker orchard.Worker) bool {
	return !worker.Offline(i.now(), i.offline)
}

// poolLabel is the worker label that names a pool of the fleet.
const poolLabel = "pool"

// ProfileOf gives the profile of a machine of this controller: the profile that this process
// started it with, as started gives it, or else the profile that the tags of the machine name, when
// the configuration still has a profile of that name. It gives empty text for a machine of neither,
// such as one whose profile an operator removed before a restart.
func ProfileOf(owned Owned, started func(job.ID) string, profiles []profile.Profile) string {
	if name := started(owned.Job); name != "" {
		return name
	}

	name := vmspec.ProfileOf(owned.VM)

	for _, p := range profiles {
		if name != "" && p.Name == name {
			return name
		}
	}

	return ""
}

// Observe adds what the fleet holds to an observation of the instruments.
//
// The caller gives the profile of each machine, as ProfileOf finds it. A machine whose profile it
// cannot name counts under UnknownProfile.
func (s Snapshot) Observe(observation *telemetry.Observation, online func(orchard.Worker) bool,
	profileOf func(Owned) string,
) {
	observation.VMs = map[telemetry.VM]int64{}
	observation.Slots = map[telemetry.Slot]int64{}
	observation.Workers = map[telemetry.WorkerState]int64{}

	for _, owned := range s.Owned {
		name := profileOf(owned)
		if name == "" {
			name = UnknownProfile
		}

		observation.VMs[telemetry.VM{Profile: name, Phase: Phase(owned.VM)}]++

		// The worker of the conventions is the thing that does the work of a job, which here is
		// the machine. A machine of this controller carries one job and is never idle, so it is
		// busy while it runs and offline when it failed. A machine that is still starting is not
		// a worker yet.
		switch Phase(owned.VM) {
		case telemetry.VMRunning:
			observation.Workers[telemetry.WorkerBusy]++
		case telemetry.VMFailed:
			observation.Workers[telemetry.WorkerOffline]++
		}
	}

	s.slots(observation, online)
}

// Leftover counts the machines of jobs, and not the orphans, whose job is in a terminal state
// according to the function. The reaper deletes such a machine, so the count is the work that is
// left for it.
func (s Snapshot) Leftover(observation *telemetry.Observation, terminal func(job.ID) bool) {
	observation.Leftover = 0

	for _, owned := range s.Owned {
		if terminal(owned.Job) {
			observation.Leftover++
		}
	}
}

// UnknownProfile is the profile of a machine that this process did not start and whose tags name no
// profile of the configuration.
const UnknownProfile = "unknown"

// slots counts the machine slots of each pool.
func (s Snapshot) slots(observation *telemetry.Observation, online func(orchard.Worker) bool) {
	used := map[string]int64{}
	total := map[string]int64{}

	for _, worker := range s.Workers {
		if !online(worker) {
			continue
		}

		pool := worker.Labels[poolLabel]
		total[pool] += count(worker.Resources[orchard.ResourceVMs])
		used[pool] += int64(len(s.Carried(worker.Name)))
	}

	for pool, slots := range total {
		observation.Slots[telemetry.Slot{Pool: pool, State: telemetry.SlotsTotal}] = slots
		observation.Slots[telemetry.Slot{Pool: pool, State: telemetry.SlotsUsed}] = used[pool]

		free := slots - used[pool]
		if free < 0 {
			// A worker can carry more machines than it advertises, because Orchard placed them
			// before an operator lowered the number. The fleet then has nothing free, and not a
			// negative amount.
			free = 0
		}

		observation.Slots[telemetry.Slot{Pool: pool, State: telemetry.SlotsFree}] = free
	}
}

// count turns a resource of Orchard into a number that an instrument carries. Orchard counts
// without a sign and an instrument counts with one, and a fleet of more machines than a signed
// number holds does not exist.
func count(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}

	return int64(value)
}

// Phase says how far a machine has come, in the words of the instruments.
func Phase(vm orchard.VM) telemetry.VMPhase {
	switch {
	case vm.Status == orchard.StatusFailed:
		return telemetry.VMFailed
	case vm.Status == orchard.StatusRunning:
		return telemetry.VMRunning
	case !vm.Placed():
		return telemetry.VMUnplaced
	default:
		return telemetry.VMBooting
	}
}
