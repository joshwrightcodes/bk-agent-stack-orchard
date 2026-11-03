// SPDX-License-Identifier: AGPL-3.0-or-later

package config_test

import (
	"math"
	"os"
	"path/filepath"
	"slices"
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

	// The probes have no authentication, so a controller on a host serves them to itself only.
	// The Helm chart sets an address that the kubelet reaches.
	if cfg.HealthAddress != "127.0.0.1:8080" {
		t.Errorf("HealthAddress = %q, want the loopback address 127.0.0.1:8080", cfg.HealthAddress)
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

// The token outlives the start timeouts of its profile by one minute, because it is issued before
// the create, and the unplaced timeout counts from the first snapshot that lists the machine. A
// profile whose timeouts fill the whole lifetime of the token leaves no minute, and a slow but
// healthy machine can then meet an expired token. A sum that leaves the minute loads.
func TestLoadKeepsTheMinuteOfTheTokenMargin(t *testing.T) {
	for name, test := range map[string]struct {
		boot  string
		loads bool
	}{
		// 2 min + 23 min + 5 min fills the 30 min of the token.
		"no minute left": {boot: "23m", loads: false},
		// 2 min + 22 min + 5 min leaves the minute.
		"the minute left": {boot: "22m", loads: true},
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(minimal, "profiles:", "defaults:\n  timeouts:\n    boot: "+
				test.boot+"\nprofiles:", 1)

			_, err := load(t, body, secrets())

			if test.loads && err != nil {
				t.Errorf("Load() with boot %s = %v, want it to load", test.boot, err)
			}

			if !test.loads && (err == nil || !strings.Contains(err.Error(), "jobTokenLifetime") ||
				!strings.Contains(err.Error(), "one minute")) {
				t.Errorf("Load() with boot %s = %v, want an error that names the token and the "+
					"minute", test.boot, err)
			}
		})
	}
}

// The start timeouts set the lifetime of each token, so their sum must not wrap. Two timeouts near
// the largest duration add up to a negative sum, and a negative timeout makes the sum small. Each
// sum would pass the check against the token, and the launcher would ask for a token that has
// already expired.
func TestLoadRefusesStartTimeoutsThatBreakTheSum(t *testing.T) {
	largest := time.Duration(math.MaxInt64).String()

	for name, timeouts := range map[string]string{
		"a sum that wraps":   "    boot: " + largest + "\n    acquire: " + largest + "\n",
		"a negative timeout": "    boot: -1h\n",
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(minimal, "profiles:", "defaults:\n  timeouts:\n"+timeouts+
				"profiles:", 1)

			_, err := load(t, body, secrets())
			if err == nil || !strings.Contains(err.Error(), "xcode-27") {
				t.Errorf("Load() with %s = %v, want an error that names the profile", name, err)
			}
		})
	}
}

// The Stacks API accepts a reservation of 3600 s at most, and a token of the same length at most.
// A value above that fails at start-up, and not at the first job, where Buildkite would refuse it.
func TestLoadRefusesAReservationOverOneHour(t *testing.T) {
	body := strings.Replace(minimal, "buildkite:", "buildkite:\n  reservationExpiry: 3601s", 1)

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "buildkite.reservationExpiry") ||
		!strings.Contains(err.Error(), "3600") {
		t.Errorf("Load() with a reservation of 3601 s = %v, want an error that names it and the limit", err)
	}
}

// The token goes into a virtual machine that runs untrusted code, so it lives 1800 s at most. That
// is shorter than the limit of the API.
func TestLoadRefusesATokenLifetimeOverThirtyMinutes(t *testing.T) {
	for _, lifetime := range []string{"1801s", "3600s", "3601s"} {
		t.Run(lifetime, func(t *testing.T) {
			body := strings.Replace(minimal, "buildkite:",
				"buildkite:\n  jobTokenLifetime: "+lifetime, 1)

			_, err := load(t, body, secrets())
			if err == nil || !strings.Contains(err.Error(), "buildkite.jobTokenLifetime") ||
				!strings.Contains(err.Error(), "1800") {
				t.Errorf("Load() with a token lifetime of %s = %v, want an error that names it and 1800",
					lifetime, err)
			}
		})
	}
}

// The API takes whole seconds, and a token lifetime of at least 1 s. A shorter value would turn
// into 0, which the library leaves out of the request, and the API would then use its default.
func TestLoadRefusesALifetimeUnderOneSecond(t *testing.T) {
	//nolint:gosec // The keys name a lifetime and not a credential.
	for name, change := range map[string]string{
		"buildkite.reservationExpiry": "buildkite:\n  reservationExpiry: 500ms",
		"buildkite.jobTokenLifetime":  "buildkite:\n  jobTokenLifetime: 500ms",
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(minimal, strings.SplitN(change, "\n", 2)[0], change, 1)

			_, err := load(t, body, secrets())
			if err == nil || !strings.Contains(err.Error(), name) ||
				!strings.Contains(err.Error(), "at least 1 s") {
				t.Errorf("Load() with %s = %v, want an error that names it and the minimum", change, err)
			}
		})
	}
}

// The library sends whole seconds, so 1500 ms would become 1 s. The configuration refuses it, and
// does not truncate it.
func TestLoadRefusesALifetimeThatIsNotWholeSeconds(t *testing.T) {
	//nolint:gosec // The keys name a lifetime and not a credential.
	for name, change := range map[string]string{
		"buildkite.reservationExpiry": "buildkite:\n  reservationExpiry: 1500ms",
		"buildkite.jobTokenLifetime":  "buildkite:\n  jobTokenLifetime: 1500ms",
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(minimal, strings.SplitN(change, "\n", 2)[0], change, 1)

			_, err := load(t, body, secrets())
			if err == nil || !strings.Contains(err.Error(), name) ||
				!strings.Contains(err.Error(), "whole seconds") {
				t.Errorf("Load() with %s = %v, want an error that names it and whole seconds",
					change, err)
			}
		})
	}
}

func TestLoadAcceptsTheLongestLifetimes(t *testing.T) {
	body := strings.Replace(minimal, "buildkite:",
		"buildkite:\n  reservationExpiry: 3600s\n  jobTokenLifetime: 1800s", 1)

	if _, err := load(t, body, secrets()); err != nil {
		t.Errorf("Load() with a reservation of 3600 s and a token of 1800 s = %v, want no error", err)
	}
}

// BUILDKITE_AGENT_TAGS is one comma-separated list, and the queue is its first tag.
func TestLoadRefusesAQueueThatIsUnsafeInTheTagList(t *testing.T) {
	for name, queue := range map[string]string{
		"a comma":          `"a,b"`,
		"a double quote":   `'a"b'`,
		"a single quote":   `"a'b"`,
		"a newline":        `"a\nb"`,
		"a carriage":       `"a\rb"`,
		"leading padding":  `" a"`,
		"trailing padding": `"a "`,
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(minimal, "queue: macos\n", "queue: "+queue+"\n", 1)
			if body == minimal {
				t.Fatal("the test fixture has no queue line to change")
			}

			_, err := load(t, body, secrets())
			if err == nil || !strings.Contains(err.Error(), "buildkite.queue") {
				t.Errorf("Load() with a queue with %s = %v, want an error that names the queue", name, err)
			}
		})
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

func TestLoadWarnsThatASoftnetAllowReachesEveryPipelineOnTheQueue(t *testing.T) {
	body := strings.Replace(minimal, "    image: ghcr.io/acme/macos-golden-gate-xcode@sha256:abc",
		"    image: ghcr.io/acme/macos-golden-gate-xcode@sha256:abc\n    softnetAllow: [\"10.20.30.40/32\"]", 1)

	cfg, err := load(t, body, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error, because the allow is a warning", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "every pipeline on the queue") {
		t.Errorf("Warnings = %v, want one warning about every pipeline", cfg.Warnings)
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

// The agent token and the Orchard token travel with every request, so an address must use TLS.
// A loopback address stays on the machine, and the tests use one.
func TestLoadRejectsAnAddressWithoutTLS(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"buildkite": {
			"endpoint: https://agent.buildkite.com/v3", "endpoint: http://agent.buildkite.com/v3",
			"buildkite.endpoint",
		},
		"orchard": {
			"url: https://orchard.internal:6120", "url: http://orchard.internal:6120", "orchard.url",
		},
		"another scheme": {
			"url: https://orchard.internal:6120", "url: ftp://orchard.internal:6120", "orchard.url",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, strings.Replace(minimal, c.from, c.to, 1), secrets())
			if err == nil || !strings.Contains(err.Error(), c.want) ||
				!strings.Contains(err.Error(), "https") {
				t.Fatalf("Load() = %v, want an error that %s must use https", err, c.want)
			}
		})
	}
}

func TestLoadAcceptsPlainHTTPOnALoopbackAddress(t *testing.T) {
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		t.Run(host, func(t *testing.T) {
			body := strings.Replace(minimal, "endpoint: https://agent.buildkite.com/v3",
				"endpoint: http://"+host+"/v3", 1)
			body = strings.Replace(body, "url: https://orchard.internal:6120",
				"url: http://"+host, 1)

			if _, err := load(t, body, secrets()); err != nil {
				t.Fatalf("Load() = %v, want no error for the loopback address %s", err, host)
			}
		})
	}
}

// A test cluster can serve a fake Stacks API without TLS. It must say so, and the controller
// warns about it.
func TestLoadAcceptsAPlainHTTPBuildkiteEndpointThatTheConfigurationAllows(t *testing.T) {
	body := strings.Replace(minimal, "endpoint: https://agent.buildkite.com/v3",
		"endpoint: http://fakestacks.smoke:8080\n  plainHTTPEndpoint: true", 1)

	cfg, err := load(t, body, secrets())
	if err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}

	if !slices.ContainsFunc(cfg.Warnings, func(w string) bool {
		return strings.Contains(w, "buildkite.endpoint") && strings.Contains(w, "plain HTTP")
	}) {
		t.Fatalf("Warnings = %q, want a warning about the plain HTTP endpoint", cfg.Warnings)
	}
}

// A profile names the variables whose values go into its machines. A job runs untrusted code in
// that machine, so a name that holds a controller secret would give the secret to the job.
func TestLoadRefusesAProfileThatNamesAControllerSecret(t *testing.T) {
	secretNames := []string{
		"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN",
		"BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT",
		"OTEL_EXPORTER_OTLP_HEADERS",
	}
	fields := map[string]string{
		"otel.headersEnv": `    otel: {enabled: true, endpoint: "https://otel.example.com", headersEnv: %s}
`,
		"vmCredentials.usernameEnv": `    startupTransport: ssh
    vmCredentials: {usernameEnv: %s, passwordEnv: VM_PASSWORD}
`,
		"vmCredentials.passwordEnv": `    startupTransport: ssh
    vmCredentials: {usernameEnv: VM_USER, passwordEnv: %s}
`,
	}

	for field, template := range fields {
		for _, name := range secretNames {
			t.Run(field+" "+name, func(t *testing.T) {
				environment := func(variable string) string {
					return map[string]string{
						"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN": "bkct_test",
						"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":         "orchard_test",
					}[variable] + "x"
				}

				_, err := load(t, minimal+strings.Replace(template, "%s", name, 1), environment)
				if err == nil {
					t.Fatalf("Load() with %s naming %s = nil, want an error", field, name)
				}
				for _, want := range []string{`profile "xcode-27"`, field, name} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("Load() = %q, want it to contain %q", err, want)
					}
				}
			})
		}
	}
}

// A name that the controller does not use stays free for an operator to choose.
func TestLoadAcceptsAProfileThatNamesItsOwnVariables(t *testing.T) {
	body := minimal + `    otel: {enabled: true, endpoint: "https://otel.example.com", headersEnv: VM_OTEL_HEADERS}
`

	if _, err := load(t, body, secrets()); err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}
}

// The telemetry headers of the agent travel with every export, so the endpoint needs TLS as the
// other endpoints do. A loopback address stays on the machine.
func TestLoadRefusesAPlainHTTPAgentTelemetryEndpoint(t *testing.T) {
	otel := func(endpoint string) string {
		return minimal + `    otel: {enabled: true, endpoint: "` + endpoint + `"}
`
	}

	_, err := load(t, otel("http://collector.example.com:4318"), secrets())
	if err == nil || !strings.Contains(err.Error(), "otel.endpoint") ||
		!strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), "xcode-27") {
		t.Fatalf("Load() = %v, want an error that names the profile and otel.endpoint and asks for https", err)
	}

	for _, endpoint := range []string{
		"https://collector.example.com:4318", "http://127.0.0.1:4318", "http://localhost:4318",
		"http://[::1]:4318",
	} {
		if _, err := load(t, otel(endpoint), secrets()); err != nil {
			t.Errorf("Load() with the endpoint %s = %v, want no error", endpoint, err)
		}
	}
}

// The controller pod holds other secrets than its own, such as a proxy URL with a password. Only a
// variable that the operator marks for the VM with the VM_ prefix may go into a machine.
func TestLoadRefusesAProfileVariableThatIsNotForTheVM(t *testing.T) {
	for _, name := range []string{
		"HTTPS_PROXY", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "MY_SECRET", "vm_password",
	} {
		t.Run(name, func(t *testing.T) {
			body := minimal + `    otel: {enabled: true, endpoint: "https://otel.example.com", headersEnv: ` + name + `}
`
			_, err := load(t, body, func(string) string { return "x" })
			if err == nil {
				t.Fatalf("Load() with headersEnv %s = nil, want an error", name)
			}
			for _, want := range []string{`profile "xcode-27"`, "otel.headersEnv", name, "VM_"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Load() = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadAcceptsAVMVariableForEachField(t *testing.T) {
	body := minimal + `    startupTransport: ssh
    vmCredentials: {usernameEnv: VM_USER, passwordEnv: VM_PASSWORD}
    otel: {enabled: true, endpoint: "https://otel.example.com", headersEnv: VM_OTEL_HEADERS}
`

	if _, err := load(t, body, func(string) string { return "x" }); err != nil {
		t.Fatalf("Load() = %v, want no error", err)
	}
}

// The most specific message wins: a controller secret is a controller secret, not only a name
// without the prefix.
func TestLoadNamesTheControllerSecretBeforeTheVMPrefixRule(t *testing.T) {
	body := minimal + `    otel: {enabled: true, endpoint: "https://otel.example.com", headersEnv: BK_AGENT_STACK_ORCHARD_TOKEN}
`

	_, err := load(t, body, secrets())
	if err == nil || !strings.Contains(err.Error(), "belongs to the controller") ||
		strings.Contains(err.Error(), "must start with") {
		t.Errorf("Load() = %v, want only the controller secret message", err)
	}
}
