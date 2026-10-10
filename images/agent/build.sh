#!/usr/bin/env bash
# Builds the agent image of the fleet, without Xcode (W-23, part 1): macOS Golden Gate with
# buildkite-agent v4, its hooks, and the Tart guest agent of the base image. It lets every
# end-to-end scenario run except the iOS build, which needs the image with Xcode.
#
# The build drives the VM with tart exec, through the Tart guest agent, and not with SSH. Tart is
# the only program that reaches the VM, so the build needs no Local Network permission of macOS and
# no password. It needs an Apple silicon Mac itself: a macOS VM cannot run another macOS VM.
#
# Usage: ARTIFACT_DESTINATION=s3://<bucket>/<prefix> images/agent/build.sh
#
# VM_NAME names the local Tart VM that the build makes (bkaso-agent by default).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
VM="${VM_NAME:-bkaso-agent}"
DESTINATION="${ARTIFACT_DESTINATION:-}"

# The base image, pinned by digest. It carries Homebrew, the Tart guest agent, and admin with
# passwordless sudo.
BASE="ghcr.io/cirruslabs/macos-golden-gate-base@sha256:9a2f20d179d6418a128dcb76c593588ea85a263dbfa286e757db63f3d949af7c"

# buildkite-agent, and the SHA-256 that its release publishes.
AGENT_VERSION="4.3.0"
AGENT_SHA256="f6b6278bb8c2dfa193e51e971cdc260e7a6e419c94b2b77c409fe28ea279e81d"

say() { printf 'agent-image: %s\n' "$*"; }
die() { printf 'agent-image: %s\n' "$*" >&2; exit 1; }

# The destinations that buildkite-agent accepts. An empty one is the storage of Buildkite, which
# the hook replaces with one that the agent refuses.
if [[ -n "$DESTINATION" &&
	! "$DESTINATION" =~ ^(s3|gs|rt)://[^[:space:]]+$ &&
	! "$DESTINATION" =~ ^https://[^[:space:]]+\.blob\.core\.windows\.net/[^[:space:]]+$ ]]; then
	die "ARTIFACT_DESTINATION is s3://, gs://, rt://, or an Azure blob URL, which are the destinations that buildkite-agent accepts"
fi

run() { tart exec -i "$VM" /bin/bash -s; }

# bounded runs a command, and stops it after some seconds. A tart exec early in the boot of a VM can
# wait for ever, so each attempt has a limit.
bounded() {
	local seconds=$1
	shift
	"$@" &
	local pid=$!
	(sleep "$seconds" && kill "$pid" 2>/dev/null) &
	local watch=$!
	local status=0
	wait "$pid" 2>/dev/null || status=$?
	{ kill "$watch" && wait "$watch"; } 2>/dev/null || true
	return "$status"
}

# answers waits up to ten minutes for the guest agent of the VM.
answers() {
	for _ in $(seq 1 60); do
		bounded 8 tart exec "$VM" true >/dev/null 2>&1 && return 0
		sleep 2
	done
	return 1
}

# running reports that the VM of this build runs.
running() { pgrep -f "tart run( .*)? ${VM}\$" >/dev/null; }

# shut_down stops the VM from inside: macOS writes its caches to the disk and powers off, and Tart
# then ends. tart stop does not wait for that, and a file that the build wrote just before it is
# lost. tart stop is the last resort, for a VM that does not power off. shutdown is in /sbin,
# which is not on the PATH of the guest agent.
shut_down() {
	running || return 0
	bounded 30 tart exec "$VM" /bin/sh -c 'sync; sudo /sbin/shutdown -h now' >/dev/null 2>&1 || true
	for _ in $(seq 1 90); do
		running || return 0
		sleep 2
	done
	tart stop "$VM" >/dev/null 2>&1 || true
}
trap shut_down EXIT

# tart list fails while any VM runs, so the build reads the directory of the VM.
if [[ -f "${TART_HOME:-${HOME}/.tart}/vms/${VM}/config.json" ]]; then
	tart stop "$VM" >/dev/null 2>&1 || true
	tart delete "$VM"
fi

say "cloning $BASE"
tart clone "$BASE" "$VM"

say "starting $VM"
tart run --no-graphics "$VM" >/dev/null 2>&1 &

answers || die "the guest agent of $VM did not answer inside 10 minutes"

say "installing buildkite-agent $AGENT_VERSION, and removing the buildkite-agent@3 of the base image"
run <<SCRIPT
set -euo pipefail
eval "\$(/opt/homebrew/bin/brew shellenv)"
brew uninstall --force buildkite-agent@3 buildkite-agent >/dev/null 2>&1 || true
sudo rm -rf /opt/homebrew/etc/buildkite-agent
cd /tmp
curl -fsSL -o agent.tar.gz \
	https://github.com/buildkite/agent/releases/download/v${AGENT_VERSION}/buildkite-agent-darwin-arm64-${AGENT_VERSION}.tar.gz
echo '${AGENT_SHA256}  agent.tar.gz' | shasum -a 256 -c -
tar -xzf agent.tar.gz ./buildkite-agent
sudo install -m 0755 buildkite-agent /usr/local/bin/buildkite-agent
rm -f agent.tar.gz buildkite-agent
/usr/local/bin/buildkite-agent --version | grep -q 'version ${AGENT_VERSION}'
mkdir -p ~/.buildkite-agent/hooks ~/buildkite/builds ~/buildkite/plugins
SCRIPT

say "installing the configuration and the environment hook"
tart exec -i "$VM" /bin/sh -c 'cat > ~/.buildkite-agent/buildkite-agent.cfg' <"$HERE/files/buildkite-agent.cfg"
sed "s|__ARTIFACT_DESTINATION__|${DESTINATION}|" "$HERE/files/environment" |
	tart exec -i "$VM" /bin/sh -c 'cat > ~/.buildkite-agent/hooks/environment && chmod 0755 ~/.buildkite-agent/hooks/environment'

# No well-known credential opens the VM over the network (target design, section 9.2). The Tart
# guest agent needs the automatic login of admin, and macOS 27 does not log in automatically after a
# change of the password, so the image keeps admin/admin for the local login and closes every
# network path that would accept it: SSH (Remote Login) and Screen Sharing. The controller reaches
# the VM through the guest agent (ADR-0009), and tart run --vnc still shows the screen of a VM for
# a person who debugs it, because Tart serves that screen itself.
say "turning off SSH and Screen Sharing"
run <<'SCRIPT'
set -euo pipefail
for service in com.openssh.sshd com.apple.screensharing; do
	sudo launchctl disable "system/${service}"
	sudo launchctl bootout "system/${service}" 2>/dev/null || true
done
SCRIPT

say "starting $VM again, to check the image as a job VM finds it"
shut_down
tart run --no-graphics "$VM" >/dev/null 2>&1 &
answers || die "the guest agent of $VM does not answer after a restart"

check=$(tart exec -i "$VM" /bin/bash -s <<'SCRIPT'
printf 'user=%s\n' "$(whoami)"
printf 'agent=%s %s\n' "$(command -v buildkite-agent)" "$(buildkite-agent --version | head -1)"
[[ -e /opt/homebrew/bin/buildkite-agent || -e /opt/homebrew/etc/buildkite-agent ]] && printf 'v3=present\n' || printf 'v3=absent\n'
BUILDKITE_PIPELINE_SLUG=p BUILDKITE_BUILD_ID=b BUILDKITE_JOB_ID=j bash -c '. ~/.buildkite-agent/hooks/environment 2>/dev/null; printf "hook=%s\n" "$BUILDKITE_ARTIFACT_UPLOAD_DESTINATION"'
SCRIPT
)
printf '%s\n' "$check" | sed 's/^/agent-image:   /'

want_hook="none://artifact-storage-is-not-configured"
[[ -n "$DESTINATION" ]] && want_hook="${DESTINATION}/p/b/j"

grep -qx "user=admin" <<<"$check" || die "the guest agent does not run as admin"
grep -q "^agent=/usr/local/bin/buildkite-agent buildkite-agent version ${AGENT_VERSION}" <<<"$check" ||
	die "the VM does not run buildkite-agent ${AGENT_VERSION} from /usr/local/bin"
grep -qx "v3=absent" <<<"$check" || die "the buildkite-agent@3 of the base image remains"
grep -qx "hook=${want_hook}" <<<"$check" || die "the environment hook does not set ${want_hook}"

ip="$(tart ip "$VM")"
for port in 22 5900; do
	if nc -z -G 3 "$ip" "$port" 2>/dev/null; then
		die "the VM answers on port $port, which must be closed"
	fi
done
say "the guest agent answers as admin, the hook holds, and ports 22 and 5900 are closed"

say "shutting down $VM"
shut_down
say "the image is the local Tart VM $VM"
