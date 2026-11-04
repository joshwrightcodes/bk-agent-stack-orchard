// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package vmspec_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// scriptRun is what one run of the startup script did.
type scriptRun struct {
	sudo  []string // the commands that the script gave sudo, one for each line
	tags  string   // BUILDKITE_AGENT_TAGS as the agent got it, or "" when no agent started
	agent string   // which agent started: "downloaded", "image", or "" for none
	paths string   // the build, hooks, and plugins paths that the agent got, joined by "|"
	url   string   // the URL that curl got, or "" when the script did not download
	home  string   // HOME of the run
	// helpers are the credential.helper values of ~/.gitconfig after the run, one for each line.
	helpers []string
	stderr  string
	exit    int
}

// scriptCase is one run of the startup script.
type scriptCase struct {
	source       profile.AgentSource // the default is a download
	imageTags    *string             // the tag file of the image, and nil for no file
	sudoFails    bool
	agentExit    string  // the exit status of the agent that starts
	download     string  // "" for a good archive, "fails", "wrong-sha", or "no-agent"
	imageVersion string  // what the agent of the image prints for --version
	gitconfig    *string // ~/.gitconfig of the image, and nil for no file
}

// fakeAgent records what it got, so a test can tell which agent started and with what.
func fakeAgent(name string) string {
	return "#!/bin/sh\n" +
		`if [ "$1" = --version ]; then echo "$FAKE_IMAGE_VERSION"; exit 0; fi` + "\n" +
		`printf '%s' "$BUILDKITE_AGENT_TAGS" >"$FAKE_DIR/tags"` + "\n" +
		`printf '%s' "` + name + `" >"$FAKE_DIR/agent"` + "\n" +
		`printf '%s|%s|%s' "$BUILDKITE_BUILD_PATH" "$BUILDKITE_HOOKS_PATH" "$BUILDKITE_PLUGINS_PATH" >"$FAKE_DIR/paths"` + "\n" +
		`exit "${FAKE_AGENT_EXIT:-0}"` + "\n"
}

// archive writes a release archive in the shape of a Buildkite release: the agent, its sample
// configuration, and its sample hooks, each under ./.
func archive(t *testing.T, path string, withAgent bool) {
	t.Helper()

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	tw := tar.NewWriter(zw)
	files := map[string]string{
		"./buildkite-agent.cfg":      "build-path=\"$HOME/.buildkite-agent/builds\"\n",
		"./hooks/environment.sample": "#!/bin/sh\n",
		"./buildkite-agent":          fakeAgent("downloaded"),
		"./bootstrap.sh":             "#!/bin/sh\n",
	}
	if !withAgent {
		delete(files, "./buildkite-agent")
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(files[name])), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

const archiveURL = "https://mirror.example.com/buildkite-agent-darwin-arm64-4.9.0.tar.gz"

// runScript runs the startup script as Orchard does, with set -e and the export lines of the
// environment before it, in one shell that a macOS guest can use. Fakes on the PATH record what
// the script did: sudo, curl, and an agent of the image that is v3 unless the case says otherwise.
// shasum and tar are the real tools.
func runScript(t *testing.T, shell string, c scriptCase) scriptRun {
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
	write("curl", "#!/bin/sh\n"+
		`out=; url=`+"\n"+
		`while [ $# -gt 0 ]; do case $1 in -o) out=$2; shift 2 ;; *) url=$1; shift ;; esac; done`+"\n"+
		`printf '%s' "$url" >"$FAKE_DIR/url"`+"\n"+
		`[ -n "$FAKE_CURL_FAILS" ] && { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }`+"\n"+
		`cp "$FAKE_DIR/archive.tar.gz" "$out"`+"\n", 0o755)
	write("buildkite-agent", fakeAgent("image"), 0o755)
	for _, sub := range []string{"home", "tmp"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	archive(t, filepath.Join(dir, "archive.tar.gz"), c.download != "no-agent")
	body, err := readFake(dir, "archive.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	if c.download == "wrong-sha" {
		sum = fmt.Sprintf("%x", sha256.Sum256([]byte("another archive")))
	}

	if c.gitconfig != nil {
		write(filepath.Join("home", ".gitconfig"), *c.gitconfig, 0o644)
	}

	tagFile := filepath.Join(dir, "agent-tags")
	if c.imageTags != nil {
		write("agent-tags", *c.imageTags, 0o644)
	}

	r := request()
	r.Tags = map[string]string{"xcode": "27"}
	r.Profile.Agent = profile.Agent{Source: c.source, URL: archiveURL, SHA256: sum}
	if c.source == "" {
		r.Profile.Agent.Source = profile.AgentDownload
	}
	vm := build(t, r)

	script := vm.StartupScript.ScriptContent
	if strings.Count(script, vmspec.ImageTagsPath) != 2 {
		t.Fatalf("the script names %s %d times, want 2: %q",
			vmspec.ImageTagsPath, strings.Count(script, vmspec.ImageTagsPath), script)
	}
	script = strings.ReplaceAll(script, vmspec.ImageTagsPath, tagFile)

	// Orchard writes one export line for each variable of the specification before the script.
	exports := ""
	for _, key := range slices.Sorted(maps.Keys(vm.StartupScript.Env)) {
		exports += "export " + key + "='" + vm.StartupScript.Env[key] + "'\n"
	}

	version := c.imageVersion
	if version == "" {
		version = "buildkite-agent version 3.138.2+15170.cb60cb2f02cadb2f0bf151f7cf1cee730a45a4b7"
	}

	//nolint:gosec // G204: the test runs the startup script on purpose, in a shell that it found.
	cmd := exec.CommandContext(t.Context(), shell, "-c", "set -e\n"+exports+script)
	cmd.Env = []string{
		"PATH=" + dir + ":/usr/bin:/bin",
		"HOME=" + filepath.Join(dir, "home"),
		"TMPDIR=" + filepath.Join(dir, "tmp"),
		"FAKE_DIR=" + dir,
		"FAKE_AGENT_EXIT=" + c.agentExit,
		"FAKE_IMAGE_VERSION=" + version,
	}
	if c.sudoFails {
		cmd.Env = append(cmd.Env, "FAKE_SUDO_FAILS=1")
	}
	if c.download == "fails" {
		cmd.Env = append(cmd.Env, "FAKE_CURL_FAILS=1")
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	run := scriptRun{home: filepath.Join(dir, "home")}
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
	if c.gitconfig != nil {
		//nolint:gosec // G204: the test reads the file of the run with the real git.
		out, err := exec.CommandContext(t.Context(), "git", "config", "--file",
			filepath.Join(run.home, ".gitconfig"), "--get-all", "credential.helper").Output()
		if err == nil {
			run.helpers = strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		}
	}
	for field, name := range map[*string]string{&run.tags: "tags", &run.agent: "agent", &run.paths: "paths", &run.url: "url"} {
		if value, err := readFake(dir, name); err == nil {
			*field = string(value)
		}
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
		run := runScript(t, shell, scriptCase{})

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
		run := runScript(t, shell, scriptCase{sudoFails: true})

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
		if run := runScript(t, shell, scriptCase{imageTags: text(file)}); run.tags != want {
			t.Errorf("%s: the agent got\n%q, want\n%q", shell, run.tags, want)
		}
	}
}

func TestTheScriptUsesTheControllerTagsWhenTheImageHasNoTagFile(t *testing.T) {
	for _, shell := range shells(t) {
		for name, file := range map[string]*string{"no file": nil, "an empty file": text("")} {
			if run := runScript(t, shell, scriptCase{imageTags: file}); run.tags != controllerTags {
				t.Errorf("%s, %s: the agent got %q, want %q", shell, name, run.tags, controllerTags)
			}
		}
	}
}

// Orchard fails a machine whose script exits with a status that is not 0, so the status of the
// agent must reach Orchard unchanged.
func TestTheScriptExitsWithTheStatusOfTheAgent(t *testing.T) {
	for _, shell := range shells(t) {
		if run := runScript(t, shell, scriptCase{imageTags: text("macos=27.2\n"), agentExit: "27"}); run.exit != 27 {
			t.Errorf("%s: the script exited %d, want 27, the status of the agent", shell, run.exit)
		}
	}
}

// The image of an upstream base has agent v3 first on the PATH. A download must run its own
// agent, with paths that no configuration file of the image can move.
func TestTheScriptRunsTheDownloadedAgent(t *testing.T) {
	for _, shell := range shells(t) {
		run := runScript(t, shell, scriptCase{})

		if run.exit != 0 || run.agent != "downloaded" {
			t.Fatalf("%s: the script exited %d and started %q, want 0 and the downloaded agent; stderr: %s",
				shell, run.exit, run.agent, run.stderr)
		}
		if run.url != archiveURL {
			t.Errorf("%s: curl got %q, want %q", shell, run.url, archiveURL)
		}
		want := run.home + "/buildkite/builds|" + run.home + "/.buildkite-agent/hooks|" + run.home + "/buildkite/plugins"
		if run.paths != want {
			t.Errorf("%s: the agent got the paths %q, want %q", shell, run.paths, want)
		}
		if run.tags != controllerTags {
			t.Errorf("%s: the agent got %q, want %q", shell, run.tags, controllerTags)
		}
	}
}

// A step that fails stops the script before any agent starts, and its line names the step, so
// the job page says why.
func TestTheScriptStartsNoAgentWhenTheDownloadFails(t *testing.T) {
	cases := map[string]string{
		"fails":     "could not download buildkite-agent from " + archiveURL,
		"wrong-sha": "does not match agent.sha256",
		"no-agent":  "holds no buildkite-agent",
	}

	for _, shell := range shells(t) {
		for download, want := range cases {
			run := runScript(t, shell, scriptCase{download: download})

			if run.exit == 0 || run.agent != "" {
				t.Errorf("%s, %s: the script exited %d and started %q, want a failure and no agent",
					shell, download, run.exit, run.agent)
			}
			if !strings.Contains(run.stderr, want) {
				t.Errorf("%s, %s: stderr = %q, want %q", shell, download, run.stderr, want)
			}
		}
	}
}

func TestTheScriptRunsTheAgentOfTheImageWhenItIsV4(t *testing.T) {
	for _, shell := range shells(t) {
		run := runScript(t, shell, scriptCase{
			source:       profile.AgentImage,
			imageVersion: "buildkite-agent version 4.9.0+15270.735f98f3de5a29a9b3b60a36bbcef4f4e1d9ba17",
		})

		if run.exit != 0 || run.agent != "image" || run.url != "" {
			t.Errorf("%s: the script exited %d, started %q, and downloaded %q, want 0, the agent of "+
				"the image, and no download; stderr: %s", shell, run.exit, run.agent, run.url, run.stderr)
		}
		if run.paths != "||" {
			t.Errorf("%s: the agent got the paths %q, want none, so the image decides", shell, run.paths)
		}
	}
}

// The upstream base image carries agent v3, which this controller does not support.
func TestTheScriptRefusesAnImageAgentThatIsNotV4(t *testing.T) {
	for _, shell := range shells(t) {
		for _, version := range []string{
			"buildkite-agent version 3.138.2+15170.cb60cb2f02cadb2f0bf151f7cf1cee730a45a4b7",
			"buildkite-agent version 5.0.0",
			"buildkite-agent version 40.1.0",
		} {
			run := runScript(t, shell, scriptCase{source: profile.AgentImage, imageVersion: version})

			if run.exit == 0 || run.agent != "" {
				t.Errorf("%s, %s: the script exited %d and started %q, want a failure and no agent",
					shell, version, run.exit, run.agent)
			}
			if !strings.Contains(run.stderr, version) || !strings.Contains(run.stderr, "agent.source") {
				t.Errorf("%s, %s: stderr = %q, want the version and the way out", shell, version, run.stderr)
			}
		}
	}
}

// requireGit skips a test where no git exists. The macOS gate has git.
func requireGit(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		if runtime.GOOS == "darwin" && os.Getenv("CI") != "" {
			t.Fatal("this macOS runner has no git")
		}
		t.Skip("no git on this computer")
	}
}

// Git Credential Manager writes an empty helper that resets the list, then itself. Agent 4.9.0
// cannot replace two helpers, so its checkout would fail. The script removes them for both
// sources, and says so.
func TestTheScriptRemovesTheCredentialHelpersThatTheAgentCannotReplace(t *testing.T) {
	requireGit(t)
	gcm := "[credential]\n\thelper =\n\thelper = /usr/local/share/gcm-core/git-credential-manager\n" +
		"[credential \"https://dev.azure.com\"]\n\tuseHttpPath = true\n"
	v4 := "buildkite-agent version 4.9.0+15270.735f98f3de5a29a9b3b60a36bbcef4f4e1d9ba17"

	for _, shell := range shells(t) {
		for _, source := range []profile.AgentSource{profile.AgentDownload, profile.AgentImage} {
			run := runScript(t, shell, scriptCase{source: source, imageVersion: v4, gitconfig: text(gcm)})

			if run.exit != 0 || run.agent == "" {
				t.Errorf("%s, %s: the script exited %d and started %q, want 0 and an agent; stderr: %s",
					shell, source, run.exit, run.agent, run.stderr)
			}
			if len(run.helpers) != 0 {
				t.Errorf("%s, %s: ~/.gitconfig still has the helpers %q, want none", shell, source, run.helpers)
			}
			if !strings.Contains(run.stderr, "removed the 2 credential.helper lines") {
				t.Errorf("%s, %s: stderr = %q, want the line that says what the script removed",
					shell, source, run.stderr)
			}
		}
	}
}

// One helper is what the agent can replace, so the script leaves it, and an image with no
// ~/.gitconfig stays without one.
func TestTheScriptLeavesOneCredentialHelper(t *testing.T) {
	requireGit(t)

	for _, shell := range shells(t) {
		run := runScript(t, shell, scriptCase{gitconfig: text("[credential]\n\thelper = osxkeychain\n")})
		if run.exit != 0 || len(run.helpers) != 1 || run.helpers[0] != "osxkeychain" ||
			strings.Contains(run.stderr, "credential.helper") {
			t.Errorf("%s: exit %d, helpers %q, stderr %q, want 0, [osxkeychain], and no line",
				shell, run.exit, run.helpers, run.stderr)
		}

		run = runScript(t, shell, scriptCase{})
		if run.exit != 0 || strings.Contains(run.stderr, "credential.helper") {
			t.Errorf("%s: with no ~/.gitconfig, exit %d and stderr %q, want 0 and no line",
				shell, run.exit, run.stderr)
		}
		if _, err := os.Stat(filepath.Join(run.home, ".gitconfig")); err == nil {
			t.Errorf("%s: the script made a ~/.gitconfig, want none", shell)
		}
	}
}
