// SPDX-License-Identifier: AGPL-3.0-or-later

package vmspec_test

import (
	"fmt"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
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
			if got, owned := vmspec.OwnedJob(name, other); owned {
				t.Fatalf("OwnedJob(%q, %q) claimed job %q of the controller %q", name, other, got,
					owner)
			}
		}
	})
}
