#!/usr/bin/env bash
# Runs the playbook against a Tart VM, with an Orchard controller on this Mac, and checks the host
# as the fleet uses it:
#
#   - the worker registers with the labels of the inventory, through TLS with the CA in the token;
#   - the token is in no process listing, softnet has the setuid bit, the DHCP lease is short, and
#     pf keeps the VMs away from the host, also after a restart;
#   - a second run of the playbook changes nothing;
#   - the worker registers again after a restart of the host.
#
# A VM cannot run a macOS VM, so the test turns off the check of the hypervisor, and it cannot
# check that a VM of the pilot image starts. A real host checks that.
#
# It needs an Apple silicon Mac with tart, orchard, ansible-playbook, and about 50 GB of disk.
set -euo pipefail

HOSTS=$(cd "$(dirname "$0")/.." && pwd)
VM="${VM_NAME:-bkaso-host-test}"
BASE="ghcr.io/cirruslabs/macos-golden-gate-base@sha256:9a2f20d179d6418a128dcb76c593588ea85a263dbfa286e757db63f3d949af7c"
PORT=6120
WORK=$(mktemp -d)
ADMIN_TOKEN=$(openssl rand -hex 24)
WORKER_TOKEN=$(openssl rand -hex 24)
controller=""

log() { echo "hosts-test: $*"; }
fail() { echo "hosts-test: FAIL: $*" >&2; exit 1; }

clean_up() {
  [[ -n "$controller" ]] && kill "$controller" 2>/dev/null
  if [[ -z "${KEEP_VM:-}" && -d "$HOME/.tart/vms/$VM" ]]; then
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

# api GET <path>: the answer of the controller, as the bootstrap admin.
api() {
  curl -sS --fail --cacert "$WORK/ca.crt" -u "bootstrap-admin:$ADMIN_TOKEN" -X "$1" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "https://127.0.0.1:$PORT$2"
}

# worker_seen_after <epoch>: the worker of the VM reported to the controller after that moment.
worker_seen_after() {
  api GET /v1/workers | python3 -c '
import datetime, json, sys
since = float(sys.argv[1])
for worker in json.load(sys.stdin):
    if worker["name"] != sys.argv[2]:
        continue
    seen = datetime.datetime.fromisoformat(worker["last_seen"].replace("Z", "+00:00")).timestamp()
    if (worker.get("labels") or {}).get("pool") != "test":
        sys.exit("the worker has the labels %s, without pool=test" % worker.get("labels"))
    sys.exit(0 if seen > since else 1)
sys.exit(1)
' "$1" "$VM"
}

wait_for_worker() {
  local since=$1
  for _ in $(seq 1 120); do
    worker_seen_after "$since" && return 0
    sleep 2
  done
  fail "the worker did not register with the controller"
}

log "starting $VM from the base image"
if [[ -d "$HOME/.tart/vms/$VM" ]]; then
  tart stop "$VM" >/dev/null 2>&1 || true
  tart delete "$VM"
fi
tart clone "$BASE" "$VM"
tart run --no-graphics "$VM" >"$WORK/vm.log" 2>&1 &
ip=$(tart ip --wait 120 "$VM")
for _ in $(seq 1 60); do bounded 8 tart exec "$VM" true >/dev/null 2>&1 && break; sleep 2; done
# This Mac is the gateway of the network of the VMs.
gateway="${ip%.*}.1"

log "making a CA and a certificate of the controller for $gateway"
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

log "starting the Orchard controller on port $PORT"
ORCHARD_BOOTSTRAP_ADMIN_TOKEN="$ADMIN_TOKEN" orchard controller run --data-dir "$WORK/data" \
  --listen ":$PORT" --controller-cert "$WORK/tls.crt" --controller-key "$WORK/tls.key" \
  >"$WORK/controller.log" 2>&1 &
controller=$!
for _ in $(seq 1 30); do api GET /v1/workers >/dev/null 2>&1 && break; sleep 1; done
api POST /v1/service-accounts \
  "{\"name\":\"worker\",\"token\":\"$WORKER_TOKEN\",\"roles\":[\"compute:read\",\"compute:write\"]}" >/dev/null

# The pf rules of the hosts, loaded on this Mac for the Softnet test, block the VM from this Mac,
# and so from the controller of this test.
bounded 10 tart exec "$VM" /usr/bin/nc -z -G 5 "$gateway" "$PORT" >/dev/null 2>&1 ||
  fail "the VM cannot reach the controller at $gateway:$PORT; if the pf rules of the hosts are
loaded on this Mac, remove them first: sudo pfctl -a com.apple/400.bkaso-vm-isolation -F all"

log "giving Ansible a key for admin"
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

log "running the playbook"
started=$(date +%s)
playbook
wait_for_worker "$started"
log "the worker registered with the labels pool=test"

log "checking the host"
check=$(guest <<'EOF'
echo "token_in_ps=$(ps axww -o command | grep -c '[o]rchard-bootstrap-token' || true)"
echo "token_mode=$(sudo stat -f '%Su %Mp%Lp' /usr/local/etc/orchard-worker/bootstrap-token)"
echo "softnet=$(stat -f '%Su %Mp%Lp' /usr/local/bin/softnet)"
echo "pf_block=$(sudo /sbin/pfctl -a com.apple/400.bkaso-vm-isolation -s rules 2>/dev/null | grep -c 'block drop in quick')"
echo "pf_enabled=$(sudo /sbin/pfctl -s info 2>/dev/null | grep -c 'Status: Enabled')"
echo "dhcp_lease=$(/usr/libexec/PlistBuddy -c 'Print :bootpd:DHCPLeaseTimeSecs' /Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist 2>/dev/null)"
echo "worker_user=$(ps axo user,command | awk '$2 ~ /orchard$/ && $3 == "worker" {print $1}' | sort -u | tr '\n' ' ')"
EOF
)
echo "$check"
grep -qx "token_in_ps=0" <<<"$check" || fail "the token is in a process listing"
grep -qx "token_mode=root 0600" <<<"$check" || fail "the token file is not root 0600"
grep -qx "softnet=root 4755" <<<"$check" || fail "softnet has no setuid bit"
grep -qx "dhcp_lease=600" <<<"$check" || fail "the DHCP lease of the VM network is not 600 s"
grep -Eqx "pf_block=[1-9][0-9]*" <<<"$check" || fail "pf has no rule that keeps the VMs away from the host"
grep -qx "pf_enabled=1" <<<"$check" || fail "pf is not enabled"
grep -q "worker_user=.*admin" <<<"$check" || fail "no worker process runs as admin"

log "running the playbook again, which must change nothing"
playbook
grep -Eq "^$VM +: ok=[0-9]+ +changed=0 +unreachable=0 +failed=0" "$WORK/playbook.log" ||
  fail "the second run changed the host"

log "restarting the host"
tart exec "$VM" /bin/sh -c 'sudo shutdown -r now' >/dev/null 2>&1 || true
# The worker can report once more while macOS stops, so the check counts only reports after the
# guest agent stops answering.
for _ in $(seq 1 60); do
  bounded 5 tart exec "$VM" true >/dev/null 2>&1 || break
  sleep 1
done
restarted=$(date +%s)
wait_for_worker "$restarted"
log "the worker registered again after the restart"

# launchd loads the pf rules at each start, and the pfctl job of macOS loads /etc/pf.conf, so the
# rules must survive a restart whatever the order of the two jobs.
after=$(guest <<'EOF'
echo "pf_block=$(sudo /sbin/pfctl -a com.apple/400.bkaso-vm-isolation -s rules 2>/dev/null | grep -c 'block drop in quick')"
echo "pf_enabled=$(sudo /sbin/pfctl -s info 2>/dev/null | grep -c 'Status: Enabled')"
EOF
)
echo "$after"
grep -Eqx "pf_block=[1-9][0-9]*" <<<"$after" || fail "the pf rules did not survive the restart"
grep -qx "pf_enabled=1" <<<"$after" || fail "pf is not enabled after the restart"

log "PASS: the worker registers, the token stays secret, the playbook is idempotent, and the worker and the pf rules survive a restart"
