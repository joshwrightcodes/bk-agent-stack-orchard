// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionPrintsTheVersionAndTheCommit(t *testing.T) {
	version, commit = "1.2.3", "abc1234"

	var out, problems bytes.Buffer
	if status := run([]string{"-version"}, &out, &problems); status != 0 {
		t.Fatalf("run() = %d, want 0", status)
	}

	got := strings.TrimSpace(out.String())
	want := "bk-agent-stack-orchard 1.2.3 (abc1234)"

	if got != want {
		t.Errorf("run() wrote %q, want %q", got, want)
	}
}

func TestVersionReportsUnknownWhenTheBuildDidNotSetOne(t *testing.T) {
	version, commit = "", ""

	var out, problems bytes.Buffer
	if status := run([]string{"-version"}, &out, &problems); status != 0 {
		t.Fatalf("run() = %d, want 0", status)
	}

	if got := out.String(); !strings.Contains(got, "unknown") {
		t.Errorf("run() wrote %q, want it to contain %q", got, "unknown")
	}
}

// The platform sets the path of the configuration in the environment, so a process that gets no
// flag must read the file that the variable names.
func TestTheConfigPathComesFromTheEnvironmentWhenTheFlagIsMissing(t *testing.T) {
	environment := map[string]string{"BK_AGENT_STACK_ORCHARD_CONFIG": "/from/the/environment.yaml"}

	if got, _ := configPath("", func(key string) string { return environment[key] }); got !=
		"/from/the/environment.yaml" {
		t.Errorf("configPath() = %q, want the path of the environment", got)
	}
}

func TestTheDefaultConfigPath(t *testing.T) {
	got, _ := configPath("", func(string) string { return "" })

	if want := "/etc/bk-agent-stack-orchard/config.yaml"; got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

func TestTheFlagWinsOverTheEnvironment(t *testing.T) {
	environment := map[string]string{"BK_AGENT_STACK_ORCHARD_CONFIG": "/from/the/environment.yaml"}

	if got, _ := configPath("/from/the/flag.yaml", func(key string) string {
		return environment[key]
	}); got != "/from/the/flag.yaml" {
		t.Errorf("configPath() = %q, want the path of the flag", got)
	}
}

// A process with no flag reads the path of the environment, and a file that is not there must
// stop it with a message that names the configuration.
func TestRunReadsTheConfigurationThatTheEnvironmentNames(t *testing.T) {
	var out, problems bytes.Buffer

	missing := filepath.Join(t.TempDir(), "nothing.yaml")
	t.Setenv("BK_AGENT_STACK_ORCHARD_CONFIG", missing)

	if status := run(nil, &out, &problems); status != 1 {
		t.Fatalf("run() = %d, want 1", status)
	}

	if !strings.Contains(problems.String(), missing) {
		t.Errorf("run() reported %q, want it to name %q", problems.String(), missing)
	}
}

func TestRunRefusesAFlagThatDoesNotExist(t *testing.T) {
	var out, problems bytes.Buffer

	if status := run([]string{"-wat"}, &out, &problems); status != 2 {
		t.Errorf("run() = %d, want 2", status)
	}
}

// A configuration that the process cannot read is a failure of the deployment, and it must stop
// the process rather than start a controller with the defaults.
func TestRunStopsWhenTheConfigurationIsNotThere(t *testing.T) {
	var out, problems bytes.Buffer

	missing := filepath.Join(t.TempDir(), "nothing.yaml")
	if status := run([]string{"-config", missing}, &out, &problems); status != 1 {
		t.Fatalf("run() = %d, want 1", status)
	}

	if !strings.Contains(problems.String(), "configuration") {
		t.Errorf("run() reported %q, want it to name the configuration", problems.String())
	}
}

// failingWriter reports an error on every write, as a closed pipe does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("the pipe is closed")
}

func TestRunReportsAWriteThatFails(t *testing.T) {
	var problems bytes.Buffer

	if status := run([]string{"-version"}, failingWriter{}, &problems); status != 1 {
		t.Errorf("run() = %d, want 1", status)
	}
}

// An operator who reads a failed start must learn where the path came from, because the file that
// the pod reads is not the file that the operator expects when the source is a surprise.
func TestTheStartErrorNamesWhereTheConfigPathCameFrom(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing.yaml")

	tests := []struct {
		name  string
		args  []string
		env   string
		other string
	}{
		{"flag", []string{"-config", missing}, "", "the -config flag"},
		{"environment", nil, missing, "the environment variable BK_AGENT_STACK_ORCHARD_CONFIG"},
		{"default", nil, "", "the default path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, problems bytes.Buffer

			t.Setenv("BK_AGENT_STACK_ORCHARD_CONFIG", tt.env)

			if status := run(tt.args, &out, &problems); status != 1 {
				t.Fatalf("run() = %d, want 1", status)
			}

			if !strings.Contains(problems.String(), tt.other) {
				t.Errorf("run() reported %q, want it to name %q", problems.String(), tt.other)
			}
		})
	}
}

// A server that accepts a call and never answers must not hold the loop for ever.
func TestTheClientsOfTheControllerCarryATimeout(t *testing.T) {
	for name, client := range map[string]*http.Client{
		"buildkite": newHTTPClient(nil),
		"orchard":   newHTTPClient(http.DefaultTransport),
	} {
		if client.Timeout != httpTimeout || httpTimeout <= 0 {
			t.Errorf("the %s client has timeout %s, want %s", name, client.Timeout, httpTimeout)
		}
	}

	if httpTimeout != 30*time.Second {
		t.Errorf("httpTimeout = %s, want 30s", httpTimeout)
	}
}
