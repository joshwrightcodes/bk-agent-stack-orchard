// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package profile_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

const (
	mirrorURL    = "https://mirror.example.com/buildkite-agent-darwin-arm64-4.10.0.tar.gz"
	mirrorSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestTheDefaultAgentIsADownloadOfAPinnedRelease(t *testing.T) {
	d := profile.DefaultAgent

	if d.Source != profile.AgentDownload {
		t.Errorf("DefaultAgent.Source = %q, want %q", d.Source, profile.AgentDownload)
	}
	if errs := d.Validate(); len(errs) != 0 {
		t.Errorf("DefaultAgent.Validate() = %v, want no error", errs)
	}
}

// The controller default and the image build pin the same release, so an upgrade changes both.
func TestTheDefaultAgentIsTheReleaseThatTheImageBuildPins(t *testing.T) {
	body, err := os.ReadFile("../../images/build.sh")
	if err != nil {
		t.Fatal(err)
	}

	version := regexp.MustCompile(`(?m)^AGENT_VERSION="([0-9.]+)"$`).FindSubmatch(body)
	sha := regexp.MustCompile(`(?m)^AGENT_SHA256="([0-9a-f]{64})"$`).FindSubmatch(body)
	if version == nil || sha == nil {
		t.Fatal("images/build.sh has no AGENT_VERSION or AGENT_SHA256")
	}

	want := "https://github.com/buildkite/agent/releases/download/v" + string(version[1]) +
		"/buildkite-agent-darwin-arm64-" + string(version[1]) + ".tar.gz"
	if profile.DefaultAgent.URL != want {
		t.Errorf("DefaultAgent.URL = %q, want %q, the release of images/build.sh",
			profile.DefaultAgent.URL, want)
	}
	if profile.DefaultAgent.SHA256 != string(sha[1]) {
		t.Errorf("DefaultAgent.SHA256 = %q, want %q, the pin of images/build.sh",
			profile.DefaultAgent.SHA256, sha[1])
	}
}

func TestAnAgentTakesEachUnsetValueFromItsDefaults(t *testing.T) {
	fleet := profile.Agent{Source: profile.AgentDownload, URL: mirrorURL, SHA256: mirrorSHA256}

	cases := map[string]struct {
		agent profile.Agent
		want  profile.Agent
	}{
		"nothing set": {profile.Agent{}, fleet},
		"only the source": {
			profile.Agent{Source: profile.AgentImage},
			profile.Agent{Source: profile.AgentImage, URL: mirrorURL, SHA256: mirrorSHA256},
		},
		"its own pair": {
			profile.Agent{URL: profile.DefaultAgent.URL, SHA256: profile.DefaultAgent.SHA256},
			profile.Agent{
				Source: profile.AgentDownload, URL: profile.DefaultAgent.URL,
				SHA256: profile.DefaultAgent.SHA256,
			},
		},
		// The URL and the SHA-256 are one pair. A half pair must not borrow the other half, or a
		// new URL would be checked against the SHA-256 of the old release.
		"only a URL": {
			profile.Agent{URL: profile.DefaultAgent.URL},
			profile.Agent{Source: profile.AgentDownload, URL: profile.DefaultAgent.URL},
		},
		"only a SHA-256": {
			profile.Agent{SHA256: profile.DefaultAgent.SHA256},
			profile.Agent{Source: profile.AgentDownload, SHA256: profile.DefaultAgent.SHA256},
		},
	}

	for name, c := range cases {
		if got := c.agent.WithDefaults(fleet); got != c.want {
			t.Errorf("%s: WithDefaults() = %+v, want %+v", name, got, c.want)
		}
	}
}

func TestValidateAcceptsEachSource(t *testing.T) {
	for _, agent := range []profile.Agent{
		profile.DefaultAgent,
		{Source: profile.AgentDownload, URL: mirrorURL, SHA256: mirrorSHA256},
		{Source: profile.AgentImage},
		// The image source ignores the pair, so a pair from the defaults does no harm.
		{Source: profile.AgentImage, URL: mirrorURL, SHA256: mirrorSHA256},
	} {
		if errs := agent.Validate(); len(errs) != 0 {
			t.Errorf("Validate(%+v) = %v, want no error", agent, errs)
		}
	}
}

func TestValidateRefusesAnAgentThatCannotStart(t *testing.T) {
	download := func(url, sha string) profile.Agent {
		return profile.Agent{Source: profile.AgentDownload, URL: url, SHA256: sha}
	}

	cases := map[string]struct {
		agent profile.Agent
		want  string
	}{
		"an unknown source":    {profile.Agent{Source: "brew"}, "agent.source"},
		"no source":            {profile.Agent{}, "agent.source"},
		"no URL":               {download("", mirrorSHA256), "agent.url"},
		"a URL without TLS":    {download("http://mirror.example.com/agent.tar.gz", mirrorSHA256), "agent.url"},
		"a URL with no host":   {download("https:///agent.tar.gz", mirrorSHA256), "agent.url"},
		"a URL that is a path": {download("/srv/agent.tar.gz", mirrorSHA256), "agent.url"},
		"no SHA-256":           {download(mirrorURL, ""), "agent.sha256"},
		"a short SHA-256":      {download(mirrorURL, mirrorSHA256[:63]), "agent.sha256"},
		"an upper-case SHA-256": {
			download(mirrorURL, strings.ToUpper(mirrorSHA256)), "agent.sha256",
		},
		"a SHA-256 with a prefix": {download(mirrorURL, "sha256:"+mirrorSHA256[7:]), "agent.sha256"},
	}

	for name, c := range cases {
		errs := c.agent.Validate()
		if len(errs) == 0 {
			t.Errorf("%s: Validate() = nil, want an error about %s", name, c.want)
			continue
		}
		if !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%s: Validate() = %v, want an error about %s", name, errs, c.want)
		}
	}
}

// The configuration resolves the agent before it validates a profile, so a profile that has no
// source yet is one that the configuration has not resolved, and Validate leaves it alone.
func TestProfileValidateChecksTheAgentOnceItHasASource(t *testing.T) {
	p := valid()
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() with no agent = %v, want no error", err)
	}

	p.Agent = profile.Agent{Source: profile.AgentDownload, URL: "http://mirror.example.com/a.tar.gz"}
	err := p.Validate()
	if err == nil || !strings.Contains(err.Error(), `profile "xcode-27": agent.url`) {
		t.Errorf("Validate() = %v, want an error about agent.url of the profile", err)
	}
}
