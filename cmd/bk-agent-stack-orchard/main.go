// Command bk-agent-stack-orchard runs Buildkite macOS jobs in temporary Tart virtual machines
// through Orchard.
//
// This is a stub. It proves that the repository builds, lints, tests, and releases. The
// controller behaviour arrives with the walking skeleton.
package main

import (
	"fmt"
	"io"
	"os"
)

// The release build sets these through ldflags. A development build leaves them empty.
var (
	version string
	commit  string
)

func run(out io.Writer) int {
	_, err := fmt.Fprintf(out, "bk-agent-stack-orchard %s (%s)\n", orUnknown(version), orUnknown(commit))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bk-agent-stack-orchard: cannot write the version: %v\n", err)

		return 1
	}

	return 0
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}

func main() {
	os.Exit(run(os.Stdout))
}
