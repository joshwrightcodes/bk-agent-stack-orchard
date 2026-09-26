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
	"strings"
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
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

	// Orphans are machines whose name has the prefix of this controller and no job identifier.
	// Nothing else will remove them, so the reaper deletes them.
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

// Carried gives the machines that one worker runs.
func (s Snapshot) Carried(worker string) []orchard.VM {
	var carried []orchard.VM

	for _, vm := range s.VMs {
		if vm.Worker == worker {
			carried = append(carried, vm)
		}
	}

	return carried
}

// Unplaced gives the machines that Orchard holds and no worker runs yet. They are the sign that
// the fleet is full, and their resources are not spent on any worker.
func (s Snapshot) Unplaced() []orchard.VM {
	var waiting []orchard.VM

	for _, vm := range s.VMs {
		if !vm.Placed() {
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
	prefix := vmspec.Prefix(i.controllerID)

	for _, vm := range vms {
		if !strings.HasPrefix(vm.Name, prefix) {
			continue
		}

		id, owned := vmspec.OwnedJob(vm.Name, i.controllerID)
		if !owned {
			snapshot.Orphans = append(snapshot.Orphans, vm)

			continue
		}

		snapshot.Owned = append(snapshot.Owned, Owned{VM: vm, Job: id})
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

// Observe adds what the fleet holds to an observation of the instruments.
//
// The profile of a machine is not in the machine: a machine carries the worker labels of its
// profile and nothing that names the profile itself (target design, section 6.1). The caller
// therefore gives what it knows, which is the profile of a job that this process started. A
// machine that this process did not start, because the process restarted, counts under
// UnknownProfile.
func (s Snapshot) Observe(observation *telemetry.Observation, online func(orchard.Worker) bool,
	profileOf func(job.ID) string,
) {
	observation.VMs = map[telemetry.VM]int64{}
	observation.Slots = map[telemetry.Slot]int64{}
	observation.Workers = map[telemetry.WorkerState]int64{}

	for _, owned := range s.Owned {
		name := profileOf(owned.Job)
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

// UnknownProfile is the profile of a machine that this process did not start.
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
