// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package config_test

import (
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

const (
	mirrorURL    = "https://mirror.example.com/buildkite-agent-darwin-arm64-4.10.0.tar.gz"
	mirrorSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// withProfileAgent adds lines to the only profile of the minimal configuration.
func withProfileAgent(lines string) string { return minimal + lines }

// withDefaultsAgent adds a defaults block to the minimal configuration.
func withDefaultsAgent(lines string) string { return minimal + "defaults:\n  agent:\n" + lines }

func TestAProfileDownloadsTheDefaultAgentWhenNothingSaysOtherwise(t *testing.T) {
	cfg, err := load(t, minimal, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	if got := cfg.Profiles[0].Agent; got != profile.DefaultAgent {
		t.Errorf("Agent = %+v, want the default %+v", got, profile.DefaultAgent)
	}
}

func TestTheDefaultsOfTheFileApplyToEachProfile(t *testing.T) {
	cfg, err := load(t, withDefaultsAgent("    url: "+mirrorURL+"\n    sha256: "+mirrorSHA256+"\n"), secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	want := profile.Agent{Source: profile.AgentDownload, URL: mirrorURL, SHA256: mirrorSHA256}
	if got := cfg.Profiles[0].Agent; got != want {
		t.Errorf("Agent = %+v, want %+v", got, want)
	}
}

func TestAProfileCanUseTheAgentOfItsImage(t *testing.T) {
	cfg, err := load(t, withProfileAgent("    agent: {source: image}\n"), secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	if got := cfg.Profiles[0].Agent.Source; got != profile.AgentImage {
		t.Errorf("Agent.Source = %q, want %q", got, profile.AgentImage)
	}
}

func TestAProfilePairOverridesTheDefaults(t *testing.T) {
	body := withProfileAgent("    agent: {url: \""+mirrorURL+"\", sha256: \""+mirrorSHA256+"\"}\n") +
		"defaults:\n  agent:\n    source: image\n"

	cfg, err := load(t, body, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	want := profile.Agent{Source: profile.AgentImage, URL: mirrorURL, SHA256: mirrorSHA256}
	if got := cfg.Profiles[0].Agent; got != want {
		t.Errorf("Agent = %+v, want %+v: the source of the defaults, and the pair of the profile", got, want)
	}
}

func TestLoadRefusesAnAgentThatCannotStart(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"an unknown source in a profile": {
			withProfileAgent("    agent: {source: brew}\n"), `profile "xcode-27": agent.source`,
		},
		"an unknown source in the defaults": {
			withDefaultsAgent("    source: brew\n"), "defaults.agent: agent.source",
		},
		"a URL without its SHA-256": {
			withProfileAgent("    agent: {url: \"" + mirrorURL + "\"}\n"), "agent.sha256",
		},
		"a SHA-256 without its URL in the defaults": {
			withDefaultsAgent("    sha256: " + mirrorSHA256 + "\n"), "agent.url",
		},
		// YAML reads a value of digits as a number, so the message says to quote it.
		"a SHA-256 of digits without quotes": {
			withProfileAgent("    agent: {url: \"" + mirrorURL + "\", sha256: " + strings.Repeat("0", 64) + "}\n"),
			"in quotes",
		},
		"a URL without TLS": {
			withProfileAgent("    agent: {url: \"http://mirror.example.com/a.tar.gz\", sha256: \"" +
				mirrorSHA256 + "\"}\n"), "agent.url",
		},
	}

	for name, c := range cases {
		_, err := load(t, c.body, secrets())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Load() = %v, want an error about %s", name, err, c.want)
		}
	}
}
