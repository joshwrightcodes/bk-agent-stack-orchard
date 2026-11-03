// SPDX-License-Identifier: AGPL-3.0-or-later

package buildkite_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
)

// The reaper decides by these two sets, so each state of a job belongs to the right one, and a
// state that Buildkite adds later belongs to neither.
func TestTheStatesOfAJob(t *testing.T) {
	t.Parallel()

	for state, want := range map[string]struct{ acquired, terminal bool }{
		"scheduled":  {},
		"reserved":   {},
		"assigned":   {acquired: true},
		"accepted":   {acquired: true},
		"running":    {acquired: true},
		"canceling":  {acquired: true},
		"timing_out": {acquired: true},
		"finished":   {terminal: true},
		"canceled":   {terminal: true},
		"expired":    {terminal: true},
		"timed_out":  {terminal: true},
		"skipped":    {terminal: true},
		"broken":     {terminal: true},
		"":           {},
		"Running":    {},
		"unknown":    {},
	} {
		if got := buildkite.Acquired(state); got != want.acquired {
			t.Errorf("Acquired(%q) = %t, want %t", state, got, want.acquired)
		}

		if got := buildkite.Terminal(state); got != want.terminal {
			t.Errorf("Terminal(%q) = %t, want %t", state, got, want.terminal)
		}
	}
}
