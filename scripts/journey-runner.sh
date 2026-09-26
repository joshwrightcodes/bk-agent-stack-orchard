#!/usr/bin/env bash
# The self-hosted runner of the skeleton journey: a GitHub Actions runner inside a Tart macOS VM on
# an Apple silicon Mac.
#
# The journey needs a macOS host on arm64, because Orchard places a macOS machine only on a worker
# with the runtime Tart, and a synthetic worker has that runtime only on macOS. A hosted macOS
# runner costs ten times the minutes of a Linux one while the repository is private, so this script
# gives the job a runner of its own. The VM keeps the jobs away from the files of the Mac, and it
# goes away with one command.
#
# Usage: scripts/journey-runner.sh create|start|stop|status|remove
#
# docs/development.md explains when to use each command, and what to do before the repository is
# public.
set -euo pipefail

REPO="joshwrightcodes/bk-agent-stack-orchard"
VM="bk-orchard-runner"
LABEL="bk-orchard-mac"

# The OS of the fleet, and an image that carries the Tart Guest Agent, git, and Homebrew.
IMAGE="ghcr.io/cirruslabs/macos-golden-gate-base:latest"

# The runner that create installs, and the SHA-256 that its release publishes. GitHub updates a
# registered runner itself after that, because it refuses a runner that falls too far behind.
RUNNER_VERSION="2.337.0"
RUNNER_SHA256="5a2cd92908a93d7276a194e1de6008099f3e7946f3f8e14aa7a1a7b4a31fdec2"

LOG="${HOME}/.tart/${VM}.log"

say() { printf 'journey-runner: %s\n' "$*"; }
die() { printf 'journey-runner: %s\n' "$*" >&2; exit 1; }

require() {
	[[ "$(uname -s)" == "Darwin" && "$(uname -m)" == "arm64" ]] ||
		die "this needs an Apple silicon Mac, because Tart runs macOS VMs only there"
	command -v tart >/dev/null || die "tart is not installed; docs/development.md says how"
	command -v gh >/dev/null || die "gh is not installed"
	gh auth status >/dev/null 2>&1 || die "gh is not signed in; run: gh auth login"
}

# tart list and tart get fail while a VM runs, because they ask hdiutil for the size of a disk image
# that the running VM holds (Tart 2.38.0). These checks therefore read no output of Tart.

# exists reports a VM that Tart holds, from the directory that Tart keeps for it.
exists() { [[ -f "${TART_HOME:-${HOME}/.tart}/vms/${VM}/config.json" ]]; }

# running reports a VM that a tart run process holds, whatever flags started it.
running() { pgrep -f "tart run( .*)? ${VM}\$" >/dev/null; }

# boot starts the VM without a window, and waits until the guest agent answers.
boot() {
	if running; then
		say "the VM $VM already runs"
	else
		say "starting the VM $VM (log: $LOG)"
		nohup tart run --no-graphics "$VM" >>"$LOG" 2>&1 &
	fi

	for _ in $(seq 1 90); do
		if tart exec "$VM" true >/dev/null 2>&1; then
			return 0
		fi
		sleep 2
	done

	die "the VM $VM did not answer inside three minutes; see $LOG"
}

# runner_id gives the identifier that GitHub holds for the runner of this VM, or nothing.
runner_id() {
	gh api "repos/${REPO}/actions/runners" --paginate \
		--jq ".runners[] | select(.name == \"${VM}\") | .id" 2>/dev/null | head -1
}

create() {
	require

	if exists; then
		say "the VM $VM exists; to start again from nothing, run remove first"
	else
		say "cloning $IMAGE, which is about 30 GiB the first time"
		tart clone "$IMAGE" "$VM"
	fi

	boot

	say "installing the runner $RUNNER_VERSION, and checking its SHA-256"
	tart exec "$VM" /bin/sh -c "
		set -e
		mkdir -p ~/actions-runner && cd ~/actions-runner
		curl -fsSL -o runner.tar.gz \
			https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-osx-arm64-${RUNNER_VERSION}.tar.gz
		echo '${RUNNER_SHA256}  runner.tar.gz' | shasum -a 256 -c -
		tar -xzf runner.tar.gz && rm runner.tar.gz
	"

	say "registering the runner with the label $LABEL"
	# The registration token lasts an hour. It travels on stdin, so it is not in the arguments of a
	# process that another user of the Mac can list.
	gh api -X POST "repos/${REPO}/actions/runners/registration-token" --jq .token |
		tart exec -i "$VM" /bin/sh -c "
			set -e
			read -r token
			cd ~/actions-runner
			./config.sh --url https://github.com/${REPO} --token \"\$token\" \
				--name ${VM} --labels ${LABEL} --unattended --replace
			./svc.sh install
			./svc.sh start
		"

	say "pointing the skeleton journey at the runner"
	gh variable set JOURNEY_RUNNER --repo "$REPO" --body "$LABEL"

	status
}

start() {
	require
	exists || die "the VM $VM does not exist; run create first"
	boot
	status
}

stop() {
	require

	if running; then
		tart stop "$VM"
		say "stopped the VM $VM; the skeleton journey waits in the queue until the next start"
	else
		say "the VM $VM does not run"
	fi
}

status() {
	require

	if exists; then
		if running; then say "the VM $VM runs"; else say "the VM $VM is stopped"; fi
	else
		say "no VM $VM"
	fi

	local id
	id="$(runner_id)"

	if [[ -n "$id" ]]; then
		gh api "repos/${REPO}/actions/runners/${id}" \
			--jq '"runner: \(.name) \(.status), busy \(.busy), labels \([.labels[].name] | join(","))"'
	else
		say "GitHub holds no runner called $VM"
	fi

	say "JOURNEY_RUNNER=$(gh variable get JOURNEY_RUNNER --repo "$REPO" 2>/dev/null || echo "(unset: the job uses a hosted macOS runner)")"
}

remove() {
	require

	local id
	id="$(runner_id)"

	if [[ -n "$id" ]]; then
		gh api -X DELETE "repos/${REPO}/actions/runners/${id}"
		say "removed the runner $VM from GitHub"
	fi

	if gh variable get JOURNEY_RUNNER --repo "$REPO" >/dev/null 2>&1; then
		gh variable delete JOURNEY_RUNNER --repo "$REPO"
		say "deleted JOURNEY_RUNNER; the skeleton journey now uses a hosted macOS runner"
	fi

	if exists; then
		running && tart stop "$VM"
		tart delete "$VM"
		say "deleted the VM $VM"
	fi
}

case "${1:-}" in
create | start | stop | status | remove) "$1" ;;
*) die "usage: $0 create|start|stop|status|remove" ;;
esac
