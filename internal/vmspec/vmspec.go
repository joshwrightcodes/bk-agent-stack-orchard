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
	"log/slog"
	"regexp"
	"strings"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

// Orchard recommends a name of 63 characters or fewer.
const maxNameLength = 63

// namePrefix is what marks a machine as one that this controller created.
const namePrefix = "bk-"

// Resource names that the Orchard scheduler counts.
const (
	resourceVMs    = "org.cirruslabs.tart-vms"
	resourceCores  = "org.cirruslabs.logical-cores"
	resourceMemory = "org.cirruslabs.memory-mib"
)

// jobUUIDFormat is the shape of a Buildkite job identifier.
var jobUUIDFormat = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Credentials are the image credentials that the SSH transport needs. The values come from the
// environment that the profile names, never from the configuration file.
type Credentials struct {
	Username string
	Password config.Secret
}

// Request is everything that one virtual machine needs.
type Request struct {
	ControllerID string
	Queue        string
	JobUUID      string
	JobToken     config.Secret
	Profile      profile.Profile

	// ImageCredentials is necessary only for a profile that uses the SSH transport.
	ImageCredentials *Credentials

	// TraceParent and OTelHeaders travel to the agent only when the profile enables telemetry.
	TraceParent string
	OTelHeaders config.Secret
}

// Script is the startup script that the Orchard worker runs in the guest.
type Script struct {
	Transport     string            `json:"transport,omitempty"`
	ScriptContent string            `json:"script_content"`
	Env           map[string]string `json:"env,omitempty"`
}

// VM is the specification that the controller sends to Orchard.
type VM struct {
	Name            string            `json:"name"`
	Image           string            `json:"image"`
	ImagePullPolicy string            `json:"imagePullPolicy"`
	OS              string            `json:"os"`
	CPU             uint64            `json:"cpu"`
	Memory          uint64            `json:"memory"`
	DiskSize        uint64            `json:"diskSize"`
	Headless        bool              `json:"headless"`
	RandomSerial    bool              `json:"randomSerial"`
	NetSoftnet      bool              `json:"netSoftnet"`
	NetSoftnetAllow []string          `json:"netSoftnetAllow,omitempty"`
	RestartPolicy   string            `json:"restart_policy"`
	Username        string            `json:"username,omitempty"`
	Password        string            `json:"password,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Resources       map[string]uint64 `json:"resources"`
	HostDirs        []string          `json:"hostDirs,omitempty"`
	StartupScript   *Script           `json:"startup_script,omitempty"`
}

// LogValue gives a view of the specification that carries no secret. The specification must
// carry the job acquisition token, and the image password when a profile uses SSH, because the
// worker needs both. A log line needs neither.
func (v VM) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", v.Name),
		slog.String("image", v.Image),
		slog.Uint64("cpu", v.CPU),
		slog.Uint64("memoryMiB", v.Memory),
		slog.Uint64("diskSizeGB", v.DiskSize),
		slog.Bool("netSoftnet", v.NetSoftnet),
		slog.String("restartPolicy", v.RestartPolicy),
		slog.String("transport", v.transport()),
		slog.Bool("hasImageCredentials", v.Username != ""),
	)
}

func (v VM) transport() string {
	if v.StartupScript == nil {
		return ""
	}

	return v.StartupScript.Transport
}

// String hides the specification from every format verb, because it carries the token of a job.
func (v VM) String() string {
	return fmt.Sprintf("VM(%s)", v.Name)
}

// GoString hides the specification from the %#v verb.
func (v VM) GoString() string {
	return fmt.Sprintf("vmspec.VM{Name:%q}", v.Name)
}

// Name is the name of the machine that serves one job.
func Name(controllerID, jobUUID string) string {
	return namePrefix + controllerID + "-" + jobUUID
}

// OwnedJob reports the job of a machine that this controller created. A machine of another
// controller, or of another tool, belongs to nobody here.
func OwnedJob(name, controllerID string) (string, bool) {
	prefix := namePrefix + controllerID + "-"

	job, found := strings.CutPrefix(name, prefix)
	if !found || !jobUUIDFormat.MatchString(job) {
		return "", false
	}

	return job, true
}

// Build gives the specification of one job virtual machine.
func Build(r Request) (VM, error) {
	if err := r.validate(); err != nil {
		return VM{}, err
	}

	name := Name(r.ControllerID, r.JobUUID)
	if len(name) > maxNameLength {
		return VM{}, fmt.Errorf(
			"the name %q is %d characters, and Orchard recommends %d or fewer: use a shorter controllerID",
			name, len(name), maxNameLength)
	}

	vm := VM{
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
			resourceVMs:    1,
			resourceCores:  r.Profile.CPU,
			resourceMemory: r.Profile.MemoryMiB,
		},

		StartupScript: &Script{
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
		"BUILDKITE_AGENT_ACQUIRE_JOB": r.JobUUID,
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
	if !jobUUIDFormat.MatchString(r.JobUUID) {
		problems = append(problems, fmt.Errorf("the job UUID %q is not a job identifier", r.JobUUID))
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
