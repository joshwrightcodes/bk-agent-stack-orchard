// Package vmspec builds the Orchard specification of one job virtual machine, and reads the job
// out of the name of a machine that this controller created.
//
// The specification is where the isolation of an untrusted build becomes concrete: one job for
// each machine, Softnet on, no host directory, and no credential except the token of that job.
// The tests of this package are the guard for those properties.
package vmspec

import (
	"errors"
	"fmt"
	"strings"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// Orchard recommends a name of 63 characters or fewer.
const maxNameLength = 63

// namePrefix is what marks a machine as one that this controller created.
const namePrefix = "bk-"

// Credentials are the image credentials that the SSH transport needs. The values come from the
// environment that the profile names, never from the configuration file.
type Credentials struct {
	Username string
	Password secret.String
}

// Request is everything that one virtual machine needs.
type Request struct {
	ControllerID string
	Queue        string
	JobID        job.ID
	JobToken     secret.String
	Profile      profile.Profile

	// ImageCredentials is necessary only for a profile that uses the SSH transport.
	ImageCredentials *Credentials

	// TraceParent and OTelHeaders travel to the agent only when the profile enables telemetry.
	TraceParent string
	OTelHeaders secret.String
}

// Name is the name of the machine that serves one job.
func Name(controllerID string, id job.ID) string {
	return namePrefix + controllerID + "-" + id.String()
}

// Prefix is what every machine name of this controller starts with. A caller that must tell an
// orphan from a machine of somebody else needs this, because a name with the prefix and no
// identifier belongs to this controller and nothing else will remove it.
func Prefix(controllerID string) string {
	return namePrefix + controllerID + "-"
}

// OwnedJob reports the job of a machine that this controller created. A machine of another
// controller, or of another tool, belongs to nobody here.
func OwnedJob(name, controllerID string) (job.ID, bool) {
	text, found := strings.CutPrefix(name, Prefix(controllerID))
	if !found {
		return "", false
	}

	id, err := job.ParseID(text)
	if err != nil {
		return "", false
	}

	return id, true
}

// Build gives the specification of one job virtual machine.
func Build(r Request) (orchard.VM, error) {
	if err := r.validate(); err != nil {
		return orchard.VM{}, err
	}

	name := Name(r.ControllerID, r.JobID)
	if len(name) > maxNameLength {
		return orchard.VM{}, fmt.Errorf(
			"the name %q is %d characters, and Orchard recommends %d or fewer: use a shorter controllerID",
			name, len(name), maxNameLength)
	}

	vm := orchard.VM{
		Name:            name,
		Image:           r.Profile.Image,
		ImagePullPolicy: "IfNotPresent",
		OS:              "darwin",
		CPU:             r.Profile.CPU,
		Memory:          r.Profile.MemoryMiB,
		DiskSize:        r.Profile.DiskSizeGB,
		Headless:        true,
		RandomSerial:    true,

		// Softnet keeps the build away from the host, from the other machines, and from every
		// private range except the addresses that the profile permits.
		NetSoftnet:      true,
		NetSoftnetAllow: r.Profile.SoftnetAllow,

		// One machine serves one job. Orchard must never start it again for another.
		RestartPolicy: "Never",

		// Labels are placement constraints, so they carry the worker constraints and nothing
		// else. The name, not a label, says which controller owns the machine.
		Labels: r.Profile.WorkerLabels,

		Resources: map[string]uint64{
			orchard.ResourceVMs:       1,
			orchard.ResourceCores:     r.Profile.CPU,
			orchard.ResourceMemoryMiB: r.Profile.MemoryMiB,
		},

		StartupScript: &orchard.Script{
			Transport:     string(r.Profile.StartupTransport),
			ScriptContent: scriptContent,
			Env:           r.environment(name),
		},
	}

	if r.Profile.StartupTransport == profile.TransportSSH {
		vm.Username = r.ImageCredentials.Username
		vm.Password = r.ImageCredentials.Password.Reveal()
	}

	return vm, nil
}

// scriptContent starts one agent for one job. The token and the job identifier stay in the
// environment, so that a log of the script cannot carry them.
const scriptContent = `buildkite-agent start --tags ` +
	`"queue=${BK_AGENT_STACK_QUEUE},agent-stack=orchard,profile=${BK_AGENT_STACK_PROFILE}"`

// environment gives the machine the values that the agent needs, and nothing else. The job
// acquisition token is the only credential here, and it opens one job.
func (r Request) environment(name string) map[string]string {
	env := map[string]string{
		"BUILDKITE_AGENT_TOKEN":       r.JobToken.Reveal(),
		"BUILDKITE_AGENT_ACQUIRE_JOB": r.JobID.String(),
		"BUILDKITE_AGENT_NAME":        name,
		"BK_AGENT_STACK_QUEUE":        r.Queue,
		"BK_AGENT_STACK_PROFILE":      r.Profile.Name,
	}

	if !r.Profile.OTel.Enabled {
		return env
	}

	env["BUILDKITE_OPENTELEMETRY_TRACING"] = "true"
	if r.Profile.OTel.Endpoint != "" {
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = r.Profile.OTel.Endpoint
	}
	if r.OTelHeaders != "" {
		env["OTEL_EXPORTER_OTLP_HEADERS"] = r.OTelHeaders.Reveal()
	}
	if r.TraceParent != "" {
		env["TRACEPARENT"] = r.TraceParent
	}

	return env
}

func (r Request) validate() error {
	var problems []error

	if r.ControllerID == "" {
		problems = append(problems, errors.New("the controller identifier is empty"))
	}
	if r.Queue == "" {
		problems = append(problems, errors.New("the queue is empty"))
	}
	if !r.JobID.Valid() {
		problems = append(problems, fmt.Errorf("the job identifier %q is not a Buildkite job", r.JobID))
	}
	if r.JobToken == "" {
		problems = append(problems, errors.New("the job acquisition token is empty"))
	}

	// Orchard uses admin/admin when a specification names no credentials, so a machine with the
	// SSH transport and no credentials would accept the well-known password.
	if r.Profile.StartupTransport == profile.TransportSSH {
		switch {
		case r.ImageCredentials == nil:
			problems = append(problems, errors.New("the ssh transport needs the image credentials"))
		case r.ImageCredentials.Username == "" || r.ImageCredentials.Password == "":
			problems = append(problems, errors.New("the image credentials are empty"))
		}
	}

	return errors.Join(problems...)
}
