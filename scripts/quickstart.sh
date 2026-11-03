#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# The quick start: one real job in a real Tart VM on this Mac, from a real Buildkite queue.
#
# It starts a local Orchard (orchard dev, with one worker that uses the Tart of this Mac), then the
# controller with one profile, and prints a pipeline step to paste into Buildkite. Ctrl-C deletes
# the VMs that it started, stops what it started, and deletes its work directory. The image stays
# in the Tart cache.
#
# Usage: BUILDKITE_AGENT_TOKEN=... scripts/quickstart.sh --queue NAME [options]
#
# The token is a cluster agent token for the queue. Read it from your secret store into the
# environment. The script never writes it to a file.
#
# It works with the bash of macOS (3.2).
set -euo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd)"
# The real home. orchard dev gets a home of its own below, and Tart must keep using this one.
REAL_HOME="$HOME"

QUEUE=""
IMAGE="ghcr.io/cirruslabs/macos-golden-gate-base:latest"
CPU=4
MEMORY=8192
DISK=50
MATCH=()
ORCHARD_URL=""
SERVICE_ACCOUNT="bk-agent-stack-orchard"
HEALTH="127.0.0.1:18080"
CONTROLLER_ID="quickstart"
MIN_TART="2.39.0"

say() { printf 'quickstart: %s\n' "$*"; }
die() {
	printf 'quickstart: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
Usage: BUILDKITE_AGENT_TOKEN=... $0 --queue NAME [options]

  --queue NAME           the self-hosted Buildkite queue that the token belongs to (required)
  --image REF            the Tart image of the VMs (default: $IMAGE)
  --cpu N                the CPU cores of each VM (default: $CPU)
  --memory MIB           the memory of each VM, in MiB (default: $MEMORY)
  --disk GB              the disk of each VM, in GB, at least the disk of the image (default: $DISK)
  --match KEY=VALUE      a tag that a job must have, besides the queue; repeat for more tags
  --orchard-url URL      use an Orchard that you run, instead of a local one. Its service account
                         token goes in BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN, and a private CA in
                         BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT
  --service-account NAME the Orchard service account of --orchard-url (default: $SERVICE_ACCOUNT)
EOF
	exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--queue) QUEUE="${2:-}"; shift 2 ;;
	--image) IMAGE="${2:-}"; shift 2 ;;
	--cpu) CPU="${2:-}"; shift 2 ;;
	--memory) MEMORY="${2:-}"; shift 2 ;;
	--disk) DISK="${2:-}"; shift 2 ;;
	--match) MATCH+=("${2:-}"); shift 2 ;;
	--orchard-url) ORCHARD_URL="${2:-}"; shift 2 ;;
	--service-account) SERVICE_ACCOUNT="${2:-}"; shift 2 ;;
	-h | --help) usage 0 ;;
	*) printf 'quickstart: unknown option %s\n\n' "$1" >&2; usage 2 ;;
	esac
done

# --- Checks. Each one stops with one message that says what to do. ---

[[ -n "$QUEUE" ]] || die "--queue is required: the self-hosted queue of your cluster agent token"
[[ "$(uname -s)" == Darwin && "$(uname -m)" == arm64 ]] ||
	die "the VMs need a Mac with Apple silicon, and this is $(uname -s) on $(uname -m)"

TOKEN="${BUILDKITE_AGENT_TOKEN:-${BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN:-}}"
[[ -n "$TOKEN" ]] ||
	die "set BUILDKITE_AGENT_TOKEN to a cluster agent token for the queue $QUEUE, from your secret store"

for n in "$CPU" "$MEMORY" "$DISK"; do
	[[ "$n" =~ ^[1-9][0-9]*$ ]] || die "--cpu, --memory, and --disk take whole numbers, not $n"
done
for tag in ${MATCH[@]+"${MATCH[@]}"}; do
	[[ "$tag" =~ ^[a-z0-9][a-z0-9_.-]*=[^,\"\'[:space:]]+$ ]] ||
		die "--match takes KEY=VALUE, with a lower-case key and no comma, quote, or space: $tag"
done

command -v tart >/dev/null ||
	die "Tart is not installed: brew trust openai/tools && brew install openai/tools/tart openai/tools/softnet"
version="$(tart --version)"
[[ "$(printf '%s\n%s\n' "$MIN_TART" "$version" | sort -V | head -1)" == "$MIN_TART" ]] ||
	die "Tart $version is older than $MIN_TART: upgrade it"

# The VMs use Softnet, which must be set-user-ID root to start a VM without a password prompt.
softnet="$(command -v softnet || true)"
[[ -n "$softnet" ]] || die "Softnet is not installed: brew install openai/tools/softnet"
softnet="$(realpath "$softnet")"
if [[ "$(stat -f '%Su %Sp' "$softnet")" != root\ ???s* ]]; then
	die "Softnet needs the set-user-ID bit, so that a VM can start without a password prompt:
  sudo chown root \"$softnet\" && sudo chmod u+s \"$softnet\""
fi

command -v jq >/dev/null || die "jq is not on the PATH: run this through mise (mise run quickstart)"

ORCHARD=""
if [[ -z "$ORCHARD_URL" ]]; then
	ORCHARD="$(command -v orchard || true)"
	[[ -n "$ORCHARD" ]] || die "orchard is not on the PATH: run this through mise (mise run quickstart)"
	if curl -s -o /dev/null --max-time 2 http://127.0.0.1:6120/; then
		die "something already answers on port 6120, the port of a local Orchard: stop it, or give its
  address with --orchard-url"
	fi
else
	[[ -n "${BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN:-}" ]] ||
		die "--orchard-url needs the service account token in BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN"
fi

mkdir -p "$HOME/.tart"
free_gb="$(df -g "$HOME/.tart" | awk 'NR == 2 { print $4 }')"
if [[ "$free_gb" -lt $((DISK + 10)) ]]; then
	say "warning: $free_gb GB is free, and the image and one VM can need $((DISK + 10)) GB or more"
fi

# --- The work directory, and the cleanup. ---

tmp="${TMPDIR:-/tmp}"
WORK="$(mktemp -d "${tmp%/}/bkaso-quickstart.XXXXXX")"
ORCHARD_PID=""
CONTROLLER_PID=""
FOLLOW_PID=""

# orchard_api METHOD PATH: one call to Orchard. A token travels in the configuration of curl on
# standard input, never on its command line.
orchard_api() {
	local url="${ORCHARD_URL:-http://127.0.0.1:6120}" ca=()
	if [[ -n "${BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT:-}" ]]; then
		printf '%s\n' "$BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT" >"$WORK/ca.crt"
		ca=(--cacert "$WORK/ca.crt")
	fi
	if [[ -n "$ORCHARD_URL" ]]; then
		printf 'user = "%s:%s"\n' "$SERVICE_ACCOUNT" "$BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN"
	else
		printf '\n'
	fi | curl -sS -K - ${ca[@]+"${ca[@]}"} -X "$1" "$url$2"
}

our_vms() {
	orchard_api GET /v1/vms 2>/dev/null |
		jq -r --arg p "bk-$CONTROLLER_ID-" '.[]? | select(.name | startswith($p)) | .name' 2>/dev/null || true
}

cleanup() {
	trap - EXIT INT TERM
	printf '\n'
	say "stopping"
	if [[ -n "$FOLLOW_PID" ]]; then
		pkill -P "$FOLLOW_PID" 2>/dev/null || true
		kill "$FOLLOW_PID" 2>/dev/null || true
	fi
	if [[ -n "$CONTROLLER_PID" ]]; then
		kill "$CONTROLLER_PID" 2>/dev/null || true
		wait "$CONTROLLER_PID" 2>/dev/null || true
	fi

	# The controller keeps no state, so it leaves its VMs. Delete each one that it started.
	local vm left
	for vm in $(our_vms); do
		say "deleting the VM $vm"
		orchard_api DELETE "/v1/vms/$vm" >/dev/null 2>&1 || true
	done
	for _ in $(seq 1 30); do
		left="$(our_vms)"
		[[ -z "$left" ]] && break
		sleep 2
	done
	[[ -n "${left:-}" ]] && say "warning: Orchard still lists these VMs: $left"

	if [[ -n "$ORCHARD_PID" ]]; then
		kill "$ORCHARD_PID" 2>/dev/null || true
		wait "$ORCHARD_PID" 2>/dev/null || true
		# A VM that Orchard started and did not stop.
		for vm in $(tart list --source local --format json 2>/dev/null |
			jq -r --arg p "orchard-bk-$CONTROLLER_ID-" '.[] | select(.Name | startswith($p)) | .Name'); do
			say "deleting the Tart VM $vm"
			tart stop "$vm" >/dev/null 2>&1 || true
			tart delete "$vm" >/dev/null 2>&1 || true
		done
	fi

	rm -rf "$WORK"
	say "done. The image stays in the Tart cache; delete it with: tart delete $IMAGE"
}
trap cleanup EXIT
# Ctrl-C is how the quick start ends, so it is a success.
trap 'cleanup; exit 0' INT
trap 'cleanup; exit 143' TERM

# --- The image. A pull here shows its progress, and keeps it out of the boot timeout of the VM. ---

say "pulling $IMAGE (the first pull can be tens of GB)"
tart pull "$IMAGE"

# --- Orchard. ---

if [[ -z "$ORCHARD_URL" ]]; then
	say "starting a local Orchard with one worker on this Mac"
	mkdir -p "$WORK/home"
	# orchard dev writes a context into $HOME/.orchard, so it gets a home of its own. Tart keeps
	# its cache and its VMs in the real home, so that the pulled image serves the VMs.
	HOME="$WORK/home" TART_HOME="$REAL_HOME/.tart" "$ORCHARD" dev --data-dir "$WORK/orchard-data" \
		>"$WORK/orchard.log" 2>&1 &
	ORCHARD_PID=$!
	for _ in $(seq 1 60); do
		if [[ "$(orchard_api GET /v1/workers 2>/dev/null | jq 'length' 2>/dev/null || echo 0)" -ge 1 ]]; then
			break
		fi
		kill -0 "$ORCHARD_PID" 2>/dev/null || die "Orchard stopped: $(tail -5 "$WORK/orchard.log")"
		sleep 1
	done
	[[ "$(orchard_api GET /v1/workers | jq 'length')" -ge 1 ]] ||
		die "the Orchard worker did not report in 60 s: $(tail -5 "$WORK/orchard.log")"
fi

# --- The controller. ---

say "building the controller"
(cd "$HERE" && go build -o "$WORK/controller" ./cmd/bk-agent-stack-orchard)

match="{}"
if [[ ${#MATCH[@]} -gt 0 ]]; then
	match="{$(for tag in "${MATCH[@]}"; do printf '"%s": "%s", ' "${tag%%=*}" "${tag#*=}"; done | sed 's/, $//')}"
fi

cat >"$WORK/config.yaml" <<EOF
controllerID: $CONTROLLER_ID
healthAddress: "$HEALTH"
buildkite:
  queue: "$QUEUE"
orchard:
  url: "${ORCHARD_URL:-http://127.0.0.1:6120}"
  serviceAccount: "$SERVICE_ACCOUNT"
profiles:
  - name: quickstart
    match: $match
    image: "$IMAGE"
    cpu: $CPU
    memoryMiB: $MEMORY
    diskSizeGB: $DISK
EOF

orchard_token="${BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN:-quickstart}"
BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN="$TOKEN" \
	BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN="$orchard_token" \
	OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none \
	"$WORK/controller" -config "$WORK/config.yaml" >"$WORK/controller.log" 2>&1 &
CONTROLLER_PID=$!
unset TOKEN orchard_token

# Readable lines: the time, the message, and the job, the profile, and the reason when a record has
# them. A line that is not a record, such as a refusal of the configuration, passes as it is.
readable() {
	jq -R -r --unbuffered '
		. as $line | (try fromjson catch null) as $r
		| if $r == null then $line
		  else ($r.Attributes // [] | map({(.Key): .Value.Value}) | add // {}) as $a
		  | ($r.Timestamp[11:19]) + "  " + ($r.Body.Value | tostring)
		    + ([ (if $a["cicd.pipeline.task.run.id"] then "job=" + $a["cicd.pipeline.task.run.id"] else empty end),
		         (if $a["bk_agent_stack_orchard.reaper.reason"] then "reason=" + $a["bk_agent_stack_orchard.reaper.reason"] else empty end),
		         (if $a.reasons then "why: " + ([$a.reasons[]? | .Value? // .] | join("; ")) else empty end)
		       ] | if length > 0 then "  (" + join(", ") + ")" else "" end)
		  end'
}

follow() { tail -n +1 -F "$WORK/controller.log" 2>/dev/null | readable; }

for _ in $(seq 1 30); do
	grep -q 'the controller is ready' "$WORK/controller.log" 2>/dev/null && break
	if ! kill -0 "$CONTROLLER_PID" 2>/dev/null; then
		readable <"$WORK/controller.log"
		CONTROLLER_PID=""
		die "the controller stopped; its log is above"
	fi
	sleep 1
done
grep -q 'the controller is ready' "$WORK/controller.log" ||
	die "the controller is not ready after 30 s: $(tail -3 "$WORK/controller.log")"

agents="queue: \"$QUEUE\""
for tag in ${MATCH[@]+"${MATCH[@]}"}; do agents="$agents, ${tag%%=*}: \"${tag#*=}\""; done

cat <<EOF

The controller serves the queue $QUEUE. Paste this step into a Buildkite pipeline of the same
cluster, and run a build:

  steps:
    - label: ":apple: Hello from a Tart VM"
      command: sw_vers && hostname && echo "this VM is deleted when the job ends"
      agents: {$agents}

Each job gets a new VM, and Buildkite shows the VM name as the host of the agent. Press Ctrl-C to
stop. The configuration is $WORK/config.yaml until then.

EOF

# The subshell of the follower reports on its own stderr when the cleanup ends its pipeline.
follow 2>/dev/null &
FOLLOW_PID=$!
# The follower ends with the cleanup, and bash must not report that as a job that stopped.
disown "$FOLLOW_PID"
wait "$CONTROLLER_PID" || true
say "the controller stopped"
