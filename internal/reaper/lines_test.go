// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package reaper_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// finishedJobs gives n jobs, named by their number, whose state is finished, and a machine for
// each of them.
func finishedJobs(n int) (map[job.ID]string, []inventory.Owned) {
	states := make(map[job.ID]string, n)
	machines := make([]inventory.Owned, 0, n)

	for i := range n {
		id := job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", i))
		states[id] = "finished"
		machines = append(machines, owned(id, time.Hour))
	}

	return states, machines
}

// The lines of one pass go to Buildkite in one request, and not one request for each machine
// (target design, section 4). A wave of jobs that end together would otherwise spend one request
// on each of them. The line of a failed machine still comes before the line of its deletion.
func TestThePassWritesItsLinesInOneRequest(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(3)
	states[ended] = "running"
	list = append(list, failing(ended))

	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned:   list,
		Orphans: []orchard.VM{{Name: "bk-macos-prod-not-a-job", CreatedAt: now}},
	}}
	jobPages := &pages{}

	if err := buildWith(t, &observed{states: states}, machines, &decisions{}, jobPages).
		Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	// An orphan has no job, so it has no page and no line.
	if !slices.Equal(jobPages.requests, []int{5}) {
		t.Fatalf("buildkite saw requests of %v lines, want one request of 5 lines", jobPages.requests)
	}

	var page []string

	for _, line := range jobPages.notified {
		if line.JobID == ended {
			page = append(page, line.Detail)
		}
	}

	want := []string{"failed: the image could not be pulled", "VM deleted: vm_failed"}
	if !slices.Equal(page, want) {
		t.Errorf("the job page of the failed machine got %q, want %q", page, want)
	}
}

// A request carries at most NotificationsPerRequest lines, so a longer pass splits its lines.
func TestTheLinesOfAPassSplitAtTheLimitOfARequest(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(buildkite.NotificationsPerRequest + 1)
	machines := &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}}
	jobPages := &pages{}

	if err := buildWith(t, &observed{states: states}, machines, &decisions{}, jobPages).
		Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	want := []int{buildkite.NotificationsPerRequest, 1}
	if !slices.Equal(jobPages.requests, want) {
		t.Errorf("buildkite saw requests of %v lines, want %v", jobPages.requests, want)
	}
}

// Buildkite may refuse a line for a job that ended, which is the usual case for the line of a
// deletion. A refusal changes nothing, so it is a debug record and not a warning at each pass.
func TestARefusedLineIsNoWarning(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(2)
	machines := &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}}
	log := &decisions{}
	jobPages := &pages{notifyErr: &buildkite.RefusedError{
		Indexes: []int{0, 1}, Total: 2, Reason: "the job has finished",
	}}

	if err := buildWith(t, &observed{states: states}, machines, log, jobPages).
		Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	log.mu.Lock()
	defer log.mu.Unlock()

	for _, record := range log.records {
		if record.Level >= slog.LevelWarn && strings.Contains(record.Message, "job page") {
			t.Errorf("the reaper wrote %q at %s, want no warning for a refused line",
				record.Message, record.Level)
		}
	}
}

// A request that finds Buildkite not answering stops the requests behind it, because they would
// wait for the same Buildkite, each for its whole time limit.
func TestARequestThatBuildkiteDoesNotAnswerStopsTheLaterRequests(t *testing.T) {
	t.Parallel()

	states, list := finishedJobs(buildkite.NotificationsPerRequest + 1)
	machines := &fleet{read: true, snapshot: inventory.Snapshot{Owned: list}}
	jobPages := &pages{notifyErr: context.DeadlineExceeded}

	if err := buildWith(t, &observed{states: states}, machines, &decisions{}, jobPages).
		Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if !slices.Equal(jobPages.requests, []int{buildkite.NotificationsPerRequest}) {
		t.Errorf("buildkite saw requests of %v lines, want only the first", jobPages.requests)
	}
}
