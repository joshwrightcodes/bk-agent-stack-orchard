// SPDX-License-Identifier: AGPL-3.0-or-later

package match_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/match"
)

// The agent query rules come from pipeline authors, and every build is untrusted. Whatever they
// write, Tags gives only literal key=value pairs that a rule names, or a mismatch, and Match gives
// only a profile whose match map is equal to those pairs.
func FuzzTagsGiveOnlyLiteralRules(f *testing.F) {
	for _, seed := range []string{
		"queue=macos\nxcode=27",
		"queue=macos\nxcode=27\nsize=large",
		"xcode=*",
		"!xcode=27",
		"xcode=27\nxcode=26",
		"xcode",
		"=27",
		"queue=macos",
		"a=b=c",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, text string) {
		rules := strings.Split(text, "\n")

		tags, err := match.Tags(rules)
		if err != nil {
			var mismatch *match.Mismatch
			if !errors.As(err, &mismatch) {
				t.Fatalf("Tags(%q) = %v, want a *Mismatch", rules, err)
			}

			return
		}

		for key, value := range tags {
			switch {
			case key == "", value == "":
				t.Fatalf("Tags(%q) gave the empty pair %q=%q", rules, key, value)
			case strings.ContainsAny(key, "*?!"), strings.ContainsAny(value, "*?"):
				t.Fatalf("Tags(%q) gave the pattern %q=%q", rules, key, value)
			case key == "queue":
				t.Fatalf("Tags(%q) kept the queue rule", rules)
			case !slices.Contains(rules, key+"="+value):
				t.Fatalf("Tags(%q) gave %q=%q, which no rule names", rules, key, value)
			}
		}

		found, err := match.Match(rules, profiles())
		if err == nil && !maps.Equal(found.Match, tags) {
			t.Fatalf("Match(%q) = %q with %v, want a profile whose match map is %v",
				rules, found.Name, found.Match, tags)
		}
	})
}
