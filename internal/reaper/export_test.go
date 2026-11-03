// SPDX-License-Identifier: AGPL-3.0-or-later

package reaper

import (
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// Pending gives how many jobs the reaper holds a first sight of a terminal state for.
func (r *Reaper) Pending() int { return len(r.ended) }

// Saw records a snapshot taken at the time at that showed the machine as it is, as an earlier pass
// of this process would have.
func (r *Reaper) Saw(vm orchard.VM, at time.Time) { r.list(vm, at) }
