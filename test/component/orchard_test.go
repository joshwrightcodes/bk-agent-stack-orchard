package component_test

import (
	"context"
	"maps"
	"slices"
	"strings"
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
type orchardFake struct {
	now     func() time.Time
	offline time.Duration
	hosts   []*host
	vms     map[string]*orchard.VM

	// created is called with each machine that the controller asks for, before Orchard holds it.
	created func(spec orchard.VM)

	// down is the answer of every call while the Orchard controller is not available. Its
	// scheduler stops too, and the machines on the workers run on.
	down error
}

func newOrchard(now func() time.Time, offline time.Duration, hosts int) *orchardFake {
	fake := &orchardFake{now: now, offline: offline, vms: map[string]*orchard.VM{}}

	for i := range hosts {
		fake.hosts = append(fake.hosts, &host{worker: orchard.Worker{
			Name:     "mac-0" + string(rune('1'+i)),
			Arch:     "arm64",
			Runtime:  "tart",
			LastSeen: now(),
			Resources: map[string]uint64{
				orchard.ResourceVMs:       2,
				orchard.ResourceCores:     16,
				orchard.ResourceMemoryMiB: 65536,
			},
		}})
	}

	return fake
}

// online reports a worker that reported inside the offline timeout, as Orchard decides it.
func (o *orchardFake) online(w orchard.Worker) bool {
	return o.now().Sub(w.LastSeen) <= o.offline
}

// sync is what Orchard does between two calls of the controller: the workers that run report,
// the machines of a worker that stopped reporting fail, and the scheduler places what waits.
func (o *orchardFake) sync() {
	if o.down != nil {
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
			vm.Status = orchard.StatusFailed
			vm.StatusMessage = "the worker " + vm.Worker + " stopped reporting"
		}
	}

	waiting := slices.Collect(maps.Values(o.vms))
	slices.SortFunc(waiting, func(a, b *orchard.VM) int { return a.CreatedAt.Compare(b.CreatedAt) })

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
			vm.Status = orchard.StatusRunning
			vm.StartedAt = o.now()

			break
		}
	}
}

func (o *orchardFake) host(name string) *host {
	for _, h := range o.hosts {
		if h.worker.Name == name {
			return h
		}
	}

	return nil
}

// free gives what a worker has left after every machine that it carries, whatever its status.
func (o *orchardFake) free(worker string) map[string]uint64 {
	var carried []orchard.VM

	for _, vm := range o.vms {
		if vm.Worker == worker {
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

func (o *orchardFake) CreateVM(_ context.Context, spec orchard.VM) error {
	if o.down != nil {
		return o.down
	}

	if _, exists := o.vms[spec.Name]; exists {
		return orchard.ErrAlreadyExists
	}

	if o.created != nil {
		o.created(spec)
	}

	vm := spec
	vm.Status = orchard.StatusPending
	vm.CreatedAt = o.now()
	o.vms[spec.Name] = &vm

	return nil
}

func (o *orchardFake) ListVMs(context.Context) ([]orchard.VM, error) {
	if o.down != nil {
		return nil, o.down
	}

	names := slices.Sorted(maps.Keys(o.vms))

	vms := make([]orchard.VM, 0, len(names))
	for _, name := range names {
		vms = append(vms, *o.vms[name])
	}

	return vms, nil
}

// DeleteVM removes a machine. The client of this project gives no error for a machine that is
// already gone, and the fake answers as the client does.
func (o *orchardFake) DeleteVM(_ context.Context, name string) error {
	if o.down != nil {
		return o.down
	}

	delete(o.vms, name)

	return nil
}

func (o *orchardFake) ListWorkers(context.Context) ([]orchard.Worker, error) {
	if o.down != nil {
		return nil, o.down
	}

	workers := make([]orchard.Worker, 0, len(o.hosts))
	for _, h := range o.hosts {
		workers = append(workers, h.worker)
	}

	return workers, nil
}

func (o *orchardFake) Events(_ context.Context, name string) ([]orchard.Event, error) {
	if o.down != nil {
		return nil, o.down
	}

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
