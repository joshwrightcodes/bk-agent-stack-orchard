// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package profile_test

import (
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

func valid() profile.Profile {
	return profile.Profile{
		Name:             "xcode-27",
		Match:            map[string]string{"xcode": "27"},
		Image:            "ghcr.io/acme/macos-golden-gate-xcode@sha256:abc",
		CPU:              8,
		MemoryMiB:        28672,
		DiskSizeGB:       150,
		WorkerLabels:     map[string]string{"pool": "general"},
		StartupTransport: profile.TransportGuestAgent,
	}
}

func TestValidAcceptsAProfile(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want no error", err)
	}
}

func TestValidateRejectsAName(t *testing.T) {
	for _, name := range []string{"", "Xcode", "xcode_27", "xcode 27", "xcode.27"} {
		p := valid()
		p.Name = name

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with name %q = nil, want an error", name)
		}
	}
}

func TestValidateRejectsAMatchMapThatIsNotLiteral(t *testing.T) {
	cases := map[string]map[string]string{
		"an empty key":      {"": "27"},
		"an empty value":    {"xcode": ""},
		"the queue key":     {"queue": "macos"},
		"an upper-case key": {"Xcode": "27"},
	}

	for name, match := range cases {
		p := valid()
		p.Match = match

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with %s = nil, want an error", name)
		}
	}
}

// The matcher skips every job for a rule that names a tag that the controller sets, or one that
// the agent cannot carry in its tag list. A profile with such a map could never match a job.
func TestValidateRejectsAMatchMapThatNoJobCanMatch(t *testing.T) {
	cases := map[string]map[string]string{
		"the profile key":     {"profile": "large"},
		"the agent-stack key": {"agent-stack": "orchard"},
		"a comma":             {"xcode": "27,28"},
		"a double quote":      {"xcode": `2"7`},
		"a single quote":      {"xcode": "2'7"},
		"a newline":           {"xcode": "27\n"},
		"a carriage return":   {"xcode": "2\r7"},
		"leading padding":     {"xcode": " 27"},
		"trailing padding":    {"xcode": "27 "},
	}

	for name, match := range cases {
		p := valid()
		p.Match = match

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with %s = nil, want an error", name)
		}
	}
}

func TestValidateAcceptsAnEmptyMatchMap(t *testing.T) {
	p := valid()
	p.Match = map[string]string{}

	if err := p.Validate(); err != nil {
		t.Errorf("Validate() with an empty match map = %v, want no error", err)
	}
}

func TestValidateRejectsAnUnknownTransport(t *testing.T) {
	p := valid()
	p.StartupTransport = "telepathy"

	err := p.Validate()
	if err == nil {
		t.Fatal("Validate() with an unknown transport = nil, want an error")
	}
	if !strings.Contains(err.Error(), "telepathy") {
		t.Errorf("Validate() = %q, want it to name the transport", err)
	}
}

// The guest-agent transport carries no credential, and the SSH transport needs one. A profile
// that breaks either rule would put a standing credential in a VM specification, or would start
// a VM that the worker cannot reach.
func TestValidateEnforcesTheCredentialRuleOfEachTransport(t *testing.T) {
	withCredentials := valid()
	withCredentials.StartupTransport = profile.TransportGuestAgent
	withCredentials.VMCredentials = &profile.Credentials{UsernameEnv: "VM_U", PasswordEnv: "VM_P"}

	if err := withCredentials.Validate(); err == nil {
		t.Error("Validate() with the guest agent and credentials = nil, want an error")
	}

	withoutCredentials := valid()
	withoutCredentials.StartupTransport = profile.TransportSSH

	if err := withoutCredentials.Validate(); err == nil {
		t.Error("Validate() with SSH and no credentials = nil, want an error")
	}

	ssh := valid()
	ssh.StartupTransport = profile.TransportSSH
	ssh.VMCredentials = &profile.Credentials{UsernameEnv: "VM_U", PasswordEnv: "VM_P"}

	if err := ssh.Validate(); err != nil {
		t.Errorf("Validate() with SSH and credentials = %v, want no error", err)
	}
}

func TestValidateRejectsASizeOfZero(t *testing.T) {
	for name, change := range map[string]func(*profile.Profile){
		"cpu":        func(p *profile.Profile) { p.CPU = 0 },
		"memoryMiB":  func(p *profile.Profile) { p.MemoryMiB = 0 },
		"diskSizeGB": func(p *profile.Profile) { p.DiskSizeGB = 0 },
		"image":      func(p *profile.Profile) { p.Image = "" },
	} {
		p := valid()
		change(&p)

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with no %s = nil, want an error", name)
		}
	}
}

// An image without a digest can change under the fleet. That is a warning, because a development
// fleet uses a tag, and the controller still starts.
func TestWarningsReportsAnImageWithoutADigest(t *testing.T) {
	tagged := valid()
	tagged.Image = "ghcr.io/acme/macos-golden-gate-xcode:27"

	warnings := tagged.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "digest") {
		t.Errorf("Warnings() = %v, want one warning about the digest", warnings)
	}

	if warnings := valid().Warnings(); len(warnings) != 0 {
		t.Errorf("Warnings() with a digest = %v, want none", warnings)
	}
}

func TestTimeoutsUseTheDefaultsUntilTheProfileOverridesThem(t *testing.T) {
	defaults := profile.Timeouts{
		Unplaced:      2 * time.Minute,
		Boot:          15 * time.Minute,
		Acquire:       5 * time.Minute,
		MaxVMLifetime: 25 * time.Hour,
	}

	p := valid()
	p.Timeouts.Boot = 20 * time.Minute

	got := p.EffectiveTimeouts(defaults)

	if got.Boot != 20*time.Minute {
		t.Errorf("Boot = %v, want the override of 20m", got.Boot)
	}
	if got.Unplaced != 2*time.Minute || got.Acquire != 5*time.Minute {
		t.Errorf("EffectiveTimeouts() = %+v, want the defaults for the rest", got)
	}
	if got.MaxVMLifetime != 25*time.Hour {
		t.Errorf("MaxVMLifetime = %v, want the default", got.MaxVMLifetime)
	}
}

func TestStartTimeoutSumAddsTheThreeTimeoutsThatPrecedeTheAgent(t *testing.T) {
	timeouts := profile.Timeouts{
		Unplaced:      2 * time.Minute,
		Boot:          15 * time.Minute,
		Acquire:       5 * time.Minute,
		MaxVMLifetime: 25 * time.Hour,
	}

	if got, want := timeouts.StartSum(), 22*time.Minute; got != want {
		t.Errorf("StartSum() = %v, want %v", got, want)
	}
}

func TestValidateRefusesASoftnetAllowThatIsNotACIDR(t *testing.T) {
	cases := map[string]string{
		"a bare address":          "10.20.30.40",
		"a word":                  "everything",
		"an empty entry":          "",
		"an address with a zone":  "fe80::1%en0/64",
		"a prefix with host bits": "10.0.0.1/16",
		"a prefix too long":       "10.0.0.0/33",
	}

	for name, entry := range cases {
		p := valid()
		p.SoftnetAllow = []string{entry}

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with %s (%q) = nil, want an error", name, entry)
		}
	}
}

func TestValidateRefusesADuplicateInSoftnetAllow(t *testing.T) {
	p := valid()
	p.SoftnetAllow = []string{"10.20.30.40/32", "10.20.30.40/32"}

	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("Validate() = %v, want an error that names the duplicate", err)
	}
}

func TestValidateRefusesTheDefaultRoute(t *testing.T) {
	for _, entry := range []string{"0.0.0.0/0", "::/0"} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with softnetAllow %q = nil, want an error", entry)
		}
	}
}

func TestValidateRefusesARangeWiderThanSlash16(t *testing.T) {
	for _, entry := range []string{"10.0.0.0/15", "10.0.0.0/8", "128.0.0.0/1"} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		if err := p.Validate(); err == nil {
			t.Errorf("Validate() with softnetAllow %q = nil, want an error", entry)
		}
	}

	accepted := []string{"100.64.0.0/16", "192.168.100.0/24", "10.20.30.40/32"}
	p := valid()
	p.SoftnetAllow = accepted

	if err := p.Validate(); err != nil {
		t.Errorf("Validate() with softnetAllow %v = %v, want no error", accepted, err)
	}
}

// Softnet picks the /29 network of each VM from 10.0.0.0/8, 172.16.0.0/12, and 192.168.0.0 to
// 192.168.99.255, and the host takes the first address of that network as the gateway of the VM.
// A range wider than /29 inside those ranges holds the gateway of some VM, so the controller
// refuses it.
func TestValidateRefusesAWideRangeInsideTheSoftnetNetworks(t *testing.T) {
	for _, entry := range []string{
		"192.168.0.0/16", "10.20.0.0/16", "172.16.0.0/24", "172.31.255.0/24", "192.168.99.0/24",
		"192.168.96.0/20", "10.20.30.32/28",
	} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), "Softnet") || !strings.Contains(err.Error(), "/29") {
			t.Errorf("Validate() with softnetAllow %q = %v, want an error that names the Softnet "+
				"networks and the /29 limit", entry, err)
		}
	}

	// A range outside those ranges, and a /29 or narrower range inside them, can be permitted.
	accepted := []string{
		"192.168.100.0/24", "192.168.128.0/17", "172.32.0.0/16", "100.64.0.0/16",
		"10.20.30.40/29", "10.20.30.44/30", "10.20.30.40/32",
	}
	p := valid()
	p.SoftnetAllow = accepted

	if err := p.Validate(); err != nil {
		t.Errorf("Validate() with softnetAllow %v = %v, want no error", accepted, err)
	}
}

// A narrow range that holds the address that Softnet gives the host on the network of a VM is
// permitted, because a mirror can have that address, but the controller warns: the pf rules of the
// hosts cannot tell that address apart from the host.
func TestValidateWarnsAboutAnAllowThatHoldsAGatewayOfSoftnet(t *testing.T) {
	for entry, gateway := range map[string]string{
		"10.20.30.41/32":    "10.20.30.41",
		"10.20.30.40/29":    "10.20.30.41",
		"10.20.30.40/31":    "10.20.30.41",
		"172.16.5.0/30":     "172.16.5.1",
		"192.168.99.249/32": "192.168.99.249",
	} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		warnings := p.Warnings()
		if len(warnings) != 2 || !strings.Contains(warnings[1], gateway) ||
			!strings.Contains(warnings[1], "gateway") || !strings.Contains(warnings[1], p.Name) {
			t.Errorf("Warnings() with softnetAllow %q = %v, want a second warning that names %s "+
				"as a gateway", entry, warnings, gateway)
		}
	}

	// No gateway: the address is not the first of its /29, or it is outside the Softnet ranges.
	for _, entry := range []string{"10.20.30.40/32", "10.20.30.42/31", "10.20.30.44/30", "192.168.100.1/32"} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		if warnings := p.Warnings(); len(warnings) != 1 {
			t.Errorf("Warnings() with softnetAllow %q = %v, want only the warning about every pipeline",
				entry, warnings)
		}
	}
}

// Softnet reads each allow entry as an IPv4 CIDR, so an IPv6 prefix, in any form, cannot work.
func TestValidateRefusesAnIPv6PrefixInSoftnetAllow(t *testing.T) {
	for _, entry := range []string{"2001:db8::/48", "2001:db8::1/128", "::/0", "::ffff:10.0.0.0/112"} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "IPv4") {
			t.Errorf("Validate() with softnetAllow %q = %v, want an error that says IPv4 only", entry, err)
		}
	}
}

// Softnet also takes @host and @peers. The first opens the Mac host to the build, and the second
// opens the other virtual machines, so the controller refuses both and says why.
func TestValidateRefusesTheSoftnetTargetsHostAndPeers(t *testing.T) {
	for _, entry := range []string{"@host", "@peers"} {
		p := valid()
		p.SoftnetAllow = []string{entry}

		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), "@host and @peers") {
			t.Errorf("Validate() with softnetAllow %q = %v, want an error that explains the refusal", entry, err)
		}
	}
}

// The pipeline author selects the profile with the agent tags, so the allow of a profile belongs to
// every pipeline on the queue.
func TestValidateWarnsThatEveryPipelineOnTheQueueGetsTheAllow(t *testing.T) {
	p := valid()
	p.SoftnetAllow = []string{"10.20.30.40/32"}

	warnings := p.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "every pipeline on the queue") ||
		!strings.Contains(warnings[0], p.Name) {
		t.Errorf("Warnings() = %v, want one warning that names the profile and every pipeline", warnings)
	}

	if warnings := valid().Warnings(); len(warnings) != 0 {
		t.Errorf("Warnings() without an allow = %v, want none", warnings)
	}
}
