// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

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

// Prefix is what every machine name of this controller starts with. A name that starts with it
// is not always a machine of this controller: the controller whose identifier extends this one
// (macos and macos-prod) shares it. IsOrphan tells the two apart.
func Prefix(controllerID string) string {
	return namePrefix + controllerID + "-"
}

// IsOrphan reports a machine that this controller named and that carries no job: the name has the
// prefix of this controller, and it does not end in a job UUID. Nothing else will remove such a
// machine, so the reaper deletes it.
//
// A name that ends in a dash and a job UUID is the machine of the controller whose identifier is
// the text between bk- and that UUID. When that text is not this controller, the machine belongs
// to another controller, and it is never an orphan of this one, however much the text looks like
// part of a UUID. A name that has this prefix and does not end in a job UUID could belong to any
// controller whose identifier extends this one, and each of them treats it as its own orphan.
func IsOrphan(name, controllerID string) bool {
	text, found := strings.CutPrefix(name, Prefix(controllerID))
	if !found {
		return false
	}

	return !endsInJob(text)
}

// jobIDLength is the length of the text form of a job UUID.
const jobIDLength = 36

// endsInJob reports that the text is a job UUID, or that it ends in a dash and a job UUID.
func endsInJob(text string) bool {
	if job.ID(text).Valid() {
		return true
	}

	start := len(text) - jobIDLength - 1
	if start < 0 || text[start] != '-' {
		return false
	}

	return job.ID(text[start+1:]).Valid()
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
	// The configuration resolves the agent of each profile. A profile that it did not resolve
	// takes the default, so no machine starts without an agent source.
	r.Profile.Agent = r.Profile.Agent.WithDefaults(profile.DefaultAgent)

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
			ScriptContent: script(r.Profile.Agent.Source),
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

// script gives the startup script of each machine of an agent source. The token, the job
// identifier, the name, the tags, and the archive stay in the environment, so that a log of the
// script cannot carry them and no value is ever shell text. The script is the same for every
// machine of one source.
//
// Before the agent starts, the script:
//
//   - names the host after the machine, so that Buildkite shows which machine ran a job. Every
//     machine is a clone of one image, so each has the host name of the image until then. A host
//     name is a convenience: when sudo or scutil fails, the host keeps the name of the image, the
//     script writes a line about it, and the agent still starts;
//   - adds the tags of the image after the tags of the controller. A tag of the image with a key
//     that the controller already set, or with a character that a tag list cannot carry, does
//     not go to the agent;
//   - removes the credential helpers of ~/.gitconfig when it holds more than one. Git Credential
//     Manager writes two (an empty one that resets the list, then itself), and buildkite-agent
//     v4.9.0 sets its own helper with git config --global and no --replace-all, which git refuses
//     while two exist. The machine then has the helper of the system, as an image without the
//     manager has, and the agent adds its own;
//   - finds the agent: it downloads the archive of the profile and checks its SHA-256, or it
//     checks that the agent of the image is v4. A failure here ends the script before any agent
//     starts, with a line that names the step, and Orchard then fails the machine.
//
// Orchard puts set -e and the export lines before the script, so the exit status of the agent is
// that of the script.
func script(source profile.AgentSource) string {
	if source == profile.AgentImage {
		return scriptPrelude + imageAgent
	}

	return scriptPrelude + downloadedAgent
}

const scriptPrelude = `if ! sudo -n /usr/sbin/scutil --set HostName "$BUILDKITE_AGENT_NAME" ||
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
helpers=$(git config --global --get-all credential.helper 2>/dev/null | awk 'END { print NR }')
if [ "$helpers" -gt 1 ] && git config --global --unset-all credential.helper; then
	echo "removed the $helpers credential.helper lines of the global git configuration, because buildkite-agent cannot replace more than one" >&2
fi
`

// downloadedAgent downloads the archive into a new directory, checks it, and runs its agent. The
// archive is small, so the script extracts all of it and then checks that it held the agent: a
// mirror can name its members with or without ./. The build paths are those of the images of this
// project, set here so that a configuration file in the image cannot move them.
const downloadedAgent = `agent_dir=$(mktemp -d "${TMPDIR:-/tmp}/bkaso-agent.XXXXXX")
if ! curl -fsSL --retry 3 --retry-all-errors --connect-timeout 10 --max-time 120 \
	-o "$agent_dir/agent.tar.gz" "$BKASO_AGENT_URL"; then
	echo "the VM could not download buildkite-agent from $BKASO_AGENT_URL" >&2
	exit 1
fi
if ! echo "$BKASO_AGENT_SHA256  $agent_dir/agent.tar.gz" | shasum -a 256 -c - >/dev/null 2>&1; then
	echo "the archive from $BKASO_AGENT_URL does not match agent.sha256 $BKASO_AGENT_SHA256" >&2
	exit 1
fi
if ! tar -xzf "$agent_dir/agent.tar.gz" -C "$agent_dir" || [ ! -x "$agent_dir/buildkite-agent" ]; then
	echo "the archive from $BKASO_AGENT_URL holds no buildkite-agent" >&2
	exit 1
fi
rm -f "$agent_dir/agent.tar.gz"
export BUILDKITE_BUILD_PATH="$HOME/buildkite/builds"
export BUILDKITE_HOOKS_PATH="$HOME/.buildkite-agent/hooks"
export BUILDKITE_PLUGINS_PATH="$HOME/buildkite/plugins"
exec "$agent_dir/buildkite-agent" start
`

// imageAgent runs the agent of the image, once it is v4. The upstream base image carries v3, and
// this controller supports only v4.
const imageAgent = `version=$(buildkite-agent --version 2>/dev/null) || version=
case $version in
"buildkite-agent version 4."*) ;;
*)
	echo "the image has \"${version:-no buildkite-agent}\", and the controller needs buildkite-agent v4: put v4 in the image, or set agent.source to download" >&2
	exit 1
	;;
esac
exec buildkite-agent start
`

// profileTag is the key of the agent tag that names the profile of a machine. A job cannot use the
// key, so the one tag with it is the tag of the controller.
const profileTag = "profile"

// tagsVariable is the variable that carries the tags of the agent.
const tagsVariable = "BUILDKITE_AGENT_TAGS"

// ProfileOf gives the profile that the tags of a machine name, and empty text for a machine whose
// tags name none. The specification of a machine carries its tags, and Orchard gives the
// specification back when it lists the machines, so a process that did not start the machine,
// such as the process after a restart, can still name its profile.
//
// The name is what the specification says, and nothing more: a caller that needs a profile of its
// configuration looks the name up there.
func ProfileOf(vm orchard.VM) string {
	if vm.StartupScript == nil {
		return ""
	}

	for tag := range strings.SplitSeq(vm.StartupScript.Env[tagsVariable], ",") {
		if name, found := strings.CutPrefix(tag, profileTag+"="); found {
			return name
		}
	}

	return ""
}

// agentTags gives the tags of the agent as the list that the agent reads: the three fixed tags
// first, then the tags of the job in the order of their keys.
func (r Request) agentTags() string {
	list := []string{"queue=" + r.Queue, "agent-stack=orchard", profileTag + "=" + r.Profile.Name}

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
		tagsVariable:                  r.agentTags(),
	}

	if r.Profile.Agent.Source == profile.AgentDownload {
		env["BKASO_AGENT_URL"] = r.Profile.Agent.URL
		env["BKASO_AGENT_SHA256"] = r.Profile.Agent.SHA256
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

	problems = append(problems, r.Profile.Agent.Validate()...)

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
