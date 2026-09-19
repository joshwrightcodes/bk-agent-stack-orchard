package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunPrintsTheVersionAndTheCommit(t *testing.T) {
	version, commit = "1.2.3", "abc1234"

	var out bytes.Buffer
	if status := run(&out); status != 0 {
		t.Fatalf("run() = %d, want 0", status)
	}

	got := strings.TrimSpace(out.String())
	want := "bk-agent-stack-orchard 1.2.3 (abc1234)"
	if got != want {
		t.Errorf("run() wrote %q, want %q", got, want)
	}
}

func TestRunReportsAnUnknownVersionWhenTheBuildDidNotSetOne(t *testing.T) {
	version, commit = "", ""

	var out bytes.Buffer
	if status := run(&out); status != 0 {
		t.Fatalf("run() = %d, want 0", status)
	}

	if got := out.String(); !strings.Contains(got, "unknown") {
		t.Errorf("run() wrote %q, want it to contain %q", got, "unknown")
	}
}

// failingWriter reports an error on every write, as a closed pipe does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("the pipe is closed")
}

func TestRunReportsAFailedWrite(t *testing.T) {
	version, commit = "1.2.3", "abc1234"

	if status := run(failingWriter{}); status != 1 {
		t.Errorf("run() = %d, want 1 when the writer fails", status)
	}
}
