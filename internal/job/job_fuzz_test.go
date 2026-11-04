// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package job_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

// ParseID is the only way into the controller for an identifier that it did not create. Whatever
// the text, it accepts only the lowercase UUID shape that Buildkite gives, and the identifier
// keeps the text unchanged.
func FuzzParseIDAcceptsOnlyTheBuildkiteShape(f *testing.F) {
	for _, seed := range []string{
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d",
		"018F3B2C-1C4A-7E5B-9A3D-2F1E4C5B6A7D",
		"018f3b2c1c4a7e5b9a3d2f1e4c5b6a7d",
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d\n",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, text string) {
		id, err := job.ParseID(text)
		if err != nil {
			if id != "" {
				t.Fatalf("ParseID(%q) = %q with an error, want an empty identifier", text, id)
			}

			return
		}

		if !id.Valid() || id.String() != text {
			t.Fatalf("ParseID(%q) = %q, which is not the same valid identifier", text, id)
		}

		if len(text) != 36 {
			t.Fatalf("ParseID(%q) accepted %d characters, want 36", text, len(text))
		}

		for i, r := range text {
			hyphen := i == 8 || i == 13 || i == 18 || i == 23
			hex := r >= '0' && r <= '9' || r >= 'a' && r <= 'f'
			if hyphen && r != '-' || !hyphen && !hex {
				t.Fatalf("ParseID(%q) accepted %q at position %d", text, r, i)
			}
		}
	})
}
