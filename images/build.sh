#!/usr/bin/env bash
# Builds a job VM image of the fleet into a local Tart VM, and pushes it when a registry is given.
#
#   images/build.sh agent   macOS Golden Gate without Xcode: every scenario but the iOS build
#   images/build.sh pilot   macOS Golden Gate with Xcode 27: the image of the pilot (PR-026)
#
# Both start from the vanilla macOS image (ADR-0012), add the Tart guest agent, buildkite-agent v4,
# and the environment hook, close SSH and Screen Sharing, and check the result in a second boot
# against a list of what the image may hold. The agent image adds the Command Line Tools. The pilot
# image adds Xcode from an archive that the owner downloads, its iOS simulator runtime, Homebrew,
# and the formulae that build and publish an app.
#
# The build drives the VM with tart exec, through the Tart guest agent. It needs an Apple silicon
# Mac with Tart: a macOS VM cannot run another macOS VM, so a hosted GitHub runner, which is a VM,
# cannot run it.
#
# Environment:
#   ARTIFACT_DESTINATION  s3://, gs://, rt://, or an Azure blob URL: the owner-managed storage of
#                         the artifacts (PR-032). Without it, the hook makes every upload fail.
#   XCODE_ARCHIVE         the pilot only: the Xcode archive (~/XcodesCache/Xcode_<version>.xip by
#                         default). The build checks it against XCODE_SHA256.
#   XCODE_URL             the pilot only: an HTTPS URL of the archive, for example a presigned URL
#                         of the owner-managed storage. The build downloads it when XCODE_ARCHIVE
#                         does not exist.
#   VM_NAME               the local Tart VM that the build makes (bkaso-<image> by default).
#   IMAGE_REGISTRY        a registry and namespace, for example ghcr.io/joshwrightcodes. With it,
#                         the build pushes bkaso-<image>:<tag> there. TART_REGISTRY_USERNAME and
#                         TART_REGISTRY_PASSWORD give the credentials.
#   IMAGE_TAG             the tag of the push (the commit of this repository by default).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
IMAGE="${1:-}"

case "$IMAGE" in
agent | pilot) ;;
*)
	printf 'usage: %s agent|pilot\n' "$0" >&2
	exit 2
	;;
esac

# The base, pinned by digest: the vanilla image of Cirrus Labs, which is the restore image of Apple
# with the setup assistant done, admin/admin with passwordless sudo, automatic login, and SSH, and
# nothing else (ADR-0012).
BASE="ghcr.io/cirruslabs/macos-golden-gate-vanilla@sha256:b78a521145ebef24e9916c0b3999e165bb12b587d11524c5fa3ba4fe7bf79bb9"

VM="${VM_NAME:-bkaso-${IMAGE}}"
DESTINATION="${ARTIFACT_DESTINATION:-}"
REGISTRY="${IMAGE_REGISTRY:-}"
TAG="${IMAGE_TAG:-$(git -C "$HERE" rev-parse --short=12 HEAD 2>/dev/null || date -u +%Y%m%d%H%M%S)}"

# buildkite-agent, and the SHA-256 that its release publishes in its SHA256SUMS.
AGENT_VERSION="4.3.0"
AGENT_SHA256="f6b6278bb8c2dfa193e51e971cdc260e7a6e419c94b2b77c409fe28ea279e81d"

# The Tart guest agent, and the SHA-256 that its release publishes. The vanilla image has none.
GUEST_AGENT_VERSION="0.15.0"
GUEST_AGENT_SHA256="eb47f402f18e742a8ea96344115ca776179f9c4c67494bc0c46a10a68db280b7"

# Xcode, from the archive that the owner downloads with an Apple ID: Apple gives it to no one else,
# so a build never signs in to Apple (ADR-0012). The SHA-256 is that of the archive the owner first
# downloaded, and the build also checks that Apple signed it.
XCODE_VERSION="27.0"
XCODE_SHA256="6a270c53a5a0c5e0ac78125342d44c3cfff2716ec390373cd5e30834d80a67c3"
XCODE_ARCHIVE="${XCODE_ARCHIVE:-${HOME}/XcodesCache/Xcode_${XCODE_VERSION}.xip}"

# Homebrew, from the package of its release, and the SHA-256 that GitHub gives for it.
HOMEBREW_VERSION="7.0.9"
HOMEBREW_SHA256="e021295947a4a9b7ca1a8eff310c9ea1db14d923c5acf7d79e325b60a81f1a15"

# The disk of the pilot: Xcode, its iOS simulator runtime, and the formulae need about 50 GB beside
# macOS, and a job needs room for its build. Tart rounds a disk up to whole 4 KiB blocks, and then
# refuses a profile of the same size in GB as smaller (2026-10-10: 100 GB became 100000002048
# bytes). A multiple of 8 GB is whole blocks, so a profile of the size of the image works.
PILOT_DISK_GB=104
PILOT_FREE_GB=20

# What the image may hold in each place that software installs itself (images/README.md). Anything
# else fails the build, so a tool that a new vanilla image adds cannot reach a job VM unseen.
# /Applications/Utilities is the folder of macOS.
ALLOWED_DAEMONS="org.cirruslabs.tart-guest-daemon.plist"
ALLOWED_AGENTS="org.cirruslabs.tart-guest-agent.plist"
ALLOWED_LOCAL="buildkite-agent tart-guest-agent"
case "$IMAGE" in
agent)
	ALLOWED_APPS="Safari.app Utilities"
	ALLOWED_FORMULAE=""
	;;
pilot)
	ALLOWED_APPS="Safari.app Utilities Xcode.app"
	ALLOWED_FORMULAE="carthage cocoapods fastlane swiftlint xcbeautify"
	;;
esac

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

# The pilot checks its Xcode archive before it makes a VM, so a missing or changed archive fails in
# seconds.
if [[ "$IMAGE" == pilot ]]; then
	if [[ ! -f "$XCODE_ARCHIVE" ]]; then
		[[ "${XCODE_URL:-}" =~ ^https:// ]] ||
			die "no Xcode archive at $XCODE_ARCHIVE: download Xcode $XCODE_VERSION from https://developer.apple.com/download/all/ and put it there, or set XCODE_ARCHIVE, or set XCODE_URL to an HTTPS URL of it"
		say "downloading the Xcode archive"
		mkdir -p "$(dirname "$XCODE_ARCHIVE")"
		curl -fsSL -o "${XCODE_ARCHIVE}.part" "$XCODE_URL"
		mv "${XCODE_ARCHIVE}.part" "$XCODE_ARCHIVE"
	fi
	say "checking the Xcode archive"
	xcode_sum="$(shasum -a 256 "$XCODE_ARCHIVE" | cut -d' ' -f1)"
	[[ "$xcode_sum" == "$XCODE_SHA256" ]] ||
		die "the Xcode archive $XCODE_ARCHIVE has the SHA-256 $xcode_sum, and XCODE_SHA256 is $XCODE_SHA256"
	# xip does not check the signature ("validation not attempted"), so pkgutil does.
	pkgutil --check-signature "$XCODE_ARCHIVE" | grep -q 'Status: signed Apple Software$' ||
		die "Apple did not sign the Xcode archive $XCODE_ARCHIVE"
fi

# run runs a script in the VM, with the environment that its arguments give.
run() { tart exec -i "$VM" /usr/bin/env "$@" /bin/bash -s; }

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

# running reports that the VM of this build runs: a tart run command that ends with its name.
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

work="$(mktemp -d)"
trap 'shut_down; rm -rf "$work"' EXIT

# xip in a VM ends before the app that it expands is in place, and the app came some minutes later
# or not at all (2026-10-10), so the pilot expands Xcode on this Mac, where xip ends when the app is
# in place. The shared directory of the VM does not follow some symbolic links of Xcode, so the VM
# gets Xcode as one ditto archive, which keeps the links, the modes, and the extended attributes.
if [[ "$IMAGE" == pilot ]]; then
	say "expanding the Xcode archive"
	mkdir "$work/expand" "$work/xcode"
	(cd "$work/expand" && xip --expand "$XCODE_ARCHIVE" >/dev/null)
	xcode_apps=("$work"/expand/*.app)
	[[ ${#xcode_apps[@]} -eq 1 && -d "${xcode_apps[0]}" ]] ||
		die "the Xcode archive holds $(ls "$work/expand"), not one app"
	mv "${xcode_apps[0]}" "$work/expand/Xcode.app" 2>/dev/null || true
	ditto -c --keepParent "$work/expand/Xcode.app" "$work/xcode/Xcode.cpio"
	rm -rf "$work/expand"
fi

# tart list can fail while a VM runs, so the build reads the directory of the VM.
if [[ -f "${TART_HOME:-${HOME}/.tart}/vms/${VM}/config.json" ]]; then
	tart stop "$VM" >/dev/null 2>&1 || true
	tart delete "$VM"
fi

say "cloning $BASE"
tart clone "$BASE" "$VM"

# The pilot gets a larger disk, and reads the Xcode archive from a read-only directory of this Mac,
# which macOS mounts in /Volumes/My Shared Files. Only the first boot has the directory.
# The name of the VM ends the command, because running finds the process of the VM by it.
start=(tart run --no-graphics)
if [[ "$IMAGE" == pilot ]]; then
	tart set "$VM" --disk-size "$PILOT_DISK_GB"
	start+=("--dir=xcode:${work}/xcode:ro")
fi
start+=("$VM")

say "starting $VM"
"${start[@]}" >/dev/null 2>&1 &

# The vanilla image has no guest agent, so the build puts it in place over SSH, as admin with the
# password of the vanilla image, and turns SSH off later. ssh reads a password only from a terminal
# or from SSH_ASKPASS, so a small script gives it.
printf '#!/bin/sh\necho admin\n' >"$work/askpass"
chmod 0700 "$work/askpass"
# vm_ssh runs ssh or scp, with the options before the arguments of the command.
vm_ssh() {
	local tool=$1
	shift
	SSH_ASKPASS="$work/askpass" SSH_ASKPASS_REQUIRE=force "$tool" -o StrictHostKeyChecking=no \
		-o UserKnownHostsFile=/dev/null -o PubkeyAuthentication=no -o LogLevel=ERROR "$@"
}

ip="$(tart ip --wait 120 "$VM")"
for _ in $(seq 1 90); do nc -z -G 2 "$ip" 22 2>/dev/null && break; sleep 2; done

say "installing the Tart guest agent $GUEST_AGENT_VERSION"
curl -fsSL -o "$work/guest-agent.tar.gz" \
	"https://github.com/openai/tart-guest-agent/releases/download/v${GUEST_AGENT_VERSION}/tart-guest-agent-darwin-all.tar.gz"
echo "${GUEST_AGENT_SHA256}  $work/guest-agent.tar.gz" | shasum -a 256 -c - >/dev/null ||
	die "the Tart guest agent does not match its SHA-256"
tar -xzf "$work/guest-agent.tar.gz" -C "$work" tart-guest-agent
vm_ssh scp -q "$work/tart-guest-agent" "$HERE/files/tart-guest-daemon.plist" \
	"$HERE/files/tart-guest-agent.plist" "admin@${ip}:/tmp/"
vm_ssh ssh "admin@${ip}" /bin/bash -s <<'SCRIPT'
set -euo pipefail
sudo mkdir -p /usr/local/bin
sudo install -m 0755 /tmp/tart-guest-agent /usr/local/bin/tart-guest-agent
sudo install -o root -g wheel -m 0644 /tmp/tart-guest-daemon.plist \
	/Library/LaunchDaemons/org.cirruslabs.tart-guest-daemon.plist
sudo install -o root -g wheel -m 0644 /tmp/tart-guest-agent.plist \
	/Library/LaunchAgents/org.cirruslabs.tart-guest-agent.plist
rm -f /tmp/tart-guest-agent /tmp/tart-guest-daemon.plist /tmp/tart-guest-agent.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/org.cirruslabs.tart-guest-daemon.plist
sudo launchctl bootstrap "gui/$(id -u)" /Library/LaunchAgents/org.cirruslabs.tart-guest-agent.plist
SCRIPT

answers || die "the guest agent of $VM did not answer inside 10 minutes"

if [[ "$IMAGE" == agent ]]; then
	# git and the other build tools of a job come with the Command Line Tools. softwareupdate
	# installs them without a person when the marker file below exists, as the installer of
	# Homebrew does it.
	say "installing the Command Line Tools"
	run <<'SCRIPT'
set -euo pipefail
marker=/tmp/.com.apple.dt.CommandLineTools.installondemand.in-progress
touch "$marker"
label=$(softwareupdate -l 2>/dev/null | sed -n 's/^[[:space:]]*\* Label: \(Command Line Tools.*\)$/\1/p' | sort -V | tail -1)
[[ -n "$label" ]] || { echo "softwareupdate offers no Command Line Tools" >&2; exit 1; }
sudo softwareupdate -i "$label" --agree-to-license >/dev/null
rm -f "$marker"
/usr/bin/git --version
SCRIPT
else
	# macOS 27 grows its APFS container to the disk that tart set gave it, and moves the recovery
	# partition to the end, at boot (2026-10-10), so the build needs no diskutil.
	say "the disk has $(tart exec "$VM" df -g / | awk 'NR == 2 {print $4}') GB free"

	# The VM unpacks Xcode from the read-only directory. Xcode then
	# installs its packages, and downloads the iOS simulator runtime from Apple, which needs no
	# Apple ID.
	say "installing Xcode $XCODE_VERSION and its iOS simulator runtime"
	run <<'SCRIPT'
set -euo pipefail
archive="/Volumes/My Shared Files/xcode/Xcode.cpio"
for _ in $(seq 1 60); do [[ -f "$archive" ]] && break; sleep 2; done
[[ -f "$archive" ]] || { echo "the VM does not see $archive" >&2; exit 1; }
sudo ditto -x "$archive" /Applications
sudo chown -R root:wheel /Applications/Xcode.app
sudo xcode-select -s /Applications/Xcode.app
sudo xcodebuild -license accept
sudo xcodebuild -runFirstLaunch >/dev/null
xcodebuild -downloadPlatform iOS >/dev/null
xcrun simctl runtime dyld_shared_cache update --all || true
/usr/bin/xcodebuild -version | head -1
SCRIPT

	# Homebrew, and only the formulae of the list. A job finds them through the environment hook.
	say "installing Homebrew $HOMEBREW_VERSION and $ALLOWED_FORMULAE"
	run VERSION="$HOMEBREW_VERSION" SHA256="$HOMEBREW_SHA256" FORMULAE="$ALLOWED_FORMULAE" <<'SCRIPT'
set -euo pipefail
cd /tmp
curl -fsSL -o Homebrew.pkg "https://github.com/Homebrew/brew/releases/download/${VERSION}/Homebrew.pkg"
echo "${SHA256}  Homebrew.pkg" | shasum -a 256 -c - >/dev/null
sudo installer -pkg Homebrew.pkg -target / >/dev/null
rm -f Homebrew.pkg
eval "$(/opt/homebrew/bin/brew shellenv)"
export HOMEBREW_NO_ANALYTICS=1 HOMEBREW_NO_ENV_HINTS=1
brew analytics off
# shellcheck disable=SC2086
brew install --formula --quiet $FORMULAE
brew cleanup --prune=all >/dev/null 2>&1 || true
echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >> ~/.zprofile
SCRIPT
fi

say "installing buildkite-agent $AGENT_VERSION"
run VERSION="$AGENT_VERSION" SHA256="$AGENT_SHA256" <<'SCRIPT'
set -euo pipefail
sudo mkdir -p /usr/local/bin
cd /tmp
curl -fsSL -o agent.tar.gz \
	"https://github.com/buildkite/agent/releases/download/v${VERSION}/buildkite-agent-darwin-arm64-${VERSION}.tar.gz"
echo "${SHA256}  agent.tar.gz" | shasum -a 256 -c -
tar -xzf agent.tar.gz ./buildkite-agent
sudo install -m 0755 buildkite-agent /usr/local/bin/buildkite-agent
rm -f agent.tar.gz buildkite-agent
/usr/local/bin/buildkite-agent --version | grep -q "version ${VERSION}"
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

say "starting $VM again, without the directory of the archive, to check the image as a job VM finds it"
shut_down
! running || die "$VM still runs after it shut down, so the check would not see a new boot"
tart run --no-graphics "$VM" >/dev/null 2>&1 &
answers || die "the guest agent of $VM does not answer after a restart"

# The check looks at the VM as a job does: with the PATH of the guest agent, and the environment
# hook on top.
check=$(tart exec -i "$VM" /bin/bash -s <<'SCRIPT'
list() { ls "$@" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//'; }
printf 'user=%s\n' "$(whoami)"
printf 'agent=%s %s\n' "$(command -v buildkite-agent)" "$(buildkite-agent --version | head -1)"
printf 'git=%s\n' "$(/usr/bin/git --version 2>/dev/null)"
printf 'xcode=%s\n' "$(/usr/bin/xcodebuild -version 2>/dev/null | head -1)"
printf 'ios_runtime=%s\n' "$(xcrun simctl list runtimes 2>/dev/null | grep -c '^iOS ')"
printf 'free_gb=%s\n' "$(df -g / | awk 'NR == 2 {print $4}')"
printf 'apps=%s\n' "$(list /Applications)"
printf 'daemons=%s\n' "$(list /Library/LaunchDaemons)"
printf 'agents=%s\n' "$(list /Library/LaunchAgents)"
printf 'user_agents=%s\n' "$(list ~/Library/LaunchAgents)"
printf 'local=%s\n' "$(list /usr/local/bin)"
if [[ -x /opt/homebrew/bin/brew ]]; then
	printf 'formulae=%s\n' "$(/opt/homebrew/bin/brew leaves --installed-on-request | sort | tr '\n' ' ' | sed 's/ $//')"
	printf 'casks=%s\n' "$(/opt/homebrew/bin/brew list --cask -1 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//')"
else
	printf 'formulae=\ncasks=\n'
fi
printf 'wwdr=%s\n' "$(security find-certificate -a -c 'Apple Worldwide Developer Relations' /Library/Keychains/System.keychain 2>/dev/null | grep -c '"labl"')"
BUILDKITE_PIPELINE_SLUG=p BUILDKITE_BUILD_ID=b BUILDKITE_JOB_ID=j bash -c '
. ~/.buildkite-agent/hooks/environment 2>/dev/null
printf "hook=%s\n" "$BUILDKITE_ARTIFACT_UPLOAD_DESTINATION"
app=""
for tool in fastlane pod carthage xcbeautify swiftlint; do
	command -v "$tool" >/dev/null 2>&1 && app="$app $tool"
done
printf "app=%s\n" "${app# }"
extra=""
for tool in gitlab-runner codex claude q sdkmanager flutter xcodes; do
	command -v "$tool" >/dev/null 2>&1 && extra="$extra $tool"
done
/usr/libexec/java_home >/dev/null 2>&1 && extra="$extra java"
printf "extra=%s\n" "${extra# }"'
SCRIPT
)
printf '%s\n' "$check" | sed "s/^/${IMAGE}-image:   /"

# want checks one line of the check.
want() { grep -qxF "$1" <<<"$check" || die "$2: $(grep "^${1%%=*}=" <<<"$check"), want $1"; }

want_hook="none://artifact-storage-is-not-configured"
[[ -n "$DESTINATION" ]] && want_hook="${DESTINATION}/p/b/j"

want "user=admin" "the guest agent does not run as admin"
grep -q "^agent=/usr/local/bin/buildkite-agent buildkite-agent version ${AGENT_VERSION}" <<<"$check" ||
	die "the VM does not run buildkite-agent ${AGENT_VERSION} from /usr/local/bin"
grep -q "^git=git version" <<<"$check" || die "the image has no git"
want "hook=${want_hook}" "the environment hook does not set the destination"
for line in "apps=${ALLOWED_APPS}" "daemons=${ALLOWED_DAEMONS}" "agents=${ALLOWED_AGENTS}" \
	"user_agents=" "local=${ALLOWED_LOCAL}" "formulae=${ALLOWED_FORMULAE}" "casks="; do
	want "$line" "the image holds what its list does not name"
done
want "extra=" "the image holds what a job does not need"
if [[ "$IMAGE" == pilot ]]; then
	grep -q "^xcode=Xcode ${XCODE_VERSION%.0}" <<<"$check" || die "the pilot image does not select Xcode ${XCODE_VERSION}"
	grep -q '^ios_runtime=[1-9]' <<<"$check" || die "the pilot image has no iOS simulator runtime"
	want "app=fastlane pod carthage xcbeautify swiftlint" "a job of the pilot does not find a tool to build or publish an app"
	free="$(sed -n 's/^free_gb=//p' <<<"$check")"
	((free >= PILOT_FREE_GB)) || die "the pilot image has $free GB free, and a job needs $PILOT_FREE_GB GB"
fi

# The ports are checked from this Mac, as the network sees the VM. A process that reaches a VM
# needs the Local Network permission of macOS, which a CI runner gets in System Settings.
ip="$(tart ip "$VM")"
for port in 22 5900; do
	if nc -z -G 3 "$ip" "$port" 2>/dev/null; then
		die "the VM answers on port $port, which must be closed"
	fi
done
say "the image holds only its list, the hook holds, and ports 22 and 5900 are closed"

say "shutting down $VM"
shut_down

if [[ -n "$REGISTRY" ]]; then
	ref="${REGISTRY}/bkaso-${IMAGE}:${TAG}"
	say "pushing $ref"
	tart push "$VM" "$ref"
	say "pushed $ref; a profile names it by the digest that the registry gives"
fi

say "the image is the local Tart VM $VM"
