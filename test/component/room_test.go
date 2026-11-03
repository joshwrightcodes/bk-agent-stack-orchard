// SPDX-License-Identifier: AGPL-3.0-or-later

package component_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// roomShape is the fleet where a nightly run found that the check of I-2 followed the order of the
// creates: one worker with one slot, which a running machine fills, and two workers with two
// slots each.
var roomShape = []hostShape{
	{slots: 1, cores: 16, memoryMiB: 65536},
	{slots: 2, cores: 16, memoryMiB: 32768},
	{slots: 2, cores: 8, memoryMiB: 65536},
}

// roomMachine is one machine of a tick in the check of I-2: its job and what it takes.
type roomMachine struct {
	job       job.ID
	cores     uint64
	memoryMiB uint64
}

// roomTick is the tick of the nightly run. Placed in the order of admission, the four machines
// fill the four free slots. The creates run at the same time, so they reach Orchard in any order,
// and some of the machines alone fit nowhere in the order of admission: the 2-core machine waits
// when the first 8-core machine has not arrived yet.
var roomTick = []roomMachine{
	{job: "018f3b2c-1c4a-7e5b-9a3d-00000000000d", cores: 8, memoryMiB: 6144},
	{job: "018f3b2c-1c4a-7e5b-9a3d-00000000000e", cores: 6, memoryMiB: 28672},
	{job: "018f3b2c-1c4a-7e5b-9a3d-00000000000f", cores: 8, memoryMiB: 6144},
	{job: "018f3b2c-1c4a-7e5b-9a3d-00000000001c", cores: 2, memoryMiB: 16384},
}

// T-020, the oracle of I-2. The verdict on a tick does not depend on the order in which its
// creates reach Orchard, because Orchard places the machines of one tick in the order of
// admission whatever the order of their arrival.
func TestTheRoomOfATickDoesNotDependOnTheOrderOfItsCreates(t *testing.T) {
	recorder := quiet(t)

	for _, order := range permutations(len(roomTick)) {
		var names []string
		for _, i := range order {
			names = append(names, string(roomTick[i].job[len(roomTick[i].job)-2:]))
		}

		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			f := roomFleet(t, recorder, roomTick)

			for _, i := range order {
				f.create(t, roomTick[i])
			}

			f.checkRoom()

			if len(f.violations) > 0 {
				t.Errorf("the four machines fit in the order of admission, and the check gives %v",
					f.violations)
			}
		})
	}
}

// T-020, the oracle of I-2. A tick whose machines do not all fit in the order of admission breaks
// I-2, in whatever order its creates arrive.
func TestATickThatOverfillsTheFleetBreaksTheRoomInvariant(t *testing.T) {
	recorder := quiet(t)

	over := slices.Concat(roomTick,
		[]roomMachine{{job: "018f3b2c-1c4a-7e5b-9a3d-00000000001d", cores: 2, memoryMiB: 4096}})
	extra := vmspec.Name(controllerID, over[len(over)-1].job)

	for _, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {4, 0, 1, 2, 3}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			f := roomFleet(t, recorder, over)

			for _, i := range order {
				f.create(t, over[i])
			}

			f.checkRoom()

			want := "I-2: no worker that is online has room for " + extra
			if len(f.violations) != 1 || !strings.HasPrefix(f.violations[0], want) {
				t.Errorf("the fifth machine finds no free slot, and the check gives %v, want one "+
					"violation that names %s", f.violations, extra)
			}
		})
	}
}

// roomFleet builds the fleet of roomShape, with a running machine of another owner on the worker
// with one slot, and a reserved job for each machine of the tick.
func roomFleet(t *testing.T, recorder *telemetry.Telemetry, tick []roomMachine) *fleet {
	t.Helper()

	loaded, environment, err := loadSettings(t, 0, "")
	if err != nil {
		t.Fatalf("loading the configuration: %v", err)
	}

	f := newFleetOf(t, t, recorder, loaded, environment, roomShape)

	f.orchard.vms["manual-0"] = &orchard.VM{
		Name:       "manual-0",
		Status:     orchard.StatusRunning,
		Worker:     "mac-01",
		CreatedAt:  f.now,
		Conditions: []orchard.Condition{{Type: "scheduled", State: "true"}},
		Resources: map[string]uint64{
			orchard.ResourceVMs: 1, orchard.ResourceCores: 5, orchard.ResourceMemoryMiB: 11264,
		},
	}

	for _, m := range tick {
		f.stacks.schedule(m.job, true)
		f.stacks.jobs[m.job].state = stateReserved
	}

	return f
}

// create asks the fake Orchard for the machine of one job, as a create of the launcher does.
func (f *fleet) create(t *testing.T, m roomMachine) {
	t.Helper()

	spec := orchard.VM{
		Name: vmspec.Name(controllerID, m.job),
		Resources: map[string]uint64{
			orchard.ResourceVMs: 1, orchard.ResourceCores: m.cores, orchard.ResourceMemoryMiB: m.memoryMiB,
		},
	}

	if err := f.orchard.CreateVM(t.Context(), spec); err != nil {
		t.Fatalf("creating %s: %v", spec.Name, err)
	}
}

// permutations gives every order of the numbers 0 to n-1.
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}

	var all [][]int

	for _, shorter := range permutations(n - 1) {
		for at := 0; at <= len(shorter); at++ {
			order := append(append(append([]int{}, shorter[:at]...), n-1), shorter[at:]...)
			all = append(all, order)
		}
	}

	return all
}
