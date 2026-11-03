// SPDX-License-Identifier: AGPL-3.0-or-later

// Package profile describes one type of job virtual machine: the jobs that it serves, the image
// that it runs, its size, and how the worker starts its script.
//
// A profile validates itself. The rules that need the whole configuration, such as two profiles
// with the same match map, belong to the configuration.
package profile

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Transport selects how the Orchard worker runs the startup script of a virtual machine.
type Transport string

const (
	// TransportGuestAgent reaches the guest through the control socket. The virtual machine
	// specification then carries no credential.
	TransportGuestAgent Transport = "tart-guest-agent"

	// TransportSSH reaches the guest over SSH with the credentials of the image.
	TransportSSH Transport = "ssh"
)

// UnsafeInTagList holds the characters that a tag must not contain. The agent reads its tags as
// one comma-separated list, so a comma would split a tag in two. A newline, a carriage return, or
// a quote would break the text that carries the list.
const UnsafeInTagList = ",\n\r\"'"

var (
	nameFormat     = regexp.MustCompile(`^[a-z0-9-]+$`)
	matchKeyFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
)

// Credentials names the environment variables that hold the image credentials. The values never
// appear in the configuration file.
type Credentials struct {
	// The environment variable of the controller that holds the user name of the image. It must
	// start with VM_. Required with vmCredentials.
	UsernameEnv string `yaml:"usernameEnv"`

	// The environment variable of the controller that holds the password of the image. It must
	// start with VM_. Required with vmCredentials.
	PasswordEnv string `yaml:"passwordEnv"`
}

// Timeouts limits each phase between the reservation of a job and the agent that acquires it.
// A zero value means that the profile takes the default of the configuration.
type Timeouts struct {
	// How long a VM can wait for Orchard to give it a worker. After this, the VM is deleted, and
	// the job goes back to the queue to start again.
	Unplaced time.Duration `yaml:"unplaced,omitempty"`

	// How long the image pull, the clone, and the start of the VM can take. After this, the job
	// fails and the VM is deleted.
	Boot time.Duration `yaml:"boot,omitempty"`

	// How long the boot of macOS and the start of the agent can take, until the agent takes the
	// job. After this, the job fails with the last lines of the startup script.
	Acquire time.Duration `yaml:"acquire,omitempty"`

	// The longest that any VM lives, whatever the state of its job. A job that runs longer loses
	// its VM.
	MaxVMLifetime time.Duration `yaml:"maxVMLifetime,omitempty"`
}

// StartSum is the time that a healthy virtual machine may take before its agent acquires the
// job. The job acquisition token must outlive this sum.
func (t Timeouts) StartSum() time.Duration {
	return t.Unplaced + t.Boot + t.Acquire
}

// OTel carries the optional telemetry of the agent inside the virtual machine.
type OTel struct {
	// Turns on the OpenTelemetry tracing of the agent in each VM. The spans of the agent then
	// join the trace of the job.
	Enabled bool `yaml:"enabled"`

	// The OTLP endpoint that the agent sends to. It must use https, except on a loopback address.
	// Empty means the default of the agent.
	Endpoint string `yaml:"endpoint,omitempty"`

	// The environment variable of the controller that holds the OTLP headers of the agent, such
	// as a token of the collector. It must start with VM_.
	HeadersEnv string `yaml:"headersEnv,omitempty"`
}

// Profile is one type of job virtual machine.
type Profile struct {
	// The name of the profile: lower-case letters, digits, and dashes. Each profile has its own
	// name. The agent of each VM has the tag profile=NAME. Required.
	Name string `yaml:"name"`

	// The tags that a job must have, besides its queue, to run on this profile. A job runs on the
	// profile whose match map equals its tags exactly, and {} matches a job with no tag other than
	// the queue. Two profiles cannot have the same map. A job that matches no profile is left for
	// another runner of the queue.
	Match map[string]string `yaml:"match"`

	// The Tart image of the VMs, in an OCI registry. It must run the Tart guest agent. Use a
	// digest (@sha256:...), or the controller warns that the image can change. Required.
	Image string `yaml:"image"`

	// The CPU cores of each VM. Required.
	CPU uint64 `yaml:"cpu"`

	// The memory of each VM, in MiB. Required.
	MemoryMiB uint64 `yaml:"memoryMiB"`

	// The disk of each VM, in GB. It must be at least the disk of the image. Required.
	DiskSizeGB uint64 `yaml:"diskSizeGB"`

	// The labels that a worker must have to run the VMs of this profile.
	WorkerLabels map[string]string `yaml:"workerLabels,omitempty"`

	// IPv4 ranges in private networks that the VMs can reach, such as a mirror or an artifact
	// store. Each one is a range with a prefix length from /16 to /32. Every pipeline on the queue
	// can reach them, so the controller warns at start.
	SoftnetAllow []string `yaml:"softnetAllow,omitempty"`

	// How the Orchard worker runs the startup script of a VM: tart-guest-agent, or ssh with
	// vmCredentials.
	StartupTransport Transport `yaml:"startupTransport,omitempty"`

	// The credentials of the image, for startupTransport ssh only.
	VMCredentials *Credentials `yaml:"vmCredentials,omitempty"`

	// The timeouts of this profile. A timeout that is unset or 0 takes the value of
	// defaults.timeouts.
	Timeouts Timeouts `yaml:"timeouts,omitempty"`

	// The telemetry of the agent in each VM.
	OTel OTel `yaml:"otel,omitempty"`

	// Where the agent of each VM comes from. A value that is unset takes the value of
	// defaults.agent.
	Agent Agent `yaml:"agent,omitempty"`
}

// Validate reports every rule that this profile breaks.
func (p Profile) Validate() error {
	var problems []error

	if !nameFormat.MatchString(p.Name) {
		problems = append(problems,
			fmt.Errorf("name %q must contain only lower-case letters, digits, and dashes", p.Name))
	}

	problems = append(problems, p.validateMatch()...)
	problems = append(problems, p.validateTransport()...)
	problems = append(problems, p.validateVMVariables()...)
	problems = append(problems, p.validateSoftnetAllow()...)

	// The configuration gives every profile its agent source before it validates the profile.
	if p.Agent.Source != "" {
		problems = append(problems, p.Agent.Validate()...)
	}

	if p.Image == "" {
		problems = append(problems, errors.New("image must name an image"))
	}
	if p.CPU == 0 {
		problems = append(problems, errors.New("cpu must be more than zero"))
	}
	if p.MemoryMiB == 0 {
		problems = append(problems, errors.New("memoryMiB must be more than zero"))
	}
	if p.DiskSizeGB == 0 {
		problems = append(problems, errors.New("diskSizeGB must be more than zero"))
	}

	return join(p.Name, problems)
}

// ControllerVariablePrefix starts the name of every variable that the controller reads for
// itself.
const ControllerVariablePrefix = "BK_AGENT_STACK_ORCHARD_"

// vmVariablePrefix marks a variable of the controller pod that the operator provides for the
// virtual machines of the profiles.
const vmVariablePrefix = "VM_"

// otelVariablePrefix starts the name of every OpenTelemetry SDK variable. The controller reads
// them for its own export, and the agent in a machine reads the same names.
const otelVariablePrefix = "OTEL_"

// variableField is a profile field that names an environment variable.
type variableField struct{ field, name string }

// validateVMVariables keeps the variables of the controller out of the virtual machines. The values
// of the variables that a profile names go into its machines, and a job runs untrusted code there.
// The pod of the controller holds other secrets, so only a variable that the operator marks with
// the VM_ prefix may go there. The names of the controller stay refused with their own message.
func (p Profile) validateVMVariables() []error {
	var problems []error

	named := []variableField{{"otel.headersEnv", p.OTel.HeadersEnv}}
	if p.VMCredentials != nil {
		named = append(named,
			variableField{"vmCredentials.usernameEnv", p.VMCredentials.UsernameEnv},
			variableField{"vmCredentials.passwordEnv", p.VMCredentials.PasswordEnv})
	}

	for _, n := range named {
		switch {
		case n.name == "":
		case strings.HasPrefix(n.name, ControllerVariablePrefix), strings.HasPrefix(n.name, otelVariablePrefix):
			problems = append(problems, fmt.Errorf(
				"%s names the variable %s, which belongs to the controller, and its value must "+
					"not go into a virtual machine, so name a variable that starts with %s instead", n.field,
				n.name, vmVariablePrefix))
		case !strings.HasPrefix(n.name, vmVariablePrefix):
			problems = append(problems, fmt.Errorf(
				"%s names the variable %s, which must start with %s, because only a variable "+
					"that the operator marks for the virtual machines may go into one", n.field,
				n.name, vmVariablePrefix))
		}
	}

	return problems
}

// widestAllow is the widest IPv4 range that softnetAllow may open. The VM specification passes
// each entry to Softnet, and the pf rules of the hosts open the same entries to every VM of the
// profile, so a wide range would open a whole private network.
const widestAllow = 16

// validateSoftnetAllow keeps each softnetAllow entry to one narrow IPv4 range that is written in
// full. Softnet reads each entry as an IPv4 CIDR, so an IPv6 prefix cannot work. A bare address, a
// prefix with host bits set, a default route, and a duplicate are refused, because each of them
// hides what the profile opens. The Softnet targets @host and @peers are refused on purpose: the
// first opens the Mac host to the build, and the second opens the other virtual machines.
func (p Profile) validateSoftnetAllow() []error {
	var problems []error

	seen := make(map[netip.Prefix]bool, len(p.SoftnetAllow))
	for _, entry := range p.SoftnetAllow {
		if entry == "@host" || entry == "@peers" {
			problems = append(problems, fmt.Errorf(
				"softnetAllow entry %q is refused: the Softnet targets @host and @peers are not "+
					"allowed, because @host opens the Mac host to the build and @peers opens the "+
					"other virtual machines", entry))
			continue
		}

		prefix, err := netip.ParsePrefix(entry)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf(
				"softnetAllow entry %q must be an IPv4 CIDR range with a prefix length, such as 10.20.30.40/32: %w",
				entry, err))
		case !prefix.Addr().Is4():
			problems = append(problems, fmt.Errorf(
				"softnetAllow entry %q must be an IPv4 range, because Softnet takes IPv4 CIDRs only", entry))
		case prefix.Bits() == 0:
			problems = append(problems, fmt.Errorf(
				"softnetAllow entry %q is a default route, which opens every address", entry))
		case prefix != prefix.Masked():
			problems = append(problems, fmt.Errorf(
				"softnetAllow entry %q has host bits set, so write %s", entry, prefix.Masked()))
		case prefix.Bits() < widestAllow:
			problems = append(problems, fmt.Errorf(
				"softnetAllow entry %q is too wide: the widest range is /%d", entry, widestAllow))
		case seen[prefix]:
			problems = append(problems, fmt.Errorf("softnetAllow lists %s twice", prefix))
		default:
			seen[prefix] = true
		}
	}

	return problems
}

// validateMatch keeps the match map to literal tags. A rule that is not a literal cannot select
// exactly one profile, and the queue tag selects the queue, not the profile.
func (p Profile) validateMatch() []error {
	var problems []error

	for key, value := range p.Match {
		switch {
		case key == "queue":
			problems = append(problems,
				errors.New("match must not contain the queue key, because the queue selects the fleet"))
		case key == "profile", key == "agent-stack":
			problems = append(problems, fmt.Errorf(
				"match must not contain the key %q, because the controller sets that tag", key))
		case !matchKeyFormat.MatchString(key):
			problems = append(problems, fmt.Errorf("match key %q must be a lower-case literal", key))
		case value == "":
			problems = append(problems, fmt.Errorf("match key %q has an empty value", key))
		case strings.ContainsAny(value, "*?!"):
			problems = append(problems,
				fmt.Errorf("match value %q must be a literal, because a wildcard matches more than one profile", value))
		case strings.ContainsAny(value, UnsafeInTagList), strings.TrimSpace(value) != value:
			problems = append(problems, fmt.Errorf("match value %q must have no comma, quote, newline, "+
				"or white space at an end, because the agent reads its tags as one list", value))
		}
	}

	return problems
}

// validateTransport enforces the credential rule of each transport. The guest agent needs no
// credential, so a profile that carries one would put a standing secret in every virtual machine
// specification of that profile.
func (p Profile) validateTransport() []error {
	var problems []error

	switch p.StartupTransport {
	case TransportGuestAgent:
		if p.VMCredentials != nil {
			problems = append(problems,
				errors.New("vmCredentials must be absent, because the guest agent needs no credential"))
		}
	case TransportSSH:
		switch {
		case p.VMCredentials == nil:
			problems = append(problems, errors.New("vmCredentials are necessary for the ssh transport"))
		case p.VMCredentials.UsernameEnv == "", p.VMCredentials.PasswordEnv == "":
			problems = append(problems,
				errors.New("vmCredentials must name a usernameEnv and a passwordEnv"))
		}
	default:
		problems = append(problems, fmt.Errorf("startupTransport %q must be %q or %q",
			p.StartupTransport, TransportGuestAgent, TransportSSH))
	}

	return problems
}

// Warnings reports what a fleet operator should know, and what does not stop the controller.
func (p Profile) Warnings() []string {
	var warnings []string

	if p.Image != "" && !strings.Contains(p.Image, "@sha256:") {
		warnings = append(warnings, fmt.Sprintf(
			"profile %q: image %q has no digest, so the image can change under the fleet",
			p.Name, p.Image))
	}

	if len(p.SoftnetAllow) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"profile %q: softnetAllow %v opens those addresses for every pipeline on the queue, "+
				"because the pipeline author selects the profile with the agent tags, so a "+
				"separate trust level needs a separate queue", p.Name, p.SoftnetAllow))
	}

	return warnings
}

// EffectiveTimeouts fills every timeout that the profile leaves at zero with the default.
func (p Profile) EffectiveTimeouts(defaults Timeouts) Timeouts {
	effective := p.Timeouts

	if effective.Unplaced == 0 {
		effective.Unplaced = defaults.Unplaced
	}
	if effective.Boot == 0 {
		effective.Boot = defaults.Boot
	}
	if effective.Acquire == 0 {
		effective.Acquire = defaults.Acquire
	}
	if effective.MaxVMLifetime == 0 {
		effective.MaxVMLifetime = defaults.MaxVMLifetime
	}

	return effective
}

func join(name string, problems []error) error {
	if len(problems) == 0 {
		return nil
	}

	named := make([]error, 0, len(problems))
	for _, problem := range problems {
		named = append(named, fmt.Errorf("profile %q: %w", name, problem))
	}

	return errors.Join(named...)
}
