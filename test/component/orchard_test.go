// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package component_test

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// host is one Mac of the fleet. A stopped host no longer reports, and Orchard fails its machines
// once the offline timeout has passed.
type host struct {
	worker  orchard.Worker
	stopped bool
}

// orchardFake is the Orchard controller and its scheduler, on the clock of the suite. It places
// machines as the scheduler of the pinned version does: oldest first, on the first worker that
// is online, not paused, carries the labels of the machine, and has room for it.
//
// The controller calls it from several goroutines at once, so each call holds mu while it reads or
// changes the state. The knobs (down, deleteFails, answerLost, conflict, hangs) change only
// between ticks, and a call that hangs holds no lock while it waits.
type orchardFake struct {
	mu sync.Mutex

	now     func() time.Time
	offline time.Duration
	hosts   []*host
	vms     map[string]*orchard.VM

	// created is called with each machine that the controller asks for, before Orchard holds it.
	created func(spec orchard.VM)

	// deleting is called with each machine that the controller deletes, before Orchard removes
	// it.
	deleting func(vm orchard.VM)

	// down is the answer of every call while the Orchard controller is not available. Its
	// scheduler stops too, and the machines on the workers run on.
	down error

	// skew is how far the clock of Orchard runs ahead of the clock of the suite, and a negative
	// value runs behind. Orchard gives its times to the machines on its own clock.
	skew time.Duration

	// readsFail is the answer of every list while it is set. The other calls and the scheduler
	// work, as when the answers of Orchard no longer decode after a change of its API.
	readsFail error

	// deleteFails is the answer of every delete while it is set. The other calls work, so a
	// machine that has to go stays in the listing.
	deleteFails error

	// answerLost holds the machines whose create reaches Orchard and whose answer gets lost: the
	// machine exists, and the controller sees a transport error. A machine leaves the set when its
	// create uses it.
	//
	// This knob and conflict name their machines. The launcher sends the creates of one tick at
	// the same time, and the order of its goroutines must not decide which create a knob hits, or
	// rapid could not replay the sequence.
	answerLost map[string]bool

	// conflict holds the machines whose create answers 409, with the machine in place, as the
	// retry of a create that an earlier process made shows. A machine leaves the set when its
	// create uses it.
	conflict map[string]bool

	// hangs makes every call wait for the end of its context, as an Orchard controller that
	// accepts a call and never answers does. Its scheduler stops too, as it does while it is
	// down.
	hangs bool

	// asked counts the calls of each endpoint, the calls that hang or fail included.
	asked map[string]int

	violations *[]string
}

// unavailable gives the answer of an Orchard that is down, or waits for the end of the call when
// it hangs.
func (o *orchardFake) unavailable(ctx context.Context, endpoint string) error {
	o.mu.Lock()
	if o.asked == nil {
		o.asked = map[string]int{}
	}
	o.asked[endpoint]++
	o.mu.Unlock()

	if o.hangs {
		return hang(ctx, o.violations, endpoint)
	}

	return o.down
}

// hostShape is what one worker offers: the slots for machines, the cores, and the memory.
type hostShape struct {
	slots, cores, memoryMiB uint64
}

// defaultHost is the Mac of the fixed scenarios: two slots, as Apple permits.
var defaultHost = hostShape{slots: 2, cores: 16, memoryMiB: 65536}

// newOrchardOf builds an Orchard with one worker for each shape.
func newOrchardOf(now func() time.Time, offline time.Duration, shapes []hostShape) *orchardFake {
	fake := &orchardFake{
		now: now, offline: offline, vms: map[string]*orchard.VM{},
		answerLost: map[string]bool{}, conflict: map[string]bool{},
	}

	for i, shape := range shapes {
		fake.hosts = append(fake.hosts, &host{worker: orchard.Worker{
			Name:     "mac-0" + string(rune('1'+i)),
			Arch:     "arm64",
			Runtime:  "tart",
			LastSeen: now(),
			Resources: map[string]uint64{
				orchard.ResourceVMs:       shape.slots,
				orchard.ResourceCores:     shape.cores,
				orchard.ResourceMemoryMiB: shape.memoryMiB,
			},
		}})
	}

	return fake
}

// clock gives the time on the clock of Orchard.
func (o *orchardFake) clock() time.Time {
	return o.now().Add(o.skew)
}

// online reports a worker that reported inside the offline timeout, as Orchard decides it.
func (o *orchardFake) online(w orchard.Worker) bool {
	return o.now().Sub(w.LastSeen) <= o.offline
}

// sync is what Orchard does between two calls of the controller: the workers that run report,
// the machines of a worker that stopped reporting fail, and the scheduler places what waits.
func (o *orchardFake) sync() {
	if o.down != nil || o.hangs {
		return
	}

	for _, h := range o.hosts {
		if !h.stopped {
			h.worker.LastSeen = o.now()
		}
	}

	for _, vm := range o.vms {
		if vm.Worker == "" {
			continue
		}

		if h := o.host(vm.Worker); h != nil && !o.online(h.worker) &&
			vm.Status != orchard.StatusFailed {
			fail(vm, "the worker "+vm.Worker+" stopped reporting")
		}
	}

	waiting := slices.Collect(maps.Values(o.vms))
	slices.SortFunc(waiting, func(a, b *orchard.VM) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.Name, b.Name))
	})

	for _, vm := range waiting {
		if vm.Worker != "" {
			continue
		}

		for _, h := range o.hosts {
			if !o.online(h.worker) || h.worker.SchedulingPaused || !contains(h.worker.Labels, vm.Labels) {
				continue
			}

			if !orchard.Fits(o.free(h.worker.Name), vm.Resources) {
				continue
			}

			vm.Worker = h.worker.Name
			vm.Conditions = []orchard.Condition{{Type: "scheduled", State: "true"}}
			vm.Status = orchard.StatusRunning
			vm.ScheduledAt = o.clock()
			vm.StartedAt = o.clock()

			break
		}
	}
}

// fail does what Orchard 0.58.0 does to a machine that fails: it keeps its worker, and its
// scheduled condition turns false, so the scheduler no longer counts its resources.
func fail(vm *orchard.VM, message string) {
	vm.Status = orchard.StatusFailed
	vm.StatusMessage = message
	vm.Conditions = []orchard.Condition{{Type: "scheduled", State: "false"}}
}

func (o *orchardFake) host(name string) *host {
	for _, h := range o.hosts {
		if h.worker.Name == name {
			return h
		}
	}

	return nil
}

// free gives what a worker has left after the machines that it carries and that are scheduled. A
// failed machine keeps its worker, and Orchard frees its resources.
func (o *orchardFake) free(worker string) map[string]uint64 {
	var carried []orchard.VM

	for _, vm := range o.vms {
		if vm.Worker == worker && vm.Placed() {
			carried = append(carried, *vm)
		}
	}

	return orchard.Free(o.host(worker).worker, carried)
}

func contains(labels, wanted map[string]string) bool {
	for name, value := range wanted {
		if labels[name] != value {
			return false
		}
	}

	return true
}

func (o *orchardFake) CreateVM(ctx context.Context, spec orchard.VM) error {
	if err := o.unavailable(ctx, "create"); err != nil {
		return err
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if _, exists := o.vms[spec.Name]; exists {
		return orchard.ErrAlreadyExists
	}

	if o.created != nil {
		o.created(spec)
	}

	vm := spec
	vm.Status = orchard.StatusPending
	vm.CreatedAt = o.clock()
	o.vms[spec.Name] = &vm

	if o.answerLost[spec.Name] {
		delete(o.answerLost, spec.Name)

		return errLostAnswer
	}

	if o.conflict[spec.Name] {
		delete(o.conflict, spec.Name)

		return orchard.ErrAlreadyExists
	}

	return nil
}

func (o *orchardFake) ListVMs(ctx context.Context) ([]orchard.VM, error) {
	if err := o.unavailable(ctx, "list machines"); err != nil {
		return nil, err
	}

	if o.readsFail != nil {
		return nil, o.readsFail
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	names := slices.Sorted(maps.Keys(o.vms))

	vms := make([]orchard.VM, 0, len(names))
	for _, name := range names {
		vms = append(vms, *o.vms[name])
	}

	return vms, nil
}

// DeleteVM removes a machine. The client of this project gives no error for a machine that is
// already gone, and the fake answers as the client does.
func (o *orchardFake) DeleteVM(ctx context.Context, name string) error {
	if err := o.unavailable(ctx, "delete"); err != nil {
		return err
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.deleteFails != nil {
		return o.deleteFails
	}

	if vm, exists := o.vms[name]; exists && o.deleting != nil {
		o.deleting(*vm)
	}

	delete(o.vms, name)

	return nil
}

func (o *orchardFake) ListWorkers(ctx context.Context) ([]orchard.Worker, error) {
	if err := o.unavailable(ctx, "list workers"); err != nil {
		return nil, err
	}

	if o.readsFail != nil {
		return nil, o.readsFail
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	workers := make([]orchard.Worker, 0, len(o.hosts))
	for _, h := range o.hosts {
		workers = append(workers, h.worker)
	}

	return workers, nil
}

func (o *orchardFake) Events(ctx context.Context, name string) ([]orchard.Event, error) {
	if err := o.unavailable(ctx, "events"); err != nil {
		return nil, err
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if _, exists := o.vms[name]; !exists {
		return nil, orchard.ErrNotFound
	}

	return nil, nil
}

// owned gives the machines of a controller.
func (o *orchardFake) owned(prefix string) []*orchard.VM {
	var vms []*orchard.VM

	for _, vm := range o.vms {
		if strings.HasPrefix(vm.Name, prefix) {
			vms = append(vms, vm)
		}
	}

	return vms
}
