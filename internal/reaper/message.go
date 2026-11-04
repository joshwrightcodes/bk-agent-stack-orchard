// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package reaper

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
// Lines are the log lines of the startup script, which an acquire timeout and a failed machine
// read. For an acquire timeout, a nil value means that Orchard could not give them, and the message
// says so. A failed machine shows its lines when it has some: an agent that cannot start usually
// exits, and its status then says only how the script ended.
func message(machine Machine, decision Decision, lines []string) string {
	switch decision.Reason {
	case ReasonVMFailed:
		return withLines(withStatus("the virtual machine of this job failed", machine), lines)
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
func acquireTimeout(machine Machine, lines []string) string {
	head := fmt.Sprintf("no agent acquired this job within %s of the start of its virtual machine",
		machine.Timeouts.Acquire)

	if lines == nil {
		return head + ". The output of its startup script was not available."
	}

	if len(lines) == 0 {
		return head + ". Its startup script wrote nothing."
	}

	return withLines(head, lines)
}

// withLines adds the last lines of the startup script to a message, when there are any.
//
// The end of the script is the part that matters, because it holds the reason that the agent did
// not start. The library that sends a finish cuts a long detail at its end, which would remove
// exactly those lines. A message that is too long therefore loses its oldest lines here, and
// never its newest, so that the library has nothing to cut.
func withLines(head string, lines []string) string {
	if len(lines) == 0 {
		return head
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
		lines = append(lines, plain(strings.TrimRight(event.Payload, "\n")))
	}

	return lines
}

// eventLogLine is the kind of an Orchard event that holds one line of the startup script.
const eventLogLine = "log_line"

// Failed gives the line of a machine that stopped while an agent held its job (target design,
// section 10.5). The status message of Orchard says why, when Orchard gave one.
func Failed(status string) string {
	if status == "" {
		return "failed: the virtual machine stopped"
	}

	return "failed: " + status
}

// Deleted gives the last line of a machine: the controller removed it, and why.
func Deleted(reason string) string {
	return "VM deleted: " + reason
}

// statusBudget is the most bytes of the status message of a machine that a message holds: a
// quarter of the budget of the library. The status comes from Orchard and a guest can influence
// it, and it sits before the lines of the startup script. A status without a limit could take the
// whole budget and push out every line, which are the reason that an agent did not start. With
// this limit the lines keep about three quarters of the budget.
const statusBudget = buildkite.DetailBudget / 4

// withStatus adds the status message of the machine, when Orchard gave one. It keeps at most
// statusBudget bytes of it, cut between characters, and ends a cut status with an ellipsis.
func withStatus(text string, machine Machine) string {
	if machine.VM.StatusMessage == "" {
		return text
	}

	status := plain(machine.VM.StatusMessage)

	if len(status) > statusBudget {
		const ellipsis = "…"

		end := statusBudget - len(ellipsis)
		for end > 0 && !utf8.RuneStart(status[end]) {
			end--
		}

		status = status[:end] + ellipsis
	}

	return text + ": " + status
}

// plain drops each control character of a text that a guest can write, except the line break and
// the tab. It also drops each format character (Unicode category Cf, which holds the
// bidirectional overrides and the zero-width characters) and the line and paragraph separators.
// A job page then shows the text that a reader sees in it, in the order that it is written, and an
// escape sequence in it does not reach a terminal or a log viewer.
//
// The message drops these characters, where the notification of the Buildkite client replaces
// each one with a space. A message holds lines of a script, and a dropped character leaves the
// word that it sat in whole. A notification is one line, and a space keeps its words apart.
func plain(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}

		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}

		return r
	}, text)
}
