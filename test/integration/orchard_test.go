//go:build integration

package integration_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// T-041. The Orchard client of this project, against the Orchard version that the design pins.
//
// The unit tests of the client run against a server that this project wrote, which agrees with
// whatever the client believes. This test asks the real API each question that the controller
// depends on: the specification that the launcher sends is accepted, a second create of a name is
// a conflict, the list carries what the inventory reads, the events carry the lines of the
// startup script, and a delete removes the machine and its events.
func TestTheOrchardClientAgreesWithOrchard(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	ctx := t.Context()
	client := orchardClient(t)

	workers, err := client.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("listing the workers: %v", err)
	}

	if len(workers) != 1 {
		t.Fatalf("orchard has the workers %+v, want the one that orchard dev starts", workers)
	}

	worker := workers[0]

	// Admission reads the resources, and the offline check reads the time of the last report.
	// The values are the ones that startOrchard gives the synthetic worker.
	want := map[string]uint64{
		orchard.ResourceVMs:       2,
		orchard.ResourceCores:     16,
		orchard.ResourceMemoryMiB: 65536,
	}
	if !maps.Equal(worker.Resources, want) {
		t.Errorf("the worker has the resources %v, want %v", worker.Resources, want)
	}

	if worker.Offline(time.Now(), time.Minute) {
		t.Errorf("the worker was last seen at %v, want a worker that reports", worker.LastSeen)
	}

	// The specification that the launcher sends, built by the code that builds it.
	id := job.ID(carried)
	spec, err := vmspec.Build(vmspec.Request{
		ControllerID: controllerID,
		Queue:        queue,
		JobID:        id,
		JobToken:     "bkaj_integration",
		Profile: profile.Profile{
			Name:       "xcode-27",
			Image:      "ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0",
			CPU:        4,
			MemoryMiB:  8192,
			DiskSizeGB: 150,
		},
	})
	if err != nil {
		t.Fatalf("building the specification: %v", err)
	}

	// A machine of somebody else, which the controller must leave alone.
	foreign := orchard.VM{
		Name:      "release-mac-1",
		Image:     spec.Image,
		CPU:       4,
		Memory:    8192,
		Resources: spec.Resources,
	}

	for _, vm := range []orchard.VM{spec, foreign} {
		if err := client.CreateVM(ctx, vm); err != nil {
			t.Fatalf("creating %s: %v", vm.Name, err)
		}
	}

	// A second create of the same name is how a retry of the launcher arrives, and the launcher
	// counts it as a success only when the client gives ErrAlreadyExists.
	if err := client.CreateVM(ctx, spec); !errors.Is(err, orchard.ErrAlreadyExists) {
		t.Errorf("a second create of %s gave %v, want ErrAlreadyExists", spec.Name, err)
	}

	var ours orchard.VM

	eventually(t, "orchard runs the machine of the job", func() bool {
		found, ok := listed(t, client, spec.Name)
		ours = found

		return ok && found.Status == orchard.StatusRunning
	})

	// The list gives every machine, and the controller selects its own by the name prefix.
	vms, err := client.ListVMs(ctx)
	if err != nil {
		t.Fatalf("listing the machines: %v", err)
	}

	var owned []job.ID
	for _, vm := range vms {
		if owner, ok := vmspec.OwnedJob(vm.Name, controllerID); ok {
			owned = append(owned, owner)
		}
	}

	if !slices.Equal(owned, []job.ID{id}) {
		t.Errorf("the controller owns the machines of %v among %v, want only %s", owned,
			names(vms), id)
	}

	if !slices.Contains(names(vms), foreign.Name) {
		t.Errorf("the list %v leaves out the machine of somebody else", names(vms))
	}

	// Admission counts what a placed machine takes from its worker.
	if !ours.Placed() || ours.Worker != worker.Name {
		t.Errorf("the machine is on the worker %q, want it placed on %q", ours.Worker, worker.Name)
	}

	if !maps.Equal(ours.Resources, spec.Resources) {
		t.Errorf("the machine takes %v, want the %v that the launcher asked for",
			ours.Resources, spec.Resources)
	}

	if ours.CreatedAt.IsZero() {
		t.Error("the machine has no creation time, which the reaper reads to decide its age")
	}

	// A synthetic worker writes each line of the startup script as an event, where a real worker
	// writes each line that the script prints. The acquire timeout reads them from here.
	script := strings.Split(strings.TrimSuffix(spec.StartupScript.ScriptContent, "\n"), "\n")

	var lines []string

	eventually(t, "the events carry the startup script", func() bool {
		events, err := client.Events(ctx, spec.Name)
		if err != nil {
			t.Fatalf("reading the events of %s: %v", spec.Name, err)
		}

		lines = lines[:0]
		for _, event := range events {
			if event.Kind == "log_line" {
				lines = append(lines, strings.TrimSuffix(event.Payload, "\n"))
			}
		}

		return len(lines) >= len(script)
	})

	if !slices.Equal(lines, script) {
		t.Errorf("the events carry the lines %q, want the script %q", lines, script)
	}

	for _, vm := range []orchard.VM{spec, foreign} {
		if err := client.DeleteVM(ctx, vm.Name); err != nil {
			t.Fatalf("deleting %s: %v", vm.Name, err)
		}
	}

	eventually(t, "the machines are gone", func() bool {
		vms, err := client.ListVMs(ctx)

		return err == nil && len(vms) == 0
	})

	// The reaper deletes a machine that may already be gone, and it wanted the machine gone.
	if err := client.DeleteVM(ctx, spec.Name); err != nil {
		t.Errorf("a second delete of %s gave %v, want no error", spec.Name, err)
	}

	// The events go with the machine.
	if _, err := client.Events(ctx, spec.Name); !errors.Is(err, orchard.ErrNotFound) {
		t.Errorf("the events of a deleted machine gave %v, want ErrNotFound", err)
	}
}

// orchardClient gives the client of this project for `orchard dev`, which checks no credentials.
func orchardClient(t *testing.T) *orchard.Client {
	t.Helper()

	client, err := orchard.New(orchard.Options{
		URL:            orchardURL,
		ServiceAccount: "bk-agent-stack-orchard",
		Token:          "integration",
		Attempts:       1,
	})
	if err != nil {
		t.Fatalf("creating the client: %v", err)
	}

	return client
}

// listed finds one machine in the list of the client.
func listed(t *testing.T, client *orchard.Client, name string) (orchard.VM, bool) {
	t.Helper()

	vms, err := client.ListVMs(t.Context())
	if err != nil {
		t.Fatalf("listing the machines: %v", err)
	}

	for _, vm := range vms {
		if vm.Name == name {
			return vm, true
		}
	}

	return orchard.VM{}, false
}

func names(vms []orchard.VM) []string {
	out := make([]string, 0, len(vms))
	for _, vm := range vms {
		out = append(out, vm.Name)
	}

	return out
}
