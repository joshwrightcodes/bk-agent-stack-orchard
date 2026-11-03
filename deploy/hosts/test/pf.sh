#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Loads the pf rules of the hosts on this Mac, for the Softnet test, or removes them.
#
# The rules come from the template of the role, so this Mac runs the rules that a host runs. The
# admin interface is the interface of the default route. It needs ansible and jq on the PATH (run
# it with mise x --), and sudo.
#
# Usage: deploy/hosts/test/pf.sh load [CIDR...] | unload
#
#   load [CIDR...]   load the rules, with each CIDR as a softnetAllow of a profile, and enable pf
#   unload           remove the rules, and give back the reference on pf that load took
#
# Exit status: 0 success, 1 failure, 2 usage error.
set -euo pipefail

readonly PROG="pf"
HOSTS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
ANCHOR=com.apple/400.bkaso-vm-isolation
# pfctl -E takes a reference on pf and prints a token for it. pf stays enabled until each
# reference is given back, so load keeps the token here and unload gives it back. macOS empties
# TMPDIR at a restart, which also drops every reference.
tmp="${TMPDIR:-/tmp}"
TOKEN_FILE="${tmp%/}/bkaso-pf.token"

say() { printf '%s: %s\n' "$PROG" "$*"; }
die() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	exit 1
}
usage() {
	sed -n 's/^# Usage: /usage: /p' "${BASH_SOURCE[0]}" >&2
	exit 2
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# pf runs pfctl as root, with its standard error in $work/pf.err. pfctl writes notes such as "No
# ALTQ support in kernel" there when it succeeds, so they show only when it fails.
pf() {
	if ! sudo /sbin/pfctl "$@" 2>"$work/pf.err"; then
		grep -v ALTQ "$work/pf.err" >&2 || true
		die "pfctl $* failed"
	fi
}

# give_back gives back the reference on pf that an earlier load took, if there is one.
give_back() {
	local token
	[[ -f "$TOKEN_FILE" ]] || return 0
	token="$(cat "$TOKEN_FILE")"
	rm -f "$TOKEN_FILE"
	[[ -n "$token" ]] || return 0
	sudo /sbin/pfctl -X "$token" 2>"$work/pf.err" ||
		say "pf no longer holds the reference $token, so there was nothing to give back"
}

case "${1:-}" in
load)
	shift
	command -v ansible >/dev/null || die "ansible is not on the PATH: run mise x -- $0 load"
	command -v jq >/dev/null || die "jq is not on the PATH: run mise x -- $0 load"
	admin="$(/sbin/route -n get default | awk '/interface:/ {print $2}')"
	[[ -n "$admin" ]] || die "this Mac has no default route, so it has no admin interface"
	# jq writes the CIDRs as JSON, so a value cannot change the shape of the variables.
	vars="$(jq -nc --arg admin "$admin" \
		'{orchard_worker_admin_interfaces: [$admin], orchard_worker_softnet_allow: $ARGS.positional}' \
		--args "$@")"
	ANSIBLE_CONFIG="$HOSTS/ansible.cfg" ansible localhost -i localhost, -c local \
		-m ansible.builtin.template \
		-a "src=$HOSTS/roles/orchard_worker/templates/vm-isolation.pf.j2 dest=$work/rules" \
		-e "$vars" \
		-e ansible_python_interpreter=/usr/bin/python3 \
		>/dev/null ||
		die "ansible could not render the pf rules"
	# The role checks the rules in the same way before it writes them to a host.
	pf -a "$ANCHOR" -n -f "$work/rules"
	# A second load takes one reference, not two.
	give_back
	pf -E
	sed -n 's/^Token : //p' "$work/pf.err" >"$TOKEN_FILE"
	pf -a "$ANCHOR" -f "$work/rules"
	say "the rules of the hosts are loaded, with the admin interface $admin and softnetAllow $*"
	;;
unload)
	[[ $# -eq 1 ]] || usage
	pf -a "$ANCHOR" -F all
	give_back
	say "the rules of the hosts are removed, and this script holds no reference on pf"
	;;
*)
	usage
	;;
esac
