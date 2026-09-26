package reaper

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// scriptLines is how many lines of the startup script an acquire timeout shows. The design states
// fifty: enough to see why an agent did not start, and few enough to read on a job page.
const scriptLines = 50

// message gives the line that a job page shows when the controller ends a job or reports on its
// machine. It says what happened in the words of the pipeline author, and the status of the
// machine as Orchard gave it.
//
// Lines are the log lines of the startup script, which only an acquire timeout reads. A nil value
// means that Orchard could not give them, and the message says so.
func message(machine Machine, decision Decision, lines []string) string {
	switch decision.Reason {
	case ReasonVMFailed:
		return withStatus("the virtual machine of this job failed", machine)
	case ReasonBootTimeout:
		return withStatus(fmt.Sprintf("the virtual machine of this job did not start within %s",
			machine.Timeouts.Boot), machine)
	case ReasonAcquireTimeout:
		return acquireTimeout(machine, lines)
	default:
		return fmt.Sprintf("the controller removed the virtual machine of this job: %s",
			decision.Reason)
	}
}

// acquireTimeout gives the message of an acquire timeout, with the end of the startup script.
//
// The end of the script is the part that matters, because it holds the reason that the agent did
// not start. The library that sends a finish cuts a long detail at its end, which would remove
// exactly those lines. A message that is too long therefore loses its oldest lines here, and
// never its newest, so that the library has nothing to cut.
func acquireTimeout(machine Machine, lines []string) string {
	head := fmt.Sprintf("no agent acquired this job within %s of the start of its virtual machine",
		machine.Timeouts.Acquire)

	if lines == nil {
		return head + ". The output of its startup script was not available."
	}

	if len(lines) == 0 {
		return head + ". Its startup script wrote nothing."
	}

	if len(lines) > scriptLines {
		lines = lines[len(lines)-scriptLines:]
	}

	head += ". The last lines of its startup script:\n"

	for len(lines) > 0 && len(head)+len(strings.Join(lines, "\n")) > buildkite.DetailBudget {
		lines = lines[1:]
	}

	return head + strings.Join(lines, "\n")
}

// logLines gives the lines of the startup script, oldest first.
func logLines(events []orchard.Event) []string {
	sorted := make([]orchard.Event, 0, len(events))

	for _, event := range events {
		if event.Kind == eventLogLine {
			sorted = append(sorted, event)
		}
	}

	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp < sorted[j].Timestamp })

	lines := make([]string, 0, len(sorted))
	for _, event := range sorted {
		lines = append(lines, strings.TrimRight(event.Payload, "\n"))
	}

	return lines
}

// eventLogLine is the kind of an Orchard event that holds one line of the startup script.
const eventLogLine = "log_line"

// withStatus adds the status message of the machine, when Orchard gave one.
func withStatus(text string, machine Machine) string {
	if machine.VM.StatusMessage == "" {
		return text
	}

	return text + ": " + machine.VM.StatusMessage
}
