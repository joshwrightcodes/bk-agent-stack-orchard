#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Loads the pf rules of the hosts on this Mac, for the Softnet test, or removes them.
#
# The rules come from the template of the role, so this Mac runs the rules that a host runs. The
# admin interface is the interface of the default route. It needs ansible and jq on the PATH (run
# it with mise x --), and sudo.
#
# Usage: deploy/hosts/test/pf.sh load [--pools] [CIDR...] | unload
#
#   load [CIDR...]   load the rules, with each CIDR as a softnetAllow of a profile, and enable pf
#   --pools          also permit the ranges from which Softnet picks the network of each VM, so
#                    that softnetAllow holds the gateway of every VM, which is the host
#   unload           remove the rules, and give back the reference on pf that load took
#
# load also keeps a copy of the rules in $TMPDIR/bkaso-pf.rules, which the Softnet test reads to
# find what the rules permit, because pfctl shows the loaded rules only to root.
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
RULES_FILE="${tmp%/}/bkaso-pf.rules"
ROLE="$HOSTS/roles/orchard_worker"

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
	# The pools are a variable of the role (vars/main.yml), so ansible adds them.
	allow='{{ bkaso_pf_allow }}'
	if [[ "${1:-}" == --pools ]]; then
		shift
		allow='{{ bkaso_pf_allow + orchard_worker_softnet_pools }}'
	fi
	admin="$(/sbin/route -n get default | awk '/interface:/ {print $2}')"
	[[ -n "$admin" ]] || die "this Mac has no default route, so it has no admin interface"
	# jq writes the CIDRs as JSON, so a value cannot change the shape of the variables.
	vars="$(jq -nc --arg admin "$admin" --arg allow "$allow" \
		'{orchard_worker_admin_interfaces: [$admin], bkaso_pf_allow: $ARGS.positional,
		  orchard_worker_softnet_allow: $allow}' \
		--args "$@")"
	# The template takes the pools from the variables of the role, and the guard from its filters.
	ANSIBLE_CONFIG="$HOSTS/ansible.cfg" ANSIBLE_FILTER_PLUGINS="$ROLE/filter_plugins" \
		ansible localhost -i localhost, -c local \
		-m ansible.builtin.template \
		-a "src=$ROLE/templates/vm-isolation.pf.j2 dest=$work/rules" \
		-e "@$ROLE/vars/main.yml" \
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
	cp "$work/rules" "$RULES_FILE"
	say "the rules of the hosts are loaded, with the admin interface $admin and softnetAllow:"
	sed -n 's/^table <bkaso_softnet_allow> persist//p' "$RULES_FILE"
	;;
unload)
	[[ $# -eq 1 ]] || usage
	pf -a "$ANCHOR" -F all
	give_back
	rm -f "$RULES_FILE"
	say "the rules of the hosts are removed, and this script holds no reference on pf"
	;;
*)
	usage
	;;
esac
