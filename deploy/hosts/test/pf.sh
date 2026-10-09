#!/usr/bin/env bash
# Loads the pf rules of the hosts on this Mac, for the Softnet test, or removes them.
#
#   deploy/hosts/test/pf.sh load [CIDR...]   the rules, with each CIDR as a softnetAllow of a profile
#   deploy/hosts/test/pf.sh unload           no rules
#
# The rules come from the template of the role, so this Mac runs the rules that a host runs. The
# admin interface is the interface of the default route. It needs ansible on PATH, and sudo.
set -euo pipefail

HOSTS=$(cd "$(dirname "$0")/.." && pwd)
ANCHOR=com.apple/400.bkaso-vm-isolation

case "${1:-}" in
load)
  shift
  rules=$(mktemp)
  trap 'rm -f "$rules"' EXIT
  admin=$(/sbin/route -n get default | awk '/interface:/ {print $2}')
  allow=$(python3 -c 'import json, sys; print(json.dumps(sys.argv[1:]))' "$@")
  ANSIBLE_CONFIG="$HOSTS/ansible.cfg" ansible localhost -i localhost, -c local \
    -m ansible.builtin.template \
    -a "src=$HOSTS/roles/orchard_worker/templates/vm-isolation.pf.j2 dest=$rules" \
    -e "{\"orchard_worker_admin_interfaces\": [\"$admin\"], \"orchard_worker_softnet_allow\": $allow}" \
    -e ansible_python_interpreter=/usr/bin/python3 \
    >/dev/null
  sudo /sbin/pfctl -E 2>/dev/null || true
  sudo /sbin/pfctl -a "$ANCHOR" -f "$rules" 2>/dev/null
  echo "pf: the rules of the hosts are loaded, with the admin interface $admin and softnetAllow $*"
  ;;
unload)
  sudo /sbin/pfctl -a "$ANCHOR" -F all 2>/dev/null
  echo "pf: the rules of the hosts are removed"
  ;;
*)
  echo "usage: $0 load [CIDR...] | unload" >&2
  exit 2
  ;;
esac
