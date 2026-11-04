// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package admission_test

import (
	"fmt"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// The decision of one tick at the scale of the design: a listing of 3000 jobs that never leave the
// queue, beside 100 machines of this controller that fill the fleet (target design, sections 4 and
// 6.3). The loop makes this decision once each second.
func BenchmarkDecide(b *testing.B) {
	for name, tag := range map[string]string{"unmatched": "xcode=99", "matched": "xcode=27"} {
		b.Run(name, func(b *testing.B) {
			decide := request(listing(3000, tag), fullFleet(100))

			b.ReportAllocs()

			for b.Loop() {
				if result := admission.Decide(decide); len(result.Admit) != 0 {
					b.Fatalf("admission started %d jobs on a full fleet", len(result.Admit))
				}
			}
		})
	}
}

// listing gives a queue of jobs that all carry one tag.
func listing(count int, tag string) []buildkite.ScheduledJob {
	jobs := make([]buildkite.ScheduledJob, 0, count)
	for i := range count {
		jobs = append(jobs, buildkite.ScheduledJob{
			ID:          job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012d", i)),
			Rules:       []string{"queue=macos-prod", tag},
			ScheduledAt: now,
		})
	}

	return jobs
}

// fullFleet gives workers of two slots, each full with two machines of this controller.
func fullFleet(machines int) inventory.Snapshot {
	fleet := inventory.Snapshot{At: now}

	for i := range machines / 2 {
		fleet.Workers = append(fleet.Workers, worker(fmt.Sprintf("mac-%02d", i), nil))
	}

	for i := range machines {
		id := job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012d", 10_000+i))
		vm := machine("bk-macos-prod-"+string(id), fmt.Sprintf("mac-%02d", i/2))
		vm.Conditions = []orchard.Condition{{Type: "scheduled", State: "true"}}
		fleet.VMs = append(fleet.VMs, vm)
		fleet.Owned = append(fleet.Owned, inventory.Owned{VM: vm, Job: id})
	}

	return fleet
}
