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

var (
	nameFormat     = regexp.MustCompile(`^[a-z0-9-]+$`)
	matchKeyFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
)

// Credentials names the environment variables that hold the image credentials. The values never
// appear in the configuration file.
type Credentials struct {
	UsernameEnv string `yaml:"usernameEnv"`
	PasswordEnv string `yaml:"passwordEnv"`
}

// Timeouts limits each phase between the reservation of a job and the agent that acquires it.
// A zero value means that the profile takes the default of the configuration.
type Timeouts struct {
	// Unplaced limits the wait for Orchard to give the virtual machine a worker.
	Unplaced time.Duration `yaml:"unplaced,omitempty"`

	// Boot limits the image pull, the clone, and the start of the virtual machine process.
	Boot time.Duration `yaml:"boot,omitempty"`

	// Acquire limits the guest boot and the agent that acquires the job.
	Acquire time.Duration `yaml:"acquire,omitempty"`

	// MaxVMLifetime deletes a virtual machine that outlives every other limit.
	MaxVMLifetime time.Duration `yaml:"maxVMLifetime,omitempty"`
}

// StartSum is the time that a healthy virtual machine may take before its agent acquires the
// job. The job acquisition token must outlive this sum.
func (t Timeouts) StartSum() time.Duration {
	return t.Unplaced + t.Boot + t.Acquire
}

// OTel carries the optional telemetry of the agent inside the virtual machine.
type OTel struct {
	Enabled    bool   `yaml:"enabled"`
	Endpoint   string `yaml:"endpoint,omitempty"`
	HeadersEnv string `yaml:"headersEnv,omitempty"`
}

// Profile is one type of job virtual machine.
type Profile struct {
	Name             string            `yaml:"name"`
	Match            map[string]string `yaml:"match"`
	Image            string            `yaml:"image"`
	CPU              uint64            `yaml:"cpu"`
	MemoryMiB        uint64            `yaml:"memoryMiB"`
	DiskSizeGB       uint64            `yaml:"diskSizeGB"`
	WorkerLabels     map[string]string `yaml:"workerLabels,omitempty"`
	SoftnetAllow     []string          `yaml:"softnetAllow,omitempty"`
	StartupTransport Transport         `yaml:"startupTransport,omitempty"`
	VMCredentials    *Credentials      `yaml:"vmCredentials,omitempty"`
	Timeouts         Timeouts          `yaml:"timeouts,omitempty"`
	OTel             OTel              `yaml:"otel,omitempty"`
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

// validateMatch keeps the match map to literal tags. A rule that is not a literal cannot select
// exactly one profile, and the queue tag selects the queue, not the profile.
func (p Profile) validateMatch() []error {
	var problems []error

	for key, value := range p.Match {
		switch {
		case key == "queue":
			problems = append(problems,
				errors.New("match must not contain the queue key, because the queue selects the fleet"))
		case !matchKeyFormat.MatchString(key):
			problems = append(problems, fmt.Errorf("match key %q must be a lower-case literal", key))
		case value == "":
			problems = append(problems, fmt.Errorf("match key %q has an empty value", key))
		case strings.ContainsAny(value, "*?!"):
			problems = append(problems,
				fmt.Errorf("match value %q must be a literal, because a wildcard matches more than one profile", value))
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
