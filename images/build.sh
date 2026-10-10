#!/usr/bin/env bash
# Builds a job VM image of the fleet into a local Tart VM, and pushes it when a registry is given.
#
#   images/build.sh agent   macOS Golden Gate without Xcode: every scenario but the iOS build
#   images/build.sh pilot   macOS Golden Gate with Xcode 27: the image of the pilot (PR-026)
#
# Both add buildkite-agent v4 and the environment hook to a published Cirrus Labs base, close SSH
# and Screen Sharing, and check the result in a second boot. The base carries Homebrew, the Tart
# guest agent, and admin with passwordless sudo.
#
# The build drives the VM with tart exec, through the Tart guest agent, and not with SSH. It needs
# an Apple silicon Mac with Tart: a macOS VM cannot run another macOS VM, so a hosted GitHub runner,
# which is a VM, cannot run it. The pilot needs about 160 GB of free disk.
#
# Environment:
#   ARTIFACT_DESTINATION  s3://, gs://, rt://, or an Azure blob URL: the owner-managed storage of
#                         the artifacts (PR-032). Without it, the hook makes every upload fail.
#   VM_NAME               the local Tart VM that the build makes (bkaso-<image> by default).
#   IMAGE_REGISTRY        a registry and namespace, for example ghcr.io/joshwrightcodes. With it,
#                         the build pushes bkaso-<image>:<tag> there. TART_REGISTRY_USERNAME and
#                         TART_REGISTRY_PASSWORD give the credentials.
#   IMAGE_TAG             the tag of the push (the commit of this repository by default).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
IMAGE="${1:-}"

# The bases, pinned by digest: agent is macos-golden-gate-base of 2026-10-03, and pilot is the
# Golden Gate base with Xcode 27 and its simulator runtimes, built by templates/xcode.pkr.hcl of
# cirruslabs/macos-image-templates (2026-09-22).
case "$IMAGE" in
agent) BASE="ghcr.io/cirruslabs/macos-golden-gate-base@sha256:5302a9ea6cd68c059d00fa253fe75c8ebdf4e0dc8c45d4e136ecc6e7a4c02b70" ;;
pilot) BASE="ghcr.io/cirruslabs/macos-golden-gate-xcode@sha256:324ea5656dee8ab9b0a0df70fda2cfed8912051ad0eca3b1b883bf6ddac88fab" ;;
*)
	printf 'usage: %s agent|pilot\n' "$0" >&2
	exit 2
	;;
esac

VM="${VM_NAME:-bkaso-${IMAGE}}"
DESTINATION="${ARTIFACT_DESTINATION:-}"
REGISTRY="${IMAGE_REGISTRY:-}"
TAG="${IMAGE_TAG:-$(git -C "$HERE" rev-parse --short=12 HEAD 2>/dev/null || date -u +%Y%m%d%H%M%S)}"

# buildkite-agent, and the SHA-256 that its release publishes in its SHA256SUMS.
AGENT_VERSION="4.3.0"
AGENT_SHA256="f6b6278bb8c2dfa193e51e971cdc260e7a6e419c94b2b77c409fe28ea279e81d"

say() { printf '%s-image: %s\n' "$IMAGE" "$*"; }
die() {
	printf '%s-image: %s\n' "$IMAGE" "$*" >&2
	exit 1
}

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

# tart list can fail while a VM runs, so the build reads the directory of the VM.
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

# A job VM runs Buildkite jobs, and a pilot VM builds, tests, and publishes iOS and macOS apps, so the
# image keeps nothing else that the base installs: the agents of other CI systems and AI assistants
# in both, and in the pilot the Java, Android, and Flutter of the Xcode template.
say "removing what a job does not need"
run <<SCRIPT
set -euo pipefail
eval "\$(/opt/homebrew/bin/brew shellenv)"
brew uninstall --force gitlab-runner codex >/dev/null 2>&1 || true
brew uninstall --cask --force claude-code amazon-q >/dev/null 2>&1 || true
rm -rf ~/actions-runner
if [[ "${IMAGE}" == pilot ]]; then
	brew uninstall --force openjdk@17 xcodes >/dev/null 2>&1 || true
	rm -rf ~/android-sdk ~/flutter
	sed -i '' -e '/openjdk@17/d' -e '/ANDROID_HOME/d' -e '/ANDROID_SDK_ROOT/d' -e '/FLUTTER_HOME/d' -e '/flutter/d' ~/.zprofile
fi
brew cleanup --prune=all >/dev/null 2>&1 || true
SCRIPT

say "installing the configuration and the environment hook"
tart exec -i "$VM" /bin/sh -c 'cat > ~/.buildkite-agent/buildkite-agent.cfg' <"$HERE/files/buildkite-agent.cfg"
sed "s|__ARTIFACT_DESTINATION__|${DESTINATION}|" "$HERE/files/environment" |
	tart exec -i "$VM" /bin/sh -c 'cat > ~/.buildkite-agent/hooks/environment && chmod 0755 ~/.buildkite-agent/hooks/environment'

# No well-known credential opens the VM over the network (target design, section 9.2). The Tart
# guest agent needs the automatic login of admin, and macOS 27 does not log in automatically after a
# change of the password, so the image keeps admin/admin for the local login and closes every
# network path that would accept it: SSH (Remote Login) and Screen Sharing. The controller reaches
# the VM through the guest agent (ADR-0009). A person who debugs a VM uses the window of tart run,
# or tart run --vnc-experimental, which Tart serves itself.
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
printf 'xcode=%s\n' "$(/usr/bin/xcodebuild -version 2>/dev/null | head -1)"
eval "$(/opt/homebrew/bin/brew shellenv)"
source ~/.zprofile 2>/dev/null || true
extra=""
for tool in gitlab-runner codex claude q sdkmanager flutter xcodes; do
	command -v "$tool" >/dev/null 2>&1 && extra="$extra $tool"
done
# macOS has a /usr/bin/java that only offers to install Java; java_home finds a real JDK.
/usr/libexec/java_home >/dev/null 2>&1 && extra="$extra java"
for dir in ~/actions-runner ~/android-sdk ~/flutter; do
	[[ -e "$dir" ]] && extra="$extra $dir"
done
printf 'extra=%s\n' "${extra# }"
app=""
for tool in fastlane pod carthage xcbeautify swiftlint; do
	command -v "$tool" >/dev/null 2>&1 && app="$app $tool"
done
printf 'app=%s\n' "${app# }"
SCRIPT
)
printf '%s\n' "$check" | sed "s/^/${IMAGE}-image:   /"

want_hook="none://artifact-storage-is-not-configured"
[[ -n "$DESTINATION" ]] && want_hook="${DESTINATION}/p/b/j"

grep -qx "user=admin" <<<"$check" || die "the guest agent does not run as admin"
grep -q "^agent=/usr/local/bin/buildkite-agent buildkite-agent version ${AGENT_VERSION}" <<<"$check" ||
	die "the VM does not run buildkite-agent ${AGENT_VERSION} from /usr/local/bin"
grep -qx "v3=absent" <<<"$check" || die "the buildkite-agent@3 of the base image remains"
grep -qx "hook=${want_hook}" <<<"$check" || die "the environment hook does not set ${want_hook}"
grep -qx "extra=" <<<"$check" || die "the image keeps what a job does not need: $(grep '^extra=' <<<"$check")"
if [[ "$IMAGE" == pilot ]]; then
	grep -q "^xcode=Xcode 27" <<<"$check" || die "the pilot image does not select Xcode 27"
	grep -qx "app=fastlane pod carthage xcbeautify swiftlint" <<<"$check" ||
		die "the pilot image lacks a tool to build or publish an app: $(grep '^app=' <<<"$check")"
fi

# The ports are checked from this Mac, as the network sees the VM. A process that reaches a VM
# needs the Local Network permission of macOS, which a CI runner gets in System Settings.
ip="$(tart ip "$VM")"
for port in 22 5900; do
	if nc -z -G 3 "$ip" "$port" 2>/dev/null; then
		die "the VM answers on port $port, which must be closed"
	fi
done
say "the guest agent answers as admin, the hook holds, and ports 22 and 5900 are closed"

say "shutting down $VM"
shut_down

if [[ -n "$REGISTRY" ]]; then
	ref="${REGISTRY}/bkaso-${IMAGE}:${TAG}"
	say "pushing $ref"
	tart push "$VM" "$ref"
	say "pushed $ref; a profile names it by the digest that the registry gives"
fi

say "the image is the local Tart VM $VM"
