// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package vmspec_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// The Reaper deletes the machines that OwnedJob claims, and any tool can create a machine in
// Orchard with any name. Whatever the name, the controller claims a machine only when the name is
// exactly the one that it gives to a valid job, so it never deletes the machine of another
// controller or of another tool.
func FuzzOwnedJobClaimsOnlyTheMachinesOfThisController(f *testing.F) {
	f.Add("bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d", "macos-prod")
	f.Add("bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d", "macos")
	f.Add("bk-macos-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d", "macos-prod")
	f.Add("bk-macos-prod-", "macos-prod")
	f.Add("bk-macos-prod-018F3B2C-1C4A-7E5B-9A3D-2F1E4C5B6A7D", "macos-prod")
	f.Add("ci-runner-1", "macos-prod")

	f.Fuzz(func(t *testing.T, name, controllerID string) {
		id, owned := vmspec.OwnedJob(name, controllerID)
		if owned && vmspec.IsOrphan(name, controllerID) {
			t.Fatalf("%q is both owned and an orphan of %q", name, controllerID)
		}

		if !owned {
			return
		}

		if !id.Valid() {
			t.Fatalf("OwnedJob(%q, %q) claimed the invalid job %q", name, controllerID, id)
		}

		if got := vmspec.Name(controllerID, id); got != name {
			t.Fatalf("OwnedJob(%q, %q) claimed job %q, whose machine is %q", name, controllerID,
				id, got)
		}
	})
}

// A machine of one controller is never claimed by another controller, whatever the two
// identifiers are, and the controller that named it always claims it.
func FuzzOwnedJobTellsControllersApart(f *testing.F) {
	f.Add("macos-prod", "macos", uint64(1), uint64(2))
	f.Add("a", "a-b", uint64(0), uint64(0))
	f.Add("macos", "macos", uint64(3), uint64(4))

	f.Fuzz(func(t *testing.T, owner, other string, high, low uint64) {
		text := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", high>>32, high>>16&0xffff, high&0xffff,
			low>>48, low&0xffffffffffff)

		id, err := job.ParseID(text)
		if err != nil {
			t.Fatalf("ParseID(%q) = %v, want a valid identifier", text, err)
		}

		name := vmspec.Name(owner, id)

		if got, owned := vmspec.OwnedJob(name, owner); !owned || got != id {
			t.Fatalf("OwnedJob(%q, %q) = %q, %v, want %q, true", name, owner, got, owned, id)
		}

		if other != owner {
			if vmspec.IsOrphan(name, other) {
				t.Fatalf("IsOrphan(%q, %q) took the machine of the controller %q", name, other,
					owner)
			}

			if got, owned := vmspec.OwnedJob(name, other); owned {
				t.Fatalf("OwnedJob(%q, %q) claimed job %q of the controller %q", name, other, got,
					owner)
			}
		}
	})
}

// A tag comes from a pipeline author, and every build is untrusted. Whatever the tag says, it
// reaches the agent only through the environment and never through the text of the script, and
// the list of the agent holds one entry for each tag.
func FuzzATagReachesTheEnvironmentAndNeverTheScript(f *testing.F) {
	f.Add("xcode", "27")
	f.Add("size", "large")
	f.Add("x", "$(touch /tmp/owned)")
	f.Add("x", "`id`")
	f.Add("x", "a,b=c")
	f.Add("x", "a\nb")
	f.Add("x", "\"; rm -rf /; \"")
	f.Add("", "")
	f.Add("x", " y")
	f.Add("profile", "z")

	fuzzRequest := func(tags map[string]string) vmspec.Request {
		return vmspec.Request{
			ControllerID: "macos-prod",
			Queue:        "macos",
			JobID:        job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"),
			JobToken:     secret.String("bkjat_job_token"),
			Tags:         tags,
			Profile: profile.Profile{
				Name: "xcode-27", Image: "image@sha256:abc", CPU: 8, MemoryMiB: 28672,
				DiskSizeGB: 150, StartupTransport: profile.TransportGuestAgent,
			},
		}
	}

	// The script of a job with no tags. A tag must never change it.
	plain, err := vmspec.Build(fuzzRequest(nil))
	if err != nil {
		f.Fatalf("Build() with no tags = %v", err)
	}

	f.Fuzz(func(t *testing.T, key, value string) {
		vm, err := vmspec.Build(fuzzRequest(map[string]string{key: value}))
		if err != nil {
			return
		}

		if script := vm.StartupScript.ScriptContent; script != plain.StartupScript.ScriptContent {
			t.Fatalf("Build() with the tag %q=%q gave the script %q", key, value, script)
		}

		// The agent trims each tag and keeps its first '=' as the divide, so a tag that Build
		// accepts must equal the rule of the job as the agent registers it.
		if strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			t.Fatalf("Build() accepted the padded tag %q=%q", key, value)
		}
		switch key {
		case "queue", "agent-stack", "profile":
			t.Fatalf("Build() accepted the reserved tag %q", key)
		}

		list := vm.StartupScript.Env["BUILDKITE_AGENT_TAGS"]
		if !strings.HasSuffix(list, ","+key+"="+value) {
			t.Fatalf("Build() with the tag %q=%q gave the list %q", key, value, list)
		}

		if got := len(strings.Split(list, ",")); got != 4 {
			t.Fatalf("Build() with the tag %q=%q gave %d tags in %q, want 4", key, value, got, list)
		}
	})
}

// Orchard gives back the environment of each machine, and any tool can create a machine with any
// environment. Whatever the tags, ProfileOf gives one value of the list, or nothing: never a text
// that spans two tags.
func FuzzProfileOfReadsOneTag(f *testing.F) {
	f.Add("queue=macos,agent-stack=orchard,profile=xcode-27,xcode=27")
	f.Add("profile=")
	f.Add("profile=a,profile=b")
	f.Add(",,,")
	f.Add("")

	f.Fuzz(func(t *testing.T, tags string) {
		got := vmspec.ProfileOf(orchard.VM{StartupScript: &orchard.Script{
			Env: map[string]string{"BUILDKITE_AGENT_TAGS": tags},
		}})

		if strings.Contains(got, ",") {
			t.Fatalf("ProfileOf(%q) = %q, which spans two tags", tags, got)
		}

		if got != "" && !strings.Contains(","+tags+",", ",profile="+got+",") {
			t.Fatalf("ProfileOf(%q) = %q, which is not the value of a profile tag", tags, got)
		}
	})
}
