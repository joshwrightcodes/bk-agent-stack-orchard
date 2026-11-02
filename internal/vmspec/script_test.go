// SPDX-License-Identifier: AGPL-3.0-or-later

package vmspec_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// scriptRun is what one run of the startup script did.
type scriptRun struct {
	sudo   []string // the commands that the script gave sudo, one for each line
	tags   string   // BUILDKITE_AGENT_TAGS as the agent got it, or "" when the agent did not start
	stderr string
	exit   int
}

// runScript runs the startup script as Orchard does, with set -e before it, in each shell that a
// macOS guest can use. A fake sudo and a fake buildkite-agent on the PATH record what the script
// did. imageTags is the content of the tag file of the image, and nil means no file.
func runScript(t *testing.T, shell string, imageTags *string, sudoFails bool, agentExit string) scriptRun {
	t.Helper()

	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("sudo", "#!/bin/sh\n"+
		`[ -n "$FAKE_SUDO_FAILS" ] && { echo "sudo: a password is required" >&2; exit 1; }`+"\n"+
		`printf '%s\n' "$*" >>"$FAKE_DIR/sudo.log"`+"\n", 0o755)
	write("buildkite-agent", "#!/bin/sh\n"+
		`printf '%s' "$BUILDKITE_AGENT_TAGS" >"$FAKE_DIR/tags"`+"\n"+
		`exit "${FAKE_AGENT_EXIT:-0}"`+"\n", 0o755)

	tagFile := filepath.Join(dir, "agent-tags")
	if imageTags != nil {
		write("agent-tags", *imageTags, 0o644)
	}

	script := build(t, request()).StartupScript.ScriptContent
	if strings.Count(script, vmspec.ImageTagsPath) != 2 {
		t.Fatalf("the script names %s %d times, want 2: %q",
			vmspec.ImageTagsPath, strings.Count(script, vmspec.ImageTagsPath), script)
	}
	script = strings.ReplaceAll(script, vmspec.ImageTagsPath, tagFile)

	//nolint:gosec // G204: the test runs the startup script on purpose, in a shell that it found.
	cmd := exec.CommandContext(t.Context(), shell, "-c", "set -e\n"+script)
	cmd.Env = []string{
		"PATH=" + dir + ":/usr/bin:/bin",
		"FAKE_DIR=" + dir,
		"FAKE_AGENT_EXIT=" + agentExit,
		"BUILDKITE_AGENT_NAME=bk-macos-prod-" + jobUUID.String(),
		"BUILDKITE_AGENT_TAGS=queue=macos,agent-stack=orchard,profile=xcode-27,xcode=27",
	}
	if sudoFails {
		cmd.Env = append(cmd.Env, "FAKE_SUDO_FAILS=1")
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	run := scriptRun{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", shell, err)
		}
		run.exit = exit.ExitCode()
	}
	run.stderr = stderr.String()
	if log, err := readFake(dir, "sudo.log"); err == nil {
		run.sudo = strings.Split(strings.TrimSuffix(string(log), "\n"), "\n")
	}
	if tags, err := readFake(dir, "tags"); err == nil {
		run.tags = string(tags)
	}

	return run
}

// readFake reads a file that a fake wrote in the directory of the test.
func readFake(dir, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: the directory is t.TempDir.
}

// shells gives the shells that the script must work in. Orchard runs the script with the shell of
// the guest, which is zsh on macOS, and sh is the floor. Elsewhere a missing shell skips its runs.
// On macOS in CI, which is the gate (mise run test-startup-script), it fails the test.
func shells(t *testing.T) []string {
	t.Helper()

	var found []string
	for _, name := range []string{"sh", "zsh"} {
		path, err := exec.LookPath(name)
		switch {
		case err == nil:
			found = append(found, path)
		case runtime.GOOS == "darwin" && os.Getenv("CI") != "":
			t.Fatalf("this macOS runner has no %s, and the startup script must be tested in it", name)
		}
	}
	if len(found) == 0 {
		t.Skip("no sh or zsh on this computer")
	}

	return found
}

func text(s string) *string { return &s }

const controllerTags = "queue=macos,agent-stack=orchard,profile=xcode-27,xcode=27"

// Every machine is a clone of one image, so without this each agent shows the host name of the
// image, and Buildkite cannot tell the machines apart.
func TestTheScriptNamesTheHostAfterTheMachine(t *testing.T) {
	for _, shell := range shells(t) {
		run := runScript(t, shell, nil, false, "0")

		name := "bk-macos-prod-" + jobUUID.String()
		want := []string{
			"-n /usr/sbin/scutil --set HostName " + name,
			"-n /usr/sbin/scutil --set LocalHostName " + name,
			"-n /usr/sbin/scutil --set ComputerName " + name,
		}
		if strings.Join(run.sudo, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: sudo got %q, want %q", shell, run.sudo, want)
		}
		if run.tags != controllerTags || run.exit != 0 {
			t.Errorf("%s: the agent got %q and the script exited %d, want %q and 0",
				shell, run.tags, run.exit, controllerTags)
		}
	}
}

// A host name is a convenience, so an image without passwordless sudo still runs its job.
func TestTheScriptStartsTheAgentWhenTheHostCannotBeNamed(t *testing.T) {
	for _, shell := range shells(t) {
		run := runScript(t, shell, nil, true, "0")

		if run.tags != controllerTags || run.exit != 0 {
			t.Errorf("%s: the agent got %q and the script exited %d, want %q and 0",
				shell, run.tags, run.exit, controllerTags)
		}
		if !strings.Contains(run.stderr, "the host name stays that of the image") {
			t.Errorf("%s: stderr = %q, want the line that says the host keeps its name", shell, run.stderr)
		}
	}
}

// The tags of the image come after those of the controller, in the order of the file. A tag that
// would change a tag of the controller or of the job, or that a tag list cannot carry, stays out.
func TestTheScriptAddsTheTagsOfTheImage(t *testing.T) {
	file := strings.Join([]string{
		"macos=27.2",
		"xcode=26.0",    // the job asked for xcode=27, and the controller set it
		"queue=other",   // a tag of the controller
		"profile=other", // a tag of the controller
		"image-name=bkaso-pilot",
		"",                   // an empty line
		"# a comment",        // a comment
		"no-value",           // no =
		"=no-key",            // no key
		"gpu=yes,size=large", // a comma would make two tags
		"note=has space",     // white space
		`quote="x"`,          // a quote
		"image-tag=ghcr.io/acme/bkaso@sha256:abc+1", // the characters of a reference
		"xcode-build=17C52",                         // the last line has no newline
	}, "\n")

	want := controllerTags + ",macos=27.2,image-name=bkaso-pilot," +
		"image-tag=ghcr.io/acme/bkaso@sha256:abc+1,xcode-build=17C52"

	for _, shell := range shells(t) {
		if run := runScript(t, shell, text(file), false, "0"); run.tags != want {
			t.Errorf("%s: the agent got\n%q, want\n%q", shell, run.tags, want)
		}
	}
}

func TestTheScriptUsesTheControllerTagsWhenTheImageHasNoTagFile(t *testing.T) {
	for _, shell := range shells(t) {
		for name, file := range map[string]*string{"no file": nil, "an empty file": text("")} {
			if run := runScript(t, shell, file, false, "0"); run.tags != controllerTags {
				t.Errorf("%s, %s: the agent got %q, want %q", shell, name, run.tags, controllerTags)
			}
		}
	}
}

// Orchard fails a machine whose script exits with a status that is not 0, so the status of the
// agent must reach Orchard unchanged.
func TestTheScriptExitsWithTheStatusOfTheAgent(t *testing.T) {
	for _, shell := range shells(t) {
		if run := runScript(t, shell, text("macos=27.2\n"), false, "27"); run.exit != 27 {
			t.Errorf("%s: the script exited %d, want 27, the status of the agent", shell, run.exit)
		}
	}
}
