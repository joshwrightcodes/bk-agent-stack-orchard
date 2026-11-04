// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package reaper_test

import (
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// A guest writes the lines of its startup script and can influence the status message of its
// machine, and the finish message of a job quotes both. Whatever the events and the status hold, in
// each case that finishes a job (a failed machine, a boot timeout, an acquire timeout), the message
// is never empty, it fits in the budget that the library of Buildkite sends unchanged, it is whole
// characters, and it holds no control or format character and no line or paragraph separator,
// except the line breaks and the tabs of the lines.
func FuzzTheFinishMessageOfAnyVMEvents(f *testing.F) {
	long := strings.Repeat("ビ", 5000)

	f.Add("log_line", "agent started", int64(1), "log_line", "agent exited", int64(2), uint8(3),
		"VM failed: process exited with status 1", uint8(0))
	f.Add("log_line", "", int64(0), "log_line", "", int64(0), uint8(0), "", uint8(1))
	f.Add("status", "not a line", int64(5), "log_line", "line", int64(-5), uint8(1), "x", uint8(2))
	f.Add("log_line", "colour \x1b[31mred\x1b[0m\r\n", int64(1), "log_line", "bell \a", int64(1),
		uint8(2), "\x1b[31mred", uint8(0))
	f.Add("log_line", long, int64(7), "log_line", "x", int64(8), uint8(2), long, uint8(0))
	f.Add("log_line", strings.Repeat("x", 5000), int64(1), "log_line", "\u0085\u2028\u202e",
		int64(2), uint8(40), strings.Repeat("y", 9000), uint8(1))
	f.Add("log_line", "a\u2028b\u2029c\u2066d\ufeffe\u200bf", int64(1), "log_line", "g\u202eh", int64(2),
		uint8(2), "VM\u202e failed\u200d", uint8(0))
	f.Add("", "", int64(1<<62), "", "", int64(-1<<62), uint8(255), "", uint8(255))

	f.Fuzz(func(t *testing.T, kind1, payload1 string, at1 int64, kind2, payload2 string, at2 int64,
		copies uint8, status string, which uint8,
	) {
		// Orchard gives events and statuses as JSON, and the decoder of JSON gives whole characters
		// only.
		for _, text := range []string{kind1, payload1, kind2, payload2, status} {
			if !utf8.ValidString(text) {
				t.Skip("an answer of Orchard holds whole characters")
			}
		}

		// The count of events comes from one argument, so that the fuzzer reaches lists over the
		// window of fifty lines.
		events := make([]orchard.Event, 0, 2*int(copies))
		for range copies {
			events = append(events,
				orchard.Event{Kind: kind1, Payload: payload1, Timestamp: at1},
				orchard.Event{Kind: kind2, Payload: payload2, Timestamp: at2},
			)
		}

		// The cases are the machines that the reaper finishes: one that failed, one that did not
		// boot in time, and one whose agent did not acquire its job in time.
		machine := waiting(ended)

		switch which % 3 {
		case 0:
			machine = failing(ended)
		case 1:
			machine = owned(ended, time.Hour)
			machine.VM.Status = orchard.StatusPending
		}

		machine.VM.StatusMessage = status

		detail := finished(t, &fleet{
			read:     true,
			snapshot: inventory.Snapshot{Owned: []inventory.Owned{machine}},
			events:   events,
		})

		if detail == "" {
			t.Error("the message is empty, and Buildkite refuses an empty detail")
		}

		if len(detail) > buildkite.DetailBudget {
			t.Errorf("the message has %d bytes, want %d or fewer", len(detail), buildkite.DetailBudget)
		}

		if !utf8.ValidString(detail) {
			t.Errorf("the message %q is not whole characters", detail)
		}

		for _, r := range detail {
			if r == '\n' || r == '\t' {
				continue
			}

			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
				t.Fatalf("the message holds the character %U, which a reader cannot see or which "+
					"changes what a reader sees", r)
			}
		}
	})
}
