#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
# Builds a job VM image of the fleet into a local Tart VM, and pushes it when a registry is given.
# It is a reference build for an operator who builds its own images: the project publishes no job
# VM image (ADR-0020).
#
#   images/build.sh agent   macOS Golden Gate without Xcode: every scenario but the iOS build
#   images/build.sh pilot   macOS Golden Gate with Xcode 27: the reference image for an Xcode profile
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
#                         the artifacts (PR-032). Optional: without it, the artifacts go to the
#                         storage of Buildkite.
#   XCODE_ARCHIVE         the pilot only: the Xcode archive (~/XcodesCache/Xcode_<version>.xip by
#                         default). The build checks it against XCODE_SHA256.
#   XCODE_URL             the pilot only: an HTTPS URL of the archive, for example a presigned URL
#                         of the owner-managed storage. The build downloads it when XCODE_ARCHIVE
#                         does not exist.
#   VM_NAME               the local Tart VM that the build makes (bkaso-<image> by default).
#   IMAGE_REGISTRY        a registry and namespace, for example ghcr.io/joshwrightcodes. With it,
#                         the build pushes bkaso-<image>:<tag> there. TART_REGISTRY_USERNAME and
#                         TART_REGISTRY_PASSWORD give the credentials.
#   IMAGE_TAG             the tag of the push (the commit of this repository by default, with
#                         -dirty when the tree has changes).
#
# Usage: images/build.sh agent|pilot        (mise run agent-image, mise run pilot-image)
#
# Exit status: 0 success, 1 failure, 2 usage error.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
IMAGE="${1:-}"

usage() {
	sed -n 's/^# Usage: /usage: /p' "${BASH_SOURCE[0]}" >&2
	exit 2
}

[[ $# -eq 1 ]] || usage
case "$IMAGE" in
agent | pilot) ;;
*) usage ;;
esac

# The base, pinned by digest: the vanilla image of Cirrus Labs, which is the restore image of Apple
# with the setup assistant done, admin/admin with passwordless sudo, automatic login, and SSH, and
# nothing else (ADR-0012). This digest is tag 27.2, macOS 27.2, uploaded 2026-10-31.
BASE="ghcr.io/cirruslabs/macos-golden-gate-vanilla@sha256:b35695ce30453bcc1db961a476740be72a44c2a2e464b73ece1ff0fcac12a873"

VM="${VM_NAME:-bkaso-${IMAGE}}"
DESTINATION="${ARTIFACT_DESTINATION:-}"
REGISTRY="${IMAGE_REGISTRY:-}"
# --exclude='*' leaves out every git tag, so the name is the commit, and -dirty marks an image built
# from a tree with changes.
TAG="${IMAGE_TAG:-$(git -C "$HERE" describe --always --dirty --abbrev=12 --exclude='*' 2>/dev/null || date -u +%Y%m%d%H%M%S)}"

# buildkite-agent, and the SHA-256 that its release publishes in its SHA256SUMS.
AGENT_VERSION="4.9.0"
AGENT_SHA256="faaf0084edf08a92a877253620604fbd6ce496d2851a7ae5126dd19f27429e76"

# The Tart guest agent, and the SHA-256 that its release publishes. The vanilla image has none.
GUEST_AGENT_VERSION="0.15.0"
GUEST_AGENT_SHA256="eb47f402f18e742a8ea96344115ca776179f9c4c67494bc0c46a10a68db280b7"

# Xcode, from the archive that the owner downloads with an Apple ID: Apple gives it to no one else,
# so a build never signs in to Apple (ADR-0012). The SHA-256 is that of the archive the owner first
# downloaded (Xcode 27.2, downloaded 2026-11-02, signed Apple Software), and the build also checks
# that Apple signed it.
XCODE_VERSION="27.2"
XCODE_SHA256="c28b05526225394fa4017d7f9919c1c898822713edca68872cb4701599f01978"
XCODE_ARCHIVE="${XCODE_ARCHIVE:-${HOME}/XcodesCache/Xcode_${XCODE_VERSION}.xip}"
# The simulator that the build boots once, so that its first-boot work is done in the image. The
# pilot test pipeline uses an iPhone 17.
PILOT_SIMULATOR="${PILOT_SIMULATOR:-iPhone 17}"

# The file of agent tags that the startup script of the controller reads (vmspec.ImageTagsPath).
AGENT_TAGS_PATH="/usr/local/etc/bk-agent-stack-orchard/agent-tags"

# Homebrew, from the package of its release, and the SHA-256 that GitHub gives for it.
HOMEBREW_VERSION="7.0.12"
HOMEBREW_SHA256="a9825310dce8d94bc97f70140700089183c3a894b1048501d933865d7c2441bd"

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

# The destinations that buildkite-agent accepts. An empty one is allowed: it is the storage of
# Buildkite, and the hook leaves it alone. The value goes into a sed replacement and into a
# double-quoted string of the hook, so it may hold only the characters of a bucket path, none of
# which sed or the shell reads as syntax.
path_chars='[A-Za-z0-9._~/-]'
if [[ -n "$DESTINATION" &&
	! "$DESTINATION" =~ ^(s3|gs|rt)://${path_chars}+$ &&
	! "$DESTINATION" =~ ^https://[a-z0-9]+\.blob\.core\.windows\.net/${path_chars}+$ ]]; then
	die "ARTIFACT_DESTINATION is s3://, gs://, rt://, or an Azure blob URL, which are the destinations that buildkite-agent accepts, with only letters, digits, and . _ ~ / - in its path"
fi

# The pilot checks its Xcode archive before it makes a VM, so a missing or changed archive fails in
# seconds.
if [[ "$IMAGE" == pilot ]]; then
	# check_xcode checks an archive against XCODE_SHA256 and the signature of Apple. xip does not
	# check the signature ("validation not attempted"), so pkgutil does.
	check_xcode() {
		local sum
		sum="$(shasum -a 256 "$1" | cut -d' ' -f1)"
		[[ "$sum" == "$XCODE_SHA256" ]] ||
			die "the Xcode archive $1 has the SHA-256 $sum, and XCODE_SHA256 is $XCODE_SHA256"
		pkgutil --check-signature "$1" | grep -q 'Status: signed Apple Software$' ||
			die "Apple did not sign the Xcode archive $1"
	}
	if [[ ! -f "$XCODE_ARCHIVE" ]]; then
		[[ "${XCODE_URL:-}" =~ ^https:// ]] ||
			die "no Xcode archive at $XCODE_ARCHIVE: download Xcode $XCODE_VERSION from https://developer.apple.com/download/all/ and put it there, or set XCODE_ARCHIVE, or set XCODE_URL to an HTTPS URL of it"
		say "downloading the Xcode archive"
		mkdir -p "$(dirname "$XCODE_ARCHIVE")"
		# -C - continues a download that an earlier run left in the .part file. The archive takes
		# its name only after the check, so a bad download never looks like a good archive to a
		# later run, and the .part file of a bad download is deleted.
		curl -fsSL --retry 3 -C - -o "${XCODE_ARCHIVE}.part" "$XCODE_URL"
		say "checking the Xcode archive"
		(check_xcode "${XCODE_ARCHIVE}.part") || {
			rm -f "${XCODE_ARCHIVE}.part"
			exit 1
		}
		mv "${XCODE_ARCHIVE}.part" "$XCODE_ARCHIVE"
	else
		say "checking the Xcode archive"
		check_xcode "$XCODE_ARCHIVE"
	fi
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
for _ in $(seq 1 90); do
	nc -z -G 2 "$ip" 22 2>/dev/null && break
	sleep 2
done

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
	run PILOT_SIMULATOR="$PILOT_SIMULATOR" <<'SCRIPT'
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
# Keep only the general iOS runtime. Apple can deliver a device-specific runtime beside it (for
# Xcode 27.2: com.apple.CoreSimulator.SimRuntime.iOS-27-2-iPhone19-4, 2026-11-02). Both answer to
# the same iOS version, so a test destination such as "name=iPhone 17,OS=27.2" on a cold simulator
# can pick the device-specific runtime, which cannot boot that device.
general='^com\.apple\.CoreSimulator\.SimRuntime\.iOS-[0-9]+-[0-9]+$'
xcrun simctl list runtimes | sed -n 's/^iOS .* - \([^ )]*\)) - \(com\.apple\.[^ ]*\).*/\1 \2/p' |
	while read -r build id; do
		[[ "$id" =~ $general ]] && continue
		uuid=$(xcrun simctl runtime list | sed -n "s/.*($build) - \([0-9A-F-]*\) .*/\1/p")
		echo "removing the device-specific runtime $id ($build)"
		xcrun simctl runtime delete "$uuid"
	done
xcrun simctl delete unavailable
xcrun simctl runtime dyld_shared_cache update --all || true
# Boot the simulator of the pilot once and shut it down, so the first-boot data migration of the
# device is done in the image. A job then meets a device that boots in seconds, not one that runs
# its migration while xcodebuild installs the test runner (the first real T-100 jobs, 2026-11-02).
udid=$(xcrun simctl list devices available | sed -n "s/^ *${PILOT_SIMULATOR} (\([0-9A-F-]\{36\}\)) .*/\1/p" | sed -n 1p)
[[ -n "$udid" ]] || { echo "no available ${PILOT_SIMULATOR} simulator" >&2; exit 1; }
xcrun simctl boot "$udid"
xcrun simctl bootstatus "$udid" -b >/dev/null
xcrun simctl shutdown "$udid"
# sed reads all of the output. With pipefail, head can end the pipe early, and xcodebuild then dies
# of SIGPIPE and fails the step (2026-11-02).
/usr/bin/xcodebuild -version | sed -n 1p
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

# The startup script of the controller adds these tags to the tags of each agent, so that Buildkite
# shows what a machine runs (internal/vmspec). The values come from the VM, after every install.
# Xcode has tags only when the image has Xcode: the Command Line Tools have no xcodebuild -version.
say "writing the agent tags of the image"
run IMAGE_NAME="bkaso-${IMAGE}" IMAGE_TAG="$TAG" TAGS_PATH="$AGENT_TAGS_PATH" <<'SCRIPT'
set -euo pipefail
xcode=$(/usr/bin/xcodebuild -version 2>/dev/null || true)
{
	printf 'image-name=%s\n' "$IMAGE_NAME"
	printf 'image-tag=%s\n' "$IMAGE_TAG"
	printf 'macos=%s\n' "$(sw_vers -productVersion)"
	printf 'macos-build=%s\n' "$(sw_vers -buildVersion)"
	if [[ -n "$xcode" ]]; then
		printf 'xcode=%s\n' "$(sed -n 's/^Xcode //p' <<<"$xcode")"
		printf 'xcode-build=%s\n' "$(sed -n 's/^Build version //p' <<<"$xcode")"
	fi
} >/tmp/agent-tags
sudo install -d -o root -g wheel -m 0755 "$(dirname "$TAGS_PATH")"
sudo install -o root -g wheel -m 0644 /tmp/agent-tags "$TAGS_PATH"
rm -f /tmp/agent-tags
SCRIPT

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
check=$(
	tart exec -i "$VM" /bin/bash -s <<'SCRIPT'
list() { ls "$@" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//'; }
printf 'user=%s\n' "$(whoami)"
printf 'agent=%s %s\n' "$(command -v buildkite-agent)" "$(buildkite-agent --version | sed -n 1p)"
printf 'git=%s\n' "$(/usr/bin/git --version 2>/dev/null)"
printf 'xcode=%s\n' "$(/usr/bin/xcodebuild -version 2>/dev/null | sed -n 1p)"
printf 'ios_runtime=%s\n' "$(xcrun simctl list runtimes 2>/dev/null | grep -c '^iOS ')"
printf 'ios_runtime_ids=%s\n' "$(xcrun simctl list runtimes 2>/dev/null | sed -n 's/^iOS .* - \(com\.apple\.[^ ]*\).*/\1/p' | tr '\n' ' ')"
printf 'free_gb=%s\n' "$(df -g / | awk 'NR == 2 {print $4}')"
if [[ -r /usr/local/etc/bk-agent-stack-orchard/agent-tags ]]; then
	while IFS= read -r line || [[ -n "$line" ]]; do
		printf 'tag=%s\n' "$line"
	done </usr/local/etc/bk-agent-stack-orchard/agent-tags
fi
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

# Without a destination, the hook leaves the variable empty, so the artifacts go to Buildkite.
want_hook=""
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
# Each line of the agent tags must pass the filter of the startup script (internal/vmspec), which
# drops a line silently: a key, an =, and a value of the characters that a tag list can carry.
tag_lines="$(sed -n 's/^tag=//p' <<<"$check")"
while IFS= read -r line; do
	[[ "$line" =~ ^[A-Za-z0-9._:/@+-]+=[A-Za-z0-9._:/@+=-]+$ ]] ||
		die "the startup script would drop the agent tag \"$line\" of the image"
done <<<"$tag_lines"
tags="$(tr '\n' ' ' <<<"$tag_lines" | sed 's/ $//')"
[[ "$tags" == "image-name=bkaso-${IMAGE} image-tag=${TAG} macos="*" macos-build="* ]] ||
	die "the agent tags of the image are \"$tags\", want its name, its tag, and the version of macOS first"
if [[ "$IMAGE" == pilot ]]; then
	grep -q "^xcode=Xcode ${XCODE_VERSION%.0}" <<<"$check" || die "the pilot image does not select Xcode ${XCODE_VERSION}"
	xcode="$(sed -n 's/^xcode=Xcode //p' <<<"$check")"
	[[ " $tags " == *" xcode=${xcode} xcode-build="* ]] ||
		die "the agent tags of the pilot image \"$tags\" do not name Xcode ${xcode}"
	grep -q '^ios_runtime=1$' <<<"$check" ||
		die "the pilot image has $(sed -n 's/^ios_runtime=//p' <<<"$check") iOS simulator runtimes, and it needs exactly one"
	grep -Eq '^ios_runtime_ids=com\.apple\.CoreSimulator\.SimRuntime\.iOS-[0-9]+-[0-9]+ $' <<<"$check" ||
		die "the iOS runtime of the pilot image is not the general one: $(sed -n 's/^ios_runtime_ids=//p' <<<"$check")"
	want "app=fastlane pod carthage xcbeautify swiftlint" "a job of the pilot does not find a tool to build or publish an app"
	free="$(sed -n 's/^free_gb=//p' <<<"$check")"
	((free >= PILOT_FREE_GB)) || die "the pilot image has $free GB free, and a job needs $PILOT_FREE_GB GB"
else
	[[ " $tags " != *" xcode="* ]] || die "the agent tags of the agent image \"$tags\" name an Xcode"
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
