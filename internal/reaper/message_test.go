package reaper_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// waiting gives a machine that runs and whose agent has not acquired its reserved job for longer
// than the acquire limit, which is row 12.
func waiting(id job.ID) inventory.Owned {
	machine := owned(id, time.Hour)
	machine.VM.StartedAt = now.Add(-10 * time.Minute)

	return machine
}

// script gives lines of the startup script, one event each, out of order as a list can come.
func script(count int, text func(int) string) []orchard.Event {
	events := make([]orchard.Event, 0, count+1)
	for i := count; i >= 1; i-- {
		events = append(events, orchard.Event{Kind: "log_line", Timestamp: int64(i), Payload: text(i)})
	}

	// An event of another kind is not a line of the script. It sits among the newest lines, so the
	// window of the last fifty cannot hide it.
	return append(events, orchard.Event{
		Kind: "status", Timestamp: int64(count), Payload: "not a line",
	})
}

func line(i int) string { return fmt.Sprintf("line %d of the startup script", i) }

// finished runs one pass of the reaper over a machine that waits for its agent, and gives the
// message of the finish.
func finished(t *testing.T, machines *fleet) string {
	t.Helper()

	api := &observed{states: map[job.ID]string{ended: "reserved"}}
	jobPages := &pages{}

	if err := buildWith(t, api, machines, &decisions{}, jobPages).Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(jobPages.details) != 1 {
		t.Fatalf("buildkite saw %d finishes, want 1", len(jobPages.details))
	}

	return jobPages.details[0]
}

// T-007. An agent that did not start leaves its reason at the end of the startup script, so the
// job page shows the last 50 lines, oldest first, and only lines.
func TestAnAcquireTimeoutShowsTheLastFiftyLines(t *testing.T) {
	t.Parallel()

	detail := finished(t, &fleet{
		read:     true,
		snapshot: inventory.Snapshot{Owned: []inventory.Owned{waiting(ended)}},
		events:   script(60, line),
	})

	if strings.Contains(detail, line(10)+"\n") || !strings.Contains(detail, line(11)) {
		t.Errorf("the message starts at the wrong line, want line 11 of 60:\n%s", detail)
	}

	if !strings.HasSuffix(detail, line(60)) {
		t.Errorf("the message does not end with the newest line:\n%s", detail)
	}

	if strings.Index(detail, line(11)) > strings.Index(detail, line(12)) {
		t.Error("the lines are not oldest first")
	}

	if strings.Contains(detail, "not a line") {
		t.Error("the message holds an event that is not a line of the script")
	}
}

// A script whose last lines are long fills the limit of a finish. The message loses its oldest
// lines and keeps its newest, because the newest hold the reason; a cut at the end would remove
// exactly those.
func TestALongScriptKeepsItsNewestLines(t *testing.T) {
	t.Parallel()

	// Lines of about 80 bytes land the message inside the 58 bytes between the budget and 4 KB, so
	// a message that budgets to 4 KB instead of the budget fails here. Longer lines would step over
	// that gap and hide the difference.
	long := func(i int) string { return fmt.Sprintf("%02d %s", i, strings.Repeat("x", 78)) }

	detail := finished(t, &fleet{
		read:     true,
		snapshot: inventory.Snapshot{Owned: []inventory.Owned{waiting(ended)}},
		events:   script(50, long),
	})

	if len(detail) > buildkite.DetailBudget {
		t.Errorf("the message has %d bytes, want %d or fewer, so that the library cuts nothing",
			len(detail), buildkite.DetailBudget)
	}

	if !strings.HasSuffix(detail, long(50)) {
		t.Error("the message lost the newest line of the script")
	}

	if strings.Contains(detail, long(1)) {
		t.Error("the message kept the oldest line and not the room for the newest")
	}
}

// Lines that Orchard cannot give do not stop the finish: the job still fails, and its message says
// that the lines were not available.
func TestAnAcquireTimeoutFinishesWithoutTheLines(t *testing.T) {
	t.Parallel()

	detail := finished(t, &fleet{
		read:      true,
		snapshot:  inventory.Snapshot{Owned: []inventory.Owned{waiting(ended)}},
		eventsErr: errors.New("orchard is not available"),
	})

	if !strings.Contains(detail, "not available") {
		t.Errorf("the message reads %q, want it to say that the lines were not available", detail)
	}
}

// T-007. A boot timeout gives the status that Orchard gave the machine, which names the step that
// failed, such as the pull of an image.
func TestABootTimeoutShowsTheStatusOfTheMachine(t *testing.T) {
	t.Parallel()

	machine := owned(ended, time.Hour)
	machine.VM.Status = orchard.StatusPending
	machine.VM.ScheduledAt = now.Add(-20 * time.Minute)
	machine.VM.StatusMessage = "pulling the image: 61% done"

	detail := finished(t, &fleet{
		read: true, snapshot: inventory.Snapshot{Owned: []inventory.Owned{machine}},
	})

	if !strings.Contains(detail, "pulling the image: 61% done") {
		t.Errorf("the message reads %q, want the status of the machine", detail)
	}
}

// T-103. An agent that cannot start usually exits, and the machine then fails with a status that
// says only how the startup script ended. The job page shows the last lines of the script after
// the status, because they say why.
func TestAFailedMachineShowsTheLastLinesOfItsScript(t *testing.T) {
	t.Parallel()

	machine := failing(ended)
	machine.VM.StatusMessage = "VM failed: failed to run startup script: process exited with status 1"

	detail := finished(t, &fleet{
		read: true, snapshot: inventory.Snapshot{Owned: []inventory.Owned{machine}},
		events: script(60, line),
	})

	for _, want := range []string{"process exited with status 1", line(11), line(60)} {
		if !strings.Contains(detail, want) {
			t.Errorf("the message reads %q, want %q in it", detail, want)
		}
	}

	if strings.Contains(detail, line(10)+"\n") {
		t.Errorf("the message holds more than the last fifty lines:\n%s", detail)
	}

	if !strings.HasSuffix(detail, line(60)) {
		t.Errorf("the message does not end with the newest line:\n%s", detail)
	}
}

// A machine that failed before its startup script ran, as a machine whose image cannot be pulled
// does, has no lines to show, and its message is the status alone.
func TestAFailedMachineWithoutLinesShowsItsStatus(t *testing.T) {
	t.Parallel()

	for name, machines := range map[string]*fleet{
		"no lines": {
			read: true, snapshot: inventory.Snapshot{Owned: []inventory.Owned{failing(ended)}},
		},
		"no events": {
			read: true, snapshot: inventory.Snapshot{Owned: []inventory.Owned{failing(ended)}},
			eventsErr: errors.New("orchard is not available"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			detail := finished(t, machines)
			if strings.Contains(detail, "startup script") {
				t.Errorf("the message reads %q, want the status of the machine alone", detail)
			}

			if !strings.Contains(detail, "the image could not be pulled") {
				t.Errorf("the message reads %q, want the status of the machine", detail)
			}
		})
	}
}
