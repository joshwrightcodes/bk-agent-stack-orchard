// SPDX-License-Identifier: AGPL-3.0-or-later

package orchard_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

func TestPlacedReadsTheScheduledConditionAsOrchardDoes(t *testing.T) {
	scheduled := func(state string) []orchard.Condition {
		return []orchard.Condition{{Type: "scheduled", State: state}}
	}

	tests := []struct {
		name string
		vm   orchard.VM
		want bool
	}{
		{"no worker and no condition", orchard.VM{}, false},
		{"a worker and no condition", orchard.VM{Worker: "mac-01"}, true},
		{"a worker and a true condition", orchard.VM{Worker: "mac-01", Conditions: scheduled("true")}, true},
		{"a worker and a false condition", orchard.VM{Worker: "mac-01", Conditions: scheduled("false")}, false},
		{"a true condition and no worker", orchard.VM{Conditions: scheduled("true")}, true},
		{
			"conditions that do not name scheduled",
			orchard.VM{Worker: "mac-01", Conditions: []orchard.Condition{{Type: "running", State: "false"}}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.vm.Placed(); got != tt.want {
				t.Errorf("Placed() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Orchard keeps the worker name of a machine that failed and sets its scheduled condition to
// false. Such a machine of another owner must not use a slot of the worker.
func TestAFailedVMOfAnotherOwnerHoldsNoSlot(t *testing.T) {
	worker := orchard.Worker{Name: "mac-01", Resources: map[string]uint64{orchard.ResourceVMs: 2}}

	failed := orchard.VM{
		Name:       "bk-other-0001",
		Status:     "failed",
		Worker:     "mac-01",
		Resources:  map[string]uint64{orchard.ResourceVMs: 1},
		Conditions: []orchard.Condition{{Type: "scheduled", State: "false"}},
	}
	running := orchard.VM{
		Name:      "bk-other-0002",
		Status:    "running",
		Worker:    "mac-01",
		Resources: map[string]uint64{orchard.ResourceVMs: 1},
	}

	var carried []orchard.VM

	for _, vm := range []orchard.VM{failed, running} {
		if vm.Placed() {
			carried = append(carried, vm)
		}
	}

	if got := orchard.Free(worker, carried)[orchard.ResourceVMs]; got != 1 {
		t.Errorf("free machines = %d, want 1: the failed machine holds no slot", got)
	}
}
