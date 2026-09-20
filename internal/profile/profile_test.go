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
	withCredentials.VMCredentials = &profile.Credentials{UsernameEnv: "U", PasswordEnv: "P"}

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
	ssh.VMCredentials = &profile.Credentials{UsernameEnv: "U", PasswordEnv: "P"}

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
