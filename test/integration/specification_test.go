//go:build integration

package integration_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// T-064. Orchard keeps every field of the specification that the launcher sends.
//
// Orchard ignores a JSON field that it does not know, so a create that succeeds proves only that
// no field was refused. A field that a new version renames or drops arrives nowhere, and the
// machine starts without it: without softnet, or without the credentials of the SSH transport.
// The test therefore reads the stored machine back, and every field that the specification sent
// must come back with the same value.
func TestOrchardKeepsEveryFieldOfTheSpecification(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	client := orchardClient(t)

	// Every optional field of a profile is set, so the specification carries every field that
	// the launcher can send. A field that vmspec gains is part of this test without a change here.
	base := profile.Profile{
		Name:         "xcode-27",
		Match:        map[string]string{"xcode": "27"},
		Image:        "ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0",
		CPU:          4,
		MemoryMiB:    8192,
		DiskSizeGB:   150,
		WorkerLabels: map[string]string{"pool": "release"},
		SoftnetAllow: []string{"10.20.0.0/16"},
		OTel:         profile.OTel{Enabled: true, Endpoint: "https://otel.example:4318"},
	}

	cases := []struct {
		transport   profile.Transport
		job         job.ID
		credentials *vmspec.Credentials
	}{
		{profile.TransportGuestAgent, "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01", nil},
		{
			profile.TransportSSH, "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02",
			&vmspec.Credentials{Username: "builder", Password: "integration"},
		},
	}

	for _, c := range cases {
		t.Run(string(c.transport), func(t *testing.T) {
			spec := base
			spec.StartupTransport = c.transport

			if c.credentials != nil {
				spec.VMCredentials = &profile.Credentials{
					UsernameEnv: "BK_IMAGE_USERNAME",
					PasswordEnv: "BK_IMAGE_PASSWORD",
				}
			}

			// The profile is one that the configuration accepts, so the test sends nothing that
			// a real controller could not.
			if err := spec.Validate(); err != nil {
				t.Fatalf("the profile of the test is not valid: %v", err)
			}

			vm, err := vmspec.Build(vmspec.Request{
				ControllerID:     controllerID,
				Queue:            queue,
				JobID:            c.job,
				JobToken:         "bkaj_integration",
				Profile:          spec,
				ImageCredentials: c.credentials,
				TraceParent:      "00-018f3b2c1c4a7e5b9a3d2f1e4c5b6a01-018f3b2c1c4a7e5b-01",
				OTelHeaders:      "authorization=Bearer integration",
			})
			if err != nil {
				t.Fatalf("building the specification: %v", err)
			}

			// A field that the API refuses fails here.
			if err := client.CreateVM(t.Context(), vm); err != nil {
				t.Fatalf("orchard refused the specification: %v", err)
			}

			sent := fields(t, vm)

			// The machine is read without the client of this project, so a mistake in its
			// types cannot agree with itself.
			var stored map[string]any
			if err := decode(orchardURL+"/v1/vms/"+vm.Name, &stored); err != nil {
				t.Fatalf("reading the stored machine: %v", err)
			}

			// The names only: the values carry the job token and the image password.
			var lost []string
			for name, value := range sent {
				if unset(value) {
					continue
				}

				if !reflect.DeepEqual(stored[name], value) {
					lost = append(lost, name)
				}
			}

			slices.Sort(lost)

			if len(lost) != 0 {
				t.Errorf("orchard did not keep the fields %v of the specification", lost)
			}

			if err := client.DeleteVM(t.Context(), vm.Name); err != nil {
				t.Fatalf("deleting %s: %v", vm.Name, err)
			}
		})
	}
}

// fields gives the specification as the JSON object that goes over the wire, so that the
// comparison sees the names and the values that Orchard sees.
func fields(t *testing.T, value any) map[string]any {
	t.Helper()

	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("writing the specification: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("reading the specification: %v", err)
	}

	return out
}

// unset reports a field that the specification leaves to Orchard. Orchard fills some of them in,
// the creation time for one. A time field goes over the wire even when it is zero, because
// omitempty does not omit a structure.
func unset(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == "" || v == zeroTime
	case bool:
		return !v
	case float64:
		return v == 0
	}

	return false
}

// zeroTime is how encoding/json writes a time.Time that has no value.
const zeroTime = "0001-01-01T00:00:00Z"
