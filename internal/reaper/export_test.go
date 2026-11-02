// SPDX-License-Identifier: AGPL-3.0-or-later

package reaper

// Pending gives how many jobs the reaper holds a first sight of a terminal state for.
func (r *Reaper) Pending() int { return len(r.ended) }
