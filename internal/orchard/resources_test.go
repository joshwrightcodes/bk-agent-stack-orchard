// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

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
