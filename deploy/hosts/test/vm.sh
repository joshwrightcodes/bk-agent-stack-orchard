#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Runs the playbook against a Tart VM, with an Orchard controller on this Mac, and checks the host
# as the fleet uses it:
#
#   - the worker registers with the labels of the inventory, through TLS with the CA in the token;
#   - the token is in no process listing, softnet has the setuid bit, the host does not sleep, and
#     pf keeps the VMs away from the host, also after a restart;
#   - a second run of the playbook changes nothing;
#   - the worker registers again after a restart of the host.
#
# A VM cannot run a macOS VM, so the test turns off the check of the hypervisor, and it cannot
# check that a VM of the pilot image starts. A real host checks that.
#
# It needs an Apple silicon Mac with tart, orchard, ansible-playbook, jq, and about 50 GB of disk.
#
# Usage: deploy/hosts/test/vm.sh        (mise run hosts-test)
#
# Environment:
#   VM_NAME     the Tart VM of the test (default: bkaso-host-test)
#   KEEP        1 keeps the VM after the run, for a person who wants to look at it
#   TART_HOME   the directory of Tart (default: ~/.tart)
#
# Exit status: 0 success, 1 failure, 2 usage error.
set -euo pipefail

readonly PROG="hosts-test"
HOSTS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
VM="${VM_NAME:-bkaso-host-test}"
TART_DIR="${TART_HOME:-${HOME}/.tart}"
BASE="ghcr.io/cirruslabs/macos-golden-gate-base@sha256:14f4fc32a778763ccae1a4d543ea0f177720c570dd7f859eda10b9e0020c7d05"
PORT=6120

say() { printf '%s: %s\n' "$PROG" "$*"; }
die() {
  printf '%s: FAIL: %s\n' "$PROG" "$*" >&2
  exit 1
}
usage() {
  sed -n 's/^# Usage: /usage: /p' "${BASH_SOURCE[0]}" >&2
  exit 2
}

[[ $# -eq 0 ]] || usage
for tool in tart orchard ansible-playbook jq openssl; do
  command -v "$tool" >/dev/null || die "$tool is not on the PATH: run mise run hosts-test"
done

WORK="$(mktemp -d)"
ADMIN_TOKEN="$(openssl rand -hex 24)"
WORKER_TOKEN="$(openssl rand -hex 24)"
controller=""

clean_up() {
  [[ -n "$controller" ]] && kill "$controller" 2>/dev/null
  if [[ "${KEEP:-}" != "1" && -d "$TART_DIR/vms/$VM" ]]; then
    tart stop "$VM" >/dev/null 2>&1 || true
    tart delete "$VM" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap clean_up EXIT

guest() { tart exec -i "$VM" /bin/bash -s; }

# bounded <seconds> <command...>: the command, stopped after that time. A tart exec can wait
# without end while the guest starts or stops.
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

# api <method> <path> [<body>]: the answer of the controller, as the bootstrap admin. The token
# goes to curl on its standard input, and the body through a file, so neither is in a process
# listing.
api() {
  local data=()
  if [[ $# -ge 3 ]]; then
    printf '%s' "$3" >"$WORK/body.json"
    data=(--data "@$WORK/body.json")
  fi
  printf 'user = "bootstrap-admin:%s"\n' "$ADMIN_TOKEN" |
    curl -fsS -K - --cacert "$WORK/ca.crt" -X "$1" -H 'Content-Type: application/json' \
      ${data[@]+"${data[@]}"} "https://127.0.0.1:$PORT$2"
}

# The state of the worker of the VM in the list of workers of the controller: "seen" when it
# reported after $since, "old <time>" when it reported before, "absent", "labels <labels>" when it
# does not have pool=test, or "time <value>" when last_seen is not an RFC 3339 time. Go writes up
# to nine digits of a second and an offset, which fromdateiso8601 does not read, so the filter
# reads the time itself.
# shellcheck disable=SC2016 # the $ words are jq variables
WORKER_STATE='
def epoch:
  first(capture("^(?<t>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\\.[0-9]+)?(?<z>Z|(?<s>[+-])(?<h>[0-9]{2}):(?<m>[0-9]{2}))$"))
  // null
  | if . == null then null
    else ((.t + "Z") | fromdateiso8601)
      - (if .z == "Z" then 0
         else ((.h | tonumber) * 3600 + (.m | tonumber) * 60) * (if .s == "+" then 1 else -1 end) end)
    end;
first((. // [])[] | select(.name == $name)) // null
| if . == null then "absent"
  elif (.labels.pool // "") != "test" then "labels \(.labels // {} | tojson)"
  elif (.last_seen | epoch) == null then "time \(.last_seen)"
  elif (.last_seen | epoch) > $since then "seen"
  else "old \(.last_seen)" end'

# wait_for_worker <epoch>: waits up to four minutes for the worker of the VM to report to the
# controller after that moment. A worker with the wrong labels fails at once.
wait_for_worker() {
  local since=$1 workers state="the controller did not answer"
  for _ in $(seq 1 120); do
    if workers="$(api GET /v1/workers 2>&1)"; then
      state="$(jq -r --arg name "$VM" --argjson since "$since" "$WORKER_STATE" <<<"$workers" 2>&1)" ||
        state="jq cannot read the list of workers of the controller: $state"
    else
      state="the controller did not answer: $workers"
    fi
    case "$state" in
    seen) return 0 ;;
    labels*) die "the worker registered with the labels ${state#labels }, without pool=test" ;;
    time*) die "the controller gives the worker the last_seen ${state#time }, which is not an RFC 3339 time" ;;
    esac
    sleep 2
  done
  case "$state" in
  absent) die "the worker $VM did not register with the controller inside four minutes" ;;
  old*) die "the worker $VM last reported at ${state#old }, before this step began, and not again inside four minutes" ;;
  *) die "the worker $VM did not report inside four minutes: $state" ;;
  esac
}

say "starting $VM from the base image"
if [[ -d "$TART_DIR/vms/$VM" ]]; then
  tart stop "$VM" >/dev/null 2>&1 || true
  tart delete "$VM"
fi
tart clone "$BASE" "$VM"
tart run --no-graphics "$VM" >"$WORK/vm.log" 2>&1 &
ip=$(tart ip --wait 120 "$VM")
for _ in $(seq 1 60); do bounded 8 tart exec "$VM" true >/dev/null 2>&1 && break; sleep 2; done
# This Mac is the gateway of the network of the VMs.
gateway="${ip%.*}.1"

say "making a CA and a certificate of the controller for $gateway"
cat >"$WORK/leaf.ext" <<EOF
subjectAltName = IP:$gateway, IP:127.0.0.1, DNS:localhost
extendedKeyUsage = serverAuth
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=hosts-test CA" \
  -keyout "$WORK/ca.key" -out "$WORK/ca.crt" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=orchard" \
  -keyout "$WORK/tls.key" -out "$WORK/tls.csr" 2>/dev/null
openssl x509 -req -in "$WORK/tls.csr" -CA "$WORK/ca.crt" -CAkey "$WORK/ca.key" -CAcreateserial \
  -days 1 -extfile "$WORK/leaf.ext" -out "$WORK/tls.crt" 2>/dev/null

say "starting the Orchard controller on port $PORT"
ORCHARD_BOOTSTRAP_ADMIN_TOKEN="$ADMIN_TOKEN" orchard controller run --data-dir "$WORK/data" \
  --listen ":$PORT" --controller-cert "$WORK/tls.crt" --controller-key "$WORK/tls.key" \
  >"$WORK/controller.log" 2>&1 &
controller=$!
for _ in $(seq 1 30); do api GET /v1/workers >/dev/null 2>&1 && break; sleep 1; done
api POST /v1/service-accounts "$(jq -nc --arg name "worker-$VM" --arg token "$WORKER_TOKEN" \
  '{name: $name, token: $token, roles: ["compute:read", "compute:write"]}')" >/dev/null

# The pf rules of the hosts, loaded on this Mac for the Softnet test, block the VM from this Mac,
# and so from the controller of this test.
bounded 10 tart exec "$VM" /usr/bin/nc -z -G 5 "$gateway" "$PORT" >/dev/null 2>&1 ||
  die "the VM cannot reach the controller at $gateway:$PORT; if the pf rules of the hosts are
loaded on this Mac, remove them first: deploy/hosts/test/pf.sh unload"

say "giving Ansible a key for admin"
ssh-keygen -q -t ed25519 -N "" -f "$WORK/id" -C hosts-test
guest <<EOF
mkdir -p ~/.ssh && chmod 700 ~/.ssh
echo '$(cat "$WORK/id.pub")' >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
EOF

cat >"$WORK/inventory.yml" <<EOF
workers:
  hosts:
    $VM:
      ansible_host: $ip
  vars:
    ansible_user: admin
    ansible_ssh_private_key_file: $WORK/id
    ansible_ssh_common_args: -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
    orchard_worker_controller_url: https://$gateway:$PORT
    orchard_worker_ca_cert: "{{ lookup('ansible.builtin.file', '$WORK/ca.crt') }}"
    orchard_worker_token: $WORKER_TOKEN
    orchard_worker_name: $VM
    orchard_worker_labels: {pool: test}
    orchard_worker_otlp_endpoint: http://127.0.0.1:4318
    orchard_worker_config_file: $HOSTS/test/testdata/config.yaml
    orchard_worker_require_hypervisor: false
EOF
playbook() {
  ANSIBLE_CONFIG="$HOSTS/ansible.cfg" ansible-playbook -i "$WORK/inventory.yml" "$HOSTS/site.yml" \
    | tee "$WORK/playbook.log"
}

say "running the playbook"
started=$(date +%s)
playbook
wait_for_worker "$started"
say "the worker registered with the labels pool=test"

say "checking the host"
check=$(guest <<'EOF'
echo "token_in_ps=$(ps axww -o command | grep -c '[o]rchard-bootstrap-token' || true)"
echo "token_mode=$(sudo stat -f '%Su %Mp%Lp' /usr/local/etc/orchard-worker/bootstrap-token)"
echo "softnet=$(stat -f '%Su %Mp%Lp' /usr/local/bin/softnet)"
echo "pf_block=$(sudo /sbin/pfctl -a com.apple/400.bkaso-vm-isolation -s rules 2>/dev/null | grep -c 'block drop in quick')"
echo "pf_enabled=$(sudo /sbin/pfctl -s info 2>/dev/null | grep -c 'Status: Enabled')"
echo "worker_user=$(ps axo user,command | awk '$2 ~ /orchard$/ && $3 == "worker" {print $1}' | sort -u | tr '\n' ' ')"
echo "sleep=$(/usr/bin/pmset -g | awk '$1 == "sleep" || $1 == "disksleep" {print $1 "=" $2}' | sort | tr '\n' ' ')"
EOF
)
echo "$check"
grep -qx "token_in_ps=0" <<<"$check" || die "the token is in a process listing"
grep -qx "token_mode=root 0600" <<<"$check" || die "the token file is not root 0600"
grep -qx "softnet=root 4755" <<<"$check" || die "softnet has no setuid bit"
grep -Eqx "pf_block=[1-9][0-9]*" <<<"$check" || die "pf has no rule that keeps the VMs away from the host"
grep -qx "pf_enabled=1" <<<"$check" || die "pf is not enabled"
grep -qx "sleep=disksleep=0 sleep=0 " <<<"$check" || die "the host can sleep"
grep -q "worker_user=.*admin" <<<"$check" || die "no worker process runs as admin"

say "running the playbook again, which must change nothing"
playbook
grep -Eq "^$VM +: ok=[0-9]+ +changed=0 +unreachable=0 +failed=0" "$WORK/playbook.log" ||
  die "the second run changed the host"

say "restarting the host"
booted=$(tart exec "$VM" /usr/sbin/sysctl -n kern.boottime)
# shutdown is in /sbin, which is not on the PATH of the guest agent. The call to the guest can stay
# open after the guest has gone, so it has a limit.
bounded 10 tart exec "$VM" /bin/sh -c 'sudo /sbin/shutdown -r now' >/dev/null 2>&1 || true
# The host restarted when its boot time changed. The guest agent can answer again before a poll
# sees it stop, so the boot time, not a pause of the guest agent, proves the restart.
restarted=""
for _ in $(seq 1 150); do
  now=$(bounded 8 tart exec "$VM" /usr/sbin/sysctl -n kern.boottime 2>/dev/null || true)
  if [[ -n "$now" && "$now" != "$booted" ]]; then
    restarted=$(date +%s)
    break
  fi
  sleep 2
done
[[ -n "$restarted" ]] || die "the host did not restart: its boot time did not change in 5 minutes"
# The worker reports after its own start, which follows the boot of the host.
wait_for_worker "$restarted"
say "the host restarted, and the worker registered again"

# launchd loads the pf rules at each start, and the pfctl job of macOS loads /etc/pf.conf, so the
# rules must survive a restart whatever the order of the two jobs.
after=$(guest <<'EOF'
echo "pf_block=$(sudo /sbin/pfctl -a com.apple/400.bkaso-vm-isolation -s rules 2>/dev/null | grep -c 'block drop in quick')"
echo "pf_enabled=$(sudo /sbin/pfctl -s info 2>/dev/null | grep -c 'Status: Enabled')"
EOF
)
echo "$after"
grep -Eqx "pf_block=[1-9][0-9]*" <<<"$after" || die "the pf rules did not survive the restart"
grep -qx "pf_enabled=1" <<<"$after" || die "pf is not enabled after the restart"

say "PASS: the worker registers, the token stays secret, the playbook is idempotent, and the worker and the pf rules survive a restart"
