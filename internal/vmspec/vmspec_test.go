package vmspec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

const jobUUID = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")

func request() vmspec.Request {
	return vmspec.Request{
		ControllerID: "macos-prod",
		Queue:        "macos",
		JobID:        jobUUID,
		JobToken:     config.Secret("bkjat_job_token"),
		Profile: profile.Profile{
			Name:             "xcode-27",
			Match:            map[string]string{"xcode": "27"},
			Image:            "ghcr.io/acme/macos-golden-gate-xcode@sha256:abc",
			CPU:              8,
			MemoryMiB:        28672,
			DiskSizeGB:       150,
			WorkerLabels:     map[string]string{"pool": "general"},
			SoftnetAllow:     []string{"10.20.30.40/32"},
			StartupTransport: profile.TransportGuestAgent,
		},
	}
}

func build(t *testing.T, r vmspec.Request) vmspec.VM {
	t.Helper()

	vm, err := vmspec.Build(r)
	if err != nil {
		t.Fatalf("Build() = %v, want no error", err)
	}

	return vm
}

func TestBuildNamesTheVMAfterTheControllerAndTheJob(t *testing.T) {
	vm := build(t, request())

	want := "bk-macos-prod-" + jobUUID.String()
	if vm.Name != want {
		t.Errorf("Name = %q, want %q", vm.Name, want)
	}
	if len(vm.Name) > 63 {
		t.Errorf("Name is %d characters, Orchard recommends 63 or fewer", len(vm.Name))
	}
}

// Orchard recommends names of 63 characters or fewer. The identifier and the job UUID together
// must respect that, and a longer name is a defect of the configuration, not of the job.
func TestBuildRejectsANameThatIsTooLong(t *testing.T) {
	r := request()
	r.ControllerID = strings.Repeat("a", 30)

	if _, err := vmspec.Build(r); err == nil {
		t.Error("Build() with a long controller identifier = nil, want an error")
	}
}

func TestBuildRejectsAnEmptyJobOrController(t *testing.T) {
	for name, change := range map[string]func(*vmspec.Request){
		"no job identifier": func(r *vmspec.Request) { r.JobID = "" },
		"no controller ID":  func(r *vmspec.Request) { r.ControllerID = "" },
		"no queue":          func(r *vmspec.Request) { r.Queue = "" },
		"no job token":      func(r *vmspec.Request) { r.JobToken = "" },
	} {
		r := request()
		change(&r)

		if _, err := vmspec.Build(r); err == nil {
			t.Errorf("Build() with %s = nil, want an error", name)
		}
	}
}

func TestBuildCopiesTheSizeAndTheImageOfTheProfile(t *testing.T) {
	vm := build(t, request())

	if vm.Image != "ghcr.io/acme/macos-golden-gate-xcode@sha256:abc" {
		t.Errorf("Image = %q", vm.Image)
	}
	if vm.CPU != 8 || vm.Memory != 28672 || vm.DiskSize != 150 {
		t.Errorf("size = cpu %d, memory %d, disk %d", vm.CPU, vm.Memory, vm.DiskSize)
	}
	if vm.OS != "darwin" || vm.ImagePullPolicy != "IfNotPresent" {
		t.Errorf("os = %q, imagePullPolicy = %q", vm.OS, vm.ImagePullPolicy)
	}
}

// Orchard bin-packs a worker only when a virtual machine asks for resources, and Apple permits
// two machines on one host.
func TestBuildAsksForTheResourcesThatTheSchedulerCounts(t *testing.T) {
	vm := build(t, request())

	want := map[string]uint64{
		"org.cirruslabs.tart-vms":      1,
		"org.cirruslabs.logical-cores": 8,
		"org.cirruslabs.memory-mib":    28672,
	}

	for key, value := range want {
		if vm.Resources[key] != value {
			t.Errorf("Resources[%q] = %d, want %d", key, vm.Resources[key], value)
		}
	}
	if len(vm.Resources) != len(want) {
		t.Errorf("Resources = %v, want only the three that the scheduler counts", vm.Resources)
	}
}

// Labels are placement constraints. A label that is not a worker constraint would stop Orchard
// from placing the machine.
func TestBuildLabelsTheVMWithTheWorkerConstraintsOnly(t *testing.T) {
	vm := build(t, request())

	if len(vm.Labels) != 1 || vm.Labels["pool"] != "general" {
		t.Errorf("Labels = %v, want only the worker labels of the profile", vm.Labels)
	}
}

// These four properties are the isolation of an untrusted build. A specification that loses one
// of them puts a build next to the host, another build, or a second job.
func TestBuildIsolatesTheVM(t *testing.T) {
	vm := build(t, request())

	if !vm.NetSoftnet {
		t.Error("NetSoftnet = false, want true, because a build must not reach the host")
	}
	if vm.RestartPolicy != "Never" {
		t.Errorf("RestartPolicy = %q, want Never, because a VM serves one job", vm.RestartPolicy)
	}
	if vm.HostDirs != nil {
		t.Errorf("HostDirs = %v, want none", vm.HostDirs)
	}
	if !vm.Headless || !vm.RandomSerial {
		t.Errorf("Headless = %v, RandomSerial = %v, want both true", vm.Headless, vm.RandomSerial)
	}
	if len(vm.NetSoftnetAllow) != 1 || vm.NetSoftnetAllow[0] != "10.20.30.40/32" {
		t.Errorf("NetSoftnetAllow = %v, want the allowlist of the profile", vm.NetSoftnetAllow)
	}
}

func TestBuildGivesTheVMOnlyTheTokenOfItsOwnJob(t *testing.T) {
	vm := build(t, request())

	env := vm.StartupScript.Env
	if env["BUILDKITE_AGENT_TOKEN"] != "bkjat_job_token" {
		t.Errorf("BUILDKITE_AGENT_TOKEN = %q, want the job acquisition token", env["BUILDKITE_AGENT_TOKEN"])
	}
	if env["BUILDKITE_AGENT_ACQUIRE_JOB"] != jobUUID.String() {
		t.Errorf("BUILDKITE_AGENT_ACQUIRE_JOB = %q, want the job UUID", env["BUILDKITE_AGENT_ACQUIRE_JOB"])
	}

	want := []string{
		"BUILDKITE_AGENT_TOKEN", "BUILDKITE_AGENT_ACQUIRE_JOB", "BUILDKITE_AGENT_NAME",
		"BK_AGENT_STACK_QUEUE", "BK_AGENT_STACK_PROFILE",
	}
	if len(env) != len(want) {
		t.Errorf("env = %v, want only %v", env, want)
	}
	for _, key := range want {
		if env[key] == "" {
			t.Errorf("env[%q] is empty", key)
		}
	}
}

// The cluster agent token opens every self-hosted queue of the cluster, and the Orchard token
// controls the fleet. Neither may reach a machine that runs an untrusted build.
func TestBuildNeverCarriesAFleetCredential(t *testing.T) {
	r := request()
	vm := build(t, r)

	encoded, err := json.Marshal(vm) //nolint:gosec // G117: this test marshals on purpose, to
	// prove that the specification carries no fleet credential. The wire format must carry the
	// job token and, for the ssh transport, the image password.
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}

	for _, secret := range []string{"bkct_", "orchard_token", "admin"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("the specification contains %q: %s", secret, encoded)
		}
	}
}

func TestBuildUsesTheGuestAgentTransportWithoutCredentials(t *testing.T) {
	vm := build(t, request())

	if vm.StartupScript.Transport != "tart-guest-agent" {
		t.Errorf("Transport = %q, want the guest agent", vm.StartupScript.Transport)
	}
	if vm.Username != "" || vm.Password != "" {
		t.Errorf("Username = %q, Password = %q, want neither with the guest agent",
			vm.Username, vm.Password)
	}
}

func TestBuildUsesTheImageCredentialsWithTheSSHTransport(t *testing.T) {
	r := request()
	r.Profile.StartupTransport = profile.TransportSSH
	r.Profile.VMCredentials = &profile.Credentials{UsernameEnv: "VM_USER", PasswordEnv: "VM_PASSWORD"}
	r.ImageCredentials = &vmspec.Credentials{Username: "image-admin", Password: config.Secret("image-password")}

	vm := build(t, r)

	if vm.StartupScript.Transport != "ssh" {
		t.Errorf("Transport = %q, want ssh", vm.StartupScript.Transport)
	}
	if vm.Username != "image-admin" || vm.Password != "image-password" {
		t.Errorf("Username = %q, Password = %q, want the image credentials", vm.Username, vm.Password)
	}
}

// Orchard uses admin/admin when a specification names no credentials, so the SSH transport
// without them would start a machine that anybody on the host network can enter.
func TestBuildRejectsTheSSHTransportWithoutCredentials(t *testing.T) {
	r := request()
	r.Profile.StartupTransport = profile.TransportSSH
	r.Profile.VMCredentials = &profile.Credentials{UsernameEnv: "VM_USER", PasswordEnv: "VM_PASSWORD"}

	if _, err := vmspec.Build(r); err == nil {
		t.Error("Build() with ssh and no credentials = nil, want an error")
	}
}

func TestBuildAddsTheAgentTelemetryOnlyWhenTheProfileAsksForIt(t *testing.T) {
	r := request()
	r.Profile.OTel = profile.OTel{Enabled: true, Endpoint: "https://otel.example:4318"}
	r.TraceParent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	r.OTelHeaders = config.Secret("authorization=Bearer token")

	vm := build(t, r)
	env := vm.StartupScript.Env

	if env["BUILDKITE_OPENTELEMETRY_TRACING"] != "true" {
		t.Error("the agent telemetry is off, want it on when the profile enables it")
	}
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "https://otel.example:4318" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
	if env["TRACEPARENT"] != r.TraceParent {
		t.Errorf("TRACEPARENT = %q, want the parent of the job trace", env["TRACEPARENT"])
	}

	if env := build(t, request()).StartupScript.Env; env["BUILDKITE_OPENTELEMETRY_TRACING"] != "" {
		t.Error("the agent telemetry is on by default, want it off")
	}
}

func TestBuildStartsTheAgentForOneJob(t *testing.T) {
	vm := build(t, request())

	script := vm.StartupScript.ScriptContent
	if !strings.Contains(script, "buildkite-agent start") {
		t.Errorf("the script does not start the agent: %q", script)
	}
	for _, want := range []string{"${BK_AGENT_STACK_QUEUE}", "${BK_AGENT_STACK_PROFILE}"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not use %s: %q", want, script)
		}
	}
	// The token and the job never appear in the script, because the environment carries them.
	if strings.Contains(script, jobUUID.String()) || strings.Contains(script, "bkjat_") {
		t.Errorf("the script carries a value that belongs in the environment: %q", script)
	}
}

func TestOwnedJobReadsTheJobOfAVMThatThisControllerCreated(t *testing.T) {
	name := "bk-macos-prod-" + jobUUID.String()

	got, ok := vmspec.OwnedJob(name, "macos-prod")
	if !ok || got != jobUUID {
		t.Errorf("OwnedJob(%q) = %q, %v, want %q, true", name, got, ok, jobUUID)
	}
}

func TestOwnedJobIgnoresAVMOfAnotherOwner(t *testing.T) {
	for _, name := range []string{
		"bk-macos-dev-" + jobUUID.String(), // another controller
		"some-other-vm",                    // another tool
		"bk-macos-prod-",                   // no job
		"bk-macos-prod-not-a-uuid",         // a name that carries no job UUID
	} {
		if job, ok := vmspec.OwnedJob(name, "macos-prod"); ok {
			t.Errorf("OwnedJob(%q) = %q, true, want false", name, job)
		}
	}
}

// A specification carries the token of its job and, with the ssh transport, the password of the
// image. A log line must carry neither.
func TestVMDoesNotPrintItsSecrets(t *testing.T) {
	r := request()
	r.Profile.StartupTransport = profile.TransportSSH
	r.Profile.VMCredentials = &profile.Credentials{UsernameEnv: "VM_USER", PasswordEnv: "VM_PASSWORD"}
	r.ImageCredentials = &vmspec.Credentials{Username: "image-admin", Password: config.Secret("image-password")}

	vm := build(t, r)

	printed := []string{
		fmt.Sprintf("%v", vm),
		fmt.Sprintf("%+v", vm),
		fmt.Sprintf("%#v", vm),
		vm.String(),
	}

	var buffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&buffer, nil)).Info("created", "vm", vm)
	printed = append(printed, buffer.String())

	for _, line := range printed {
		for _, secret := range []string{"bkjat_job_token", "image-password"} {
			if strings.Contains(line, secret) {
				t.Errorf("a secret reached the output: %s", line)
			}
		}
	}
}
