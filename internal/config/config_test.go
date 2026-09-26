package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
)

const minimal = `
controllerID: macos-prod
buildkite:
  queue: macos
  endpoint: https://agent.buildkite.com/v3
orchard:
  url: https://orchard.internal:6120
  serviceAccount: bk-agent-stack-orchard
profiles:
  - name: xcode-27
    match: {xcode: "27"}
    image: ghcr.io/acme/macos-golden-gate-xcode@sha256:abc
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
`

func secrets() config.Environment {
	values := map[string]string{
		"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN": "bkct_test",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":         "orchard_test",
	}

	return func(name string) string { return values[name] }
}

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	return path
}

func load(t *testing.T, body string, environment config.Environment) (*config.Config, error) {
	t.Helper()

	return config.Load(write(t, body), environment)
}

func TestLoadReadsAMinimalConfiguration(t *testing.T) {
	cfg, err := load(t, minimal, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	if cfg.ControllerID != "macos-prod" {
		t.Errorf("ControllerID = %q, want %q", cfg.ControllerID, "macos-prod")
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "xcode-27" {
		t.Errorf("Profiles = %+v, want one profile named xcode-27", cfg.Profiles)
	}
	if cfg.Buildkite.AgentToken.Reveal() != "bkct_test" {
		t.Error("Load() did not read the Buildkite token from the environment")
	}
	if cfg.Orchard.Token.Reveal() != "orchard_test" {
		t.Error("Load() did not read the Orchard token from the environment")
	}
}

// The defaults of the target design apply to a file that states none of them.
func TestLoadAppliesTheDefaults(t *testing.T) {
	cfg, err := load(t, minimal, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"pollInterval", cfg.Buildkite.PollInterval, time.Second},
		{"jobStatesInterval", cfg.Buildkite.JobStatesInterval, 5 * time.Second},
		{"reservationExpiry", cfg.Buildkite.ReservationExpiry, 300 * time.Second},
		{"reservationRenewBefore", cfg.Buildkite.ReservationRenewBefore, 120 * time.Second},
		{"jobTokenLifetime", cfg.Buildkite.JobTokenLifetime, 1800 * time.Second},
		{"inventoryInterval", cfg.Orchard.InventoryInterval, time.Second},
		{"stateFreshness", cfg.StateFreshness, 60 * time.Second},
		{"defaults.unplaced", cfg.Defaults.Timeouts.Unplaced, 2 * time.Minute},
		{"defaults.boot", cfg.Defaults.Timeouts.Boot, 15 * time.Minute},
		{"defaults.acquire", cfg.Defaults.Timeouts.Acquire, 5 * time.Minute},
		{"defaults.maxVMLifetime", cfg.Defaults.Timeouts.MaxVMLifetime, 25 * time.Hour},
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// Zero means the total slots of the eligible workers, which the admission decides.
	if cfg.MaxInFlight != 0 {
		t.Errorf("MaxInFlight = %d, want 0", cfg.MaxInFlight)
	}
	if cfg.Profiles[0].StartupTransport != "tart-guest-agent" {
		t.Errorf("StartupTransport = %q, want the guest agent by default",
			cfg.Profiles[0].StartupTransport)
	}
}

// A key that the controller does not know is a mistake, and a secret in the file is a defect.
// Strict decoding catches both.
func TestLoadRejectsAnUnknownField(t *testing.T) {
	_, err := load(t, minimal+"\nbuildkiteAgentToken: bkct_secret\n", secrets())
	if err == nil {
		t.Fatal("Load() with an unknown field = nil, want an error")
	}
	if !strings.Contains(err.Error(), "buildkiteAgentToken") {
		t.Errorf("Load() = %q, want it to name the unknown field", err)
	}
}

func TestLoadReportsTheLineOfAProblem(t *testing.T) {
	_, err := load(t, "controllerID: macos-prod\npollInterval: nonsense\n", secrets())
	if err == nil {
		t.Fatal("Load() with a value that is not valid = nil, want an error")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("Load() = %q, want it to name the line", err)
	}
}

func TestLoadRejectsAControllerIDThatCannotNameAVM(t *testing.T) {
	for _, id := range []string{"", "MacOS", "macos_prod", "this-identifier-is-far-too-long-for-a-vm"} {
		body := strings.Replace(minimal, "controllerID: macos-prod", "controllerID: "+id, 1)

		if _, err := load(t, body, secrets()); err == nil {
			t.Errorf("Load() with controllerID %q = nil, want an error", id)
		}
	}
}

func TestLoadRejectsTwoProfilesWithTheSameName(t *testing.T) {
	body := minimal + `
  - name: xcode-27
    match: {xcode: "27.1"}
    image: ghcr.io/acme/other@sha256:abc
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
`

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("Load() with a duplicate name = %v, want an error about the name", err)
	}
}

// Two profiles that match the same tags make the selection ambiguous, and the match must select
// exactly one profile.
func TestLoadRejectsTwoProfilesWithTheSameMatchMap(t *testing.T) {
	body := minimal + `
  - name: xcode-27-copy
    match: {xcode: "27"}
    image: ghcr.io/acme/other@sha256:abc
    cpu: 8
    memoryMiB: 28672
    diskSizeGB: 150
`

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "match") {
		t.Errorf("Load() with a duplicate match map = %v, want an error about the match map", err)
	}
}

// A slow but healthy virtual machine must not start with a token that has expired.
func TestLoadRejectsTimeoutsThatOutlastTheJobToken(t *testing.T) {
	body := strings.Replace(minimal, "profiles:", `defaults:
  timeouts:
    boot: 40m
profiles:`, 1)

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "jobTokenLifetime") {
		t.Errorf("Load() with timeouts that outlast the token = %v, want an error naming the token", err)
	}
}

func TestLoadRejectsASourceThatIsReadLessOftenThanTheFreshnessLimit(t *testing.T) {
	for name, change := range map[string]string{
		"buildkite.pollInterval":      "buildkite:\n  pollInterval: 60s",
		"buildkite.jobStatesInterval": "buildkite:\n  jobStatesInterval: 90s",
		"orchard.inventoryInterval":   "orchard:\n  inventoryInterval: 2m",
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(minimal, strings.SplitN(change, "\n", 2)[0], change, 1)

			_, err := load(t, body, secrets())
			if err == nil || !strings.Contains(err.Error(), name) ||
				!strings.Contains(err.Error(), "stateFreshness") {
				t.Errorf("Load() with %s = %v, want an error naming it and stateFreshness", change, err)
			}
		})
	}
}

// The probes are how the platform learns that the controller lives, so an address that cannot be
// served fails at start-up and not at the first probe.
func TestLoadRejectsAHealthAddressWithoutAPort(t *testing.T) {
	body := "healthAddress: localhost\n" + minimal

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "healthAddress") {
		t.Errorf("Load() with a health address without a port = %v, want an error naming it", err)
	}
}

func TestLoadRejectsAMissingSecret(t *testing.T) {
	none := func(string) string { return "" }

	_, err := load(t, minimal, none)
	if err == nil {
		t.Fatal("Load() without the secrets = nil, want an error")
	}
	if !strings.Contains(err.Error(), "BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN") {
		t.Errorf("Load() = %q, want it to name the variable", err)
	}
}

// The SSH transport reads the image credentials from the environment, so the variables that a
// profile names must exist before a virtual machine starts with them.
func TestLoadRejectsAnSSHProfileWhoseCredentialVariablesAreEmpty(t *testing.T) {
	body := minimal + `    startupTransport: ssh
    vmCredentials: {usernameEnv: VM_USER, passwordEnv: VM_PASSWORD}
`

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "VM_USER") {
		t.Errorf("Load() = %v, want an error naming the empty variable", err)
	}
}

func TestLoadAcceptsAnSSHProfileWithItsCredentials(t *testing.T) {
	body := minimal + `    startupTransport: ssh
    vmCredentials: {usernameEnv: VM_USER, passwordEnv: VM_PASSWORD}
`
	environment := func(name string) string {
		switch name {
		case "BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN":
			return "bkct_test"
		case "BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":
			return "orchard_test"
		case "VM_USER":
			return "admin-of-this-image"
		case "VM_PASSWORD":
			return "a-password"
		}

		return ""
	}

	if _, err := load(t, body, environment); err != nil {
		t.Errorf("Load() = %v, want no error", err)
	}
}

func TestLoadWarnsAboutAnImageWithoutADigest(t *testing.T) {
	body := strings.Replace(minimal,
		"image: ghcr.io/acme/macos-golden-gate-xcode@sha256:abc",
		"image: ghcr.io/acme/macos-golden-gate-xcode:27", 1)

	cfg, err := load(t, body, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error, because a tag is a warning", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "digest") {
		t.Errorf("Warnings = %v, want one warning about the digest", cfg.Warnings)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	body := strings.Replace(minimal, "controllerID: macos-prod", "controllerID: MacOS", 1)
	body = strings.Replace(body, "name: xcode-27", "name: Xcode_27", 1)

	_, err := load(t, body, secrets())
	if err == nil {
		t.Fatal("Load() = nil, want an error")
	}

	message := err.Error()
	if !strings.Contains(message, "controllerID") || !strings.Contains(message, "Xcode_27") {
		t.Errorf("Load() = %q, want it to report both problems", message)
	}
}

func TestLoadRejectsAFileThatDoesNotExist(t *testing.T) {
	if _, err := config.Load("/no/such/config.yaml", secrets()); err == nil {
		t.Error("Load() of a missing file = nil, want an error")
	}
}

func TestLoadRejectsAConfigurationWithNoProfile(t *testing.T) {
	body := strings.Split(minimal, "profiles:")[0]

	if _, err := load(t, body, secrets()); err == nil {
		t.Error("Load() with no profile = nil, want an error")
	}
}
