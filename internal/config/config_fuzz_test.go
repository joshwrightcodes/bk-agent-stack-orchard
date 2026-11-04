// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package config_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
)

// The configuration comes from the operator, so a bad file is a mistake, not an attack. Whatever
// the file holds, Load does not panic: it gives an error, or a configuration with at least one
// profile, each of which is valid.
func FuzzLoadGivesAnErrorOrAValidConfiguration(f *testing.F) {
	f.Add(minimal)
	f.Add("controllerID: macos-prod\n")
	f.Add("profiles: [{name: a, match: {a: b}}]\n")
	f.Add("{")
	f.Add("")

	f.Fuzz(func(t *testing.T, body string) {
		cfg, err := config.Load(write(t, body), secrets())
		if err != nil {
			return
		}

		if len(cfg.Profiles) == 0 {
			t.Fatalf("Load(%q) gave a configuration with no profile", body)
		}

		for _, p := range cfg.Profiles {
			if err := p.Validate(); err != nil {
				t.Fatalf("Load(%q) gave the invalid profile %q: %v", body, p.Name, err)
			}
		}
	})
}
