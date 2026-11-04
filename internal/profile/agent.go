// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package profile

import (
	"fmt"
	"net/url"
	"regexp"
)

// AgentSource selects where the buildkite-agent of a virtual machine comes from.
type AgentSource string

const (
	// AgentDownload makes the startup script download a pinned release, check it, and run it.
	// The image then needs no agent, so an upstream image with the guest agent serves a job.
	AgentDownload AgentSource = "download"

	// AgentImage runs the agent that the image holds. The script refuses one that is not v4.
	AgentImage AgentSource = "image"
)

// Agent says where the agent of a virtual machine comes from. URL and SHA256 are one pair: they
// name one release archive, and the script refuses an archive that does not match.
type Agent struct {
	// download: the VM downloads the archive at url, checks it against sha256, and runs its
	// agent. image: the VM runs the agent of the image, which must be v4.
	Source AgentSource `yaml:"source,omitempty"`

	// The https URL of a buildkite-agent release archive for macOS on arm64. Set it with sha256.
	URL string `yaml:"url,omitempty"`

	// The SHA-256 of the archive at url: 64 lower-case hexadecimal characters, in quotes. Set it
	// with url.
	SHA256 string `yaml:"sha256,omitempty"`
}

// DefaultAgent is the release that this controller release was tested with. images/build.sh pins
// the same release, and a test keeps the two equal.
var DefaultAgent = Agent{
	Source: AgentDownload,
	URL:    "https://github.com/buildkite/agent/releases/download/v4.9.0/buildkite-agent-darwin-arm64-4.9.0.tar.gz",
	SHA256: "faaf0084edf08a92a877253620604fbd6ce496d2851a7ae5126dd19f27429e76",
}

var sha256Format = regexp.MustCompile(`^[0-9a-f]{64}$`)

// WithDefaults fills what this agent leaves unset. The URL and the SHA-256 come from the defaults
// only when both are unset, because half of one pair and half of another would check a new
// archive against the SHA-256 of an old one.
func (a Agent) WithDefaults(defaults Agent) Agent {
	if a.Source == "" {
		a.Source = defaults.Source
	}
	if a.URL == "" && a.SHA256 == "" {
		a.URL, a.SHA256 = defaults.URL, defaults.SHA256
	}

	return a
}

// Validate reports every rule that this agent breaks.
func (a Agent) Validate() []error {
	switch a.Source {
	case AgentImage:
		return nil
	case AgentDownload:
	default:
		return []error{fmt.Errorf("agent.source %q must be %q or %q", a.Source, AgentDownload, AgentImage)}
	}

	var problems []error

	// The archive becomes the program that holds the job token, so it travels only over TLS.
	if u, err := url.Parse(a.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		problems = append(problems, fmt.Errorf(
			"agent.url %q must be an https URL of a buildkite-agent release archive", a.URL))
	}
	if !sha256Format.MatchString(a.SHA256) {
		problems = append(problems, fmt.Errorf(
			"agent.sha256 %q must be the SHA-256 of the archive at agent.url: 64 lower-case hexadecimal "+
				"characters, in quotes, because YAML reads a value of digits as a number", a.SHA256))
	}

	return problems
}
