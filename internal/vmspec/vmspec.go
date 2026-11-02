// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"maps"
	"slices"
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

	// Tags are the literal tags of the job, without the queue. The agent carries them, so that
	// the job page shows what the machine stands for.
	Tags map[string]string

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

// ImageTagsPath is the file of agent tags that an image MAY hold: one key=value on each line, for
// example the versions of macOS and of Xcode that the image was built with.
const ImageTagsPath = "/usr/local/etc/bk-agent-stack-orchard/agent-tags"

// scriptContent starts one agent for one job. The token, the job identifier, the name, and the tags
// stay in the environment, so that a log of the script cannot carry them and no value is ever shell
// text. The script is the same for every machine.
//
// Before the agent starts, the script:
//
//   - names the host after the machine, so that Buildkite shows which machine ran a job. Every
//     machine is a clone of one image, so each has the host name of the image until then. A host
//     name is a convenience: when sudo or scutil fails, the host keeps the name of the image, the
//     script writes a line about it, and the agent still starts;
//   - adds the tags of the image after the tags of the controller. A tag of the image with a key
//     that the controller already set, or with a character that a tag list cannot carry, does
//     not go to the agent.
//
// Orchard puts set -e and the export lines before the script, so the exit status of the agent is
// that of the script.
const scriptContent = `if ! sudo -n /usr/sbin/scutil --set HostName "$BUILDKITE_AGENT_NAME" ||
	! sudo -n /usr/sbin/scutil --set LocalHostName "$BUILDKITE_AGENT_NAME" ||
	! sudo -n /usr/sbin/scutil --set ComputerName "$BUILDKITE_AGENT_NAME"; then
	echo "the host name stays that of the image, because sudo or scutil failed" >&2
fi
tags=$BUILDKITE_AGENT_TAGS
if [ -r ` + ImageTagsPath + ` ]; then
	while IFS= read -r tag || [ -n "$tag" ]; do
		key=${tag%%=*}
		case $tag in
		"$key" | "="* | *[!A-Za-z0-9._:/@+=-]*) continue ;;
		esac
		case ",$tags," in
		*",$key="*) continue ;;
		esac
		tags=$tags,$tag
	done <` + ImageTagsPath + `
fi
export BUILDKITE_AGENT_TAGS="$tags"
exec buildkite-agent start
`

// agentTags gives the tags of the agent as the list that the agent reads: the three fixed tags
// first, then the tags of the job in the order of their keys.
func (r Request) agentTags() string {
	list := []string{"queue=" + r.Queue, "agent-stack=orchard", "profile=" + r.Profile.Name}

	for _, key := range slices.Sorted(maps.Keys(r.Tags)) {
		list = append(list, key+"="+r.Tags[key])
	}

	return strings.Join(list, ",")
}

// environment gives the machine the values that the agent needs, and nothing else. The job
// acquisition token is the only credential here, and it opens one job.
func (r Request) environment(name string) map[string]string {
	env := map[string]string{
		"BUILDKITE_AGENT_TOKEN":       r.JobToken.Reveal(),
		"BUILDKITE_AGENT_ACQUIRE_JOB": r.JobID.String(),
		"BUILDKITE_AGENT_NAME":        name,
		"BUILDKITE_AGENT_TAGS":        r.agentTags(),
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

	for key, value := range r.Tags {
		switch {
		case key == "", value == "", strings.ContainsAny(key+value, profile.UnsafeInTagList),
			strings.TrimSpace(key) != key, strings.TrimSpace(value) != value:
			problems = append(problems, fmt.Errorf("the tag %q=%q is empty, or it has a comma, "+
				"a newline, a quote, or white space at an end", key, value))
		case key == "queue", key == "agent-stack", key == "profile":
			problems = append(problems, fmt.Errorf("the tag %q is one that the controller sets", key))
		}
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
