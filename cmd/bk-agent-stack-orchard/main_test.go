package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
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

// A process with no configuration cannot serve a queue, and the message must say what is
// missing: an operator reads it in a pod that will not start.
func TestRunNeedsAConfiguration(t *testing.T) {
	var out, problems bytes.Buffer

	if status := run(nil, &out, &problems); status != 2 {
		t.Fatalf("run() = %d, want 2", status)
	}

	if !strings.Contains(problems.String(), "-config") {
		t.Errorf("run() reported %q, want it to name the missing flag", problems.String())
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
