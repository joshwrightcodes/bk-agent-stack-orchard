// SPDX-License-Identifier: AGPL-3.0-or-later

package reaper_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// A guest writes the lines of its startup script, and the finish message of a job quotes the last
// ones. Whatever the events hold, the message is never empty, it fits in the budget that the
// library of Buildkite sends unchanged, it is whole characters, and it holds no control character
// except the line breaks and the tabs of the lines.
func FuzzTheFinishMessageOfAnyVMEvents(f *testing.F) {
	f.Add("log_line", "agent started", int64(1), "log_line", "agent exited", int64(2), uint8(3))
	f.Add("log_line", "", int64(0), "log_line", "", int64(0), uint8(0))
	f.Add("status", "not a line", int64(5), "log_line", "line", int64(-5), uint8(1))
	f.Add("log_line", "colour \x1b[31mred\x1b[0m\r\n", int64(1), "log_line", "bell \a", int64(1), uint8(2))
	f.Add("log_line", "ビ"+strings.Repeat("ビ", 3000), int64(7), "log_line", "x", int64(8), uint8(2))
	f.Add("log_line", strings.Repeat("x", 5000), int64(1), "log_line", "\u0085\u2028\u202e", int64(2), uint8(40))
	f.Add("", "", int64(1<<62), "", "", int64(-1<<62), uint8(255))

	f.Fuzz(func(t *testing.T, kind1, payload1 string, at1 int64, kind2, payload2 string, at2 int64,
		copies uint8,
	) {
		// Orchard gives events as JSON, and the decoder of JSON gives whole characters only.
		for _, text := range []string{kind1, payload1, kind2, payload2} {
			if !utf8.ValidString(text) {
				t.Skip("an event of Orchard holds whole characters")
			}
		}

		// The count of events comes from the last argument, so that the fuzzer reaches lists over
		// the window of fifty lines.
		events := make([]orchard.Event, 0, 2*int(copies))
		for range copies {
			events = append(events,
				orchard.Event{Kind: kind1, Payload: payload1, Timestamp: at1},
				orchard.Event{Kind: kind2, Payload: payload2, Timestamp: at2},
			)
		}

		detail := finished(t, &fleet{
			read:     true,
			snapshot: inventory.Snapshot{Owned: []inventory.Owned{waiting(ended)}},
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
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				t.Fatalf("the message holds the control character %U", r)
			}
		}
	})
}
