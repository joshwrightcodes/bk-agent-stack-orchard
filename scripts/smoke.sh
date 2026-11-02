#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# The smoke deployment of W-21: the controller from its Helm chart and Orchard from its kustomize
# base, on a kind cluster, with the fake Stacks API and an OpenTelemetry collector. It passes when
# the controller becomes ready through its gRPC readiness probe, and the collector receives its
# metrics and its spans, and neither the cluster agent token nor the Orchard token appears in the
# controller log, in the collector output, or in the machine list of Orchard (invariant I-3, and the
# rule of the test strategy that a test that writes a token to a log or to telemetry must fail).
#
# Usage: scripts/smoke.sh
#
# KEEP=1 keeps the cluster after the run, for a person who wants to look at it. The tools come
# from mise: kind, ko, kubectl, kustomize, helm, and docker.
set -euo pipefail

CLUSTER="bkaso-smoke"
ROOT="$(git rev-parse --show-toplevel)"
WORK="$(mktemp -d)"
forward=""

say() { printf 'smoke: %s\n' "$*"; }

cleanup() {
	status=$?

	if [[ $status -ne 0 ]]; then
		say "the smoke test failed; the state of the cluster follows"
		for ns in smoke orchard; do
			kubectl -n "$ns" get pods,jobs -o wide || true
			kubectl -n "$ns" get events --sort-by=.lastTimestamp | tail -20 || true
			for pod in $(kubectl -n "$ns" get pods -o name 2>/dev/null); do
				echo "--- $ns $pod"
				kubectl -n "$ns" logs "$pod" --all-containers --tail=40 || true
			done
		done
	fi

	# The status is already kept, so a failed kill cannot change the exit code.
	[[ -n "$forward" ]] && kill "$forward" 2>/dev/null || true

	rm -rf "$WORK"

	if [[ "${KEEP:-}" != "1" ]]; then
		kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
	fi

	exit "$status"
}
trap cleanup EXIT

cd "$ROOT"

say "creating the kind cluster $CLUSTER"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --wait 120s

say "building the images and loading them into the cluster"
KO_DOCKER_REPO=ko.local/bk-agent-stack-orchard ko build --local --bare --tags smoke \
	./cmd/bk-agent-stack-orchard
KO_DOCKER_REPO=ko.local/fakestacks ko build --local --bare --tags smoke ./test/smoke/fakestacks
docker buildx build --load -t orchard:smoke deploy/orchard/image
for image in ko.local/bk-agent-stack-orchard:smoke ko.local/fakestacks:smoke orchard:smoke; do
	kind load docker-image "$image" --name "$CLUSTER"
done

kubectl create namespace smoke
kubectl create namespace orchard

say "making an authority and a certificate for Orchard"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=smoke authority" \
	-keyout "$WORK/ca.key" -out "$WORK/ca.crt" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=orchard" \
	-keyout "$WORK/tls.key" -out "$WORK/tls.csr" 2>/dev/null
printf 'subjectAltName=DNS:orchard,DNS:orchard.orchard,DNS:orchard.orchard.svc,DNS:orchard.orchard.svc.cluster.local\n' \
	>"$WORK/san.ext"
openssl x509 -req -in "$WORK/tls.csr" -CA "$WORK/ca.crt" -CAkey "$WORK/ca.key" -CAcreateserial \
	-days 1 -extfile "$WORK/san.ext" -out "$WORK/tls.crt" 2>/dev/null

kubectl -n orchard create secret generic orchard-tls \
	--from-file=tls.crt="$WORK/tls.crt" --from-file=tls.key="$WORK/tls.key" \
	--from-file=ca.crt="$WORK/ca.crt"

controller_token="$(openssl rand -hex 24)"
kubectl -n orchard create secret generic orchard-bootstrap \
	--from-literal=bootstrap-admin-token="$(openssl rand -hex 24)" \
	--from-literal=controller-token="$controller_token" \
	--from-literal=worker-token="$(openssl rand -hex 24)"

agent_token="bkct_smoke_$(openssl rand -hex 16)"
kubectl -n smoke create secret generic bk-agent-stack-orchard \
	--from-literal=BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN="$agent_token" \
	--from-literal=BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN="$controller_token" \
	--from-file=BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT="$WORK/ca.crt"

say "starting the collector and the fake Stacks API"
kubectl apply -f test/smoke/collector.yaml -f test/smoke/fakestacks.yaml
kubectl -n smoke rollout status deployment/collector --timeout=120s
kubectl -n smoke rollout status deployment/fakestacks --timeout=120s

say "starting Orchard from deploy/orchard"
kustomize build test/smoke/orchard | kubectl apply -f -
kubectl -n orchard rollout status statefulset/orchard --timeout=180s
kubectl -n orchard wait job/orchard-bootstrap --for=condition=complete --timeout=180s

say "installing the controller from its chart"
helm install bkaso deploy/helm/bk-agent-stack-orchard --namespace smoke \
	-f test/smoke/values.yaml --wait --timeout 180s

say "waiting for the gRPC readiness probe of the controller"
kubectl -n smoke wait pod -l app.kubernetes.io/name=bk-agent-stack-orchard \
	--for=condition=Ready --timeout=120s

say "waiting for the telemetry of the controller in the collector"
# The metrics leave each minute, so the test waits up to three minutes.
deadline=$((SECONDS + 180))
wanted=("bk-agent-stack-orchard" "bk_agent_stack_orchard.tick.duration" "tick.poll")

while :; do
	log="$(kubectl -n smoke logs deployment/collector --tail=-1)"
	missing=()
	for word in "${wanted[@]}"; do
		grep -q -- "$word" <<<"$log" || missing+=("$word")
	done

	[[ ${#missing[@]} -eq 0 ]] && break

	if ((SECONDS > deadline)); then
		say "the collector received no ${missing[*]} inside three minutes"
		exit 1
	fi

	sleep 10
done

say "the collector has the metrics and the spans of the controller"

# leaked fails the run when a secret is in the text of a source. It names the source and the
# secret, and never the value, because the value is what must not reach a log.
leaked() {
	local source="$1" text="$2" name value

	shift 2

	while (($#)); do
		name="$1" value="$2"
		shift 2

		if grep -qF -- "$value" <<<"$text"; then
			say "the $name leaked into the $source"
			return 1
		fi
	done
}

say "checking that no token leaves through a log, the telemetry, or the machine list"
controller_log="$(kubectl -n smoke logs -l app.kubernetes.io/name=bk-agent-stack-orchard \
	--all-containers --tail=-1)"
collector_log="$(kubectl -n smoke logs deployment/collector --tail=-1)"

# A source with no content proves nothing, so an empty one is a failure.
[[ -n "$controller_log" ]] || {
	say "the controller wrote no log, so the check of it proves nothing"
	exit 1
}
[[ -n "$collector_log" ]] || {
	say "the collector wrote no output, so the check of it proves nothing"
	exit 1
}

kubectl -n orchard port-forward svc/orchard 16120:6120 >/dev/null 2>&1 &
forward=$!

# The certificate names the service and not localhost, so curl connects to the forwarded port
# under the name of the service. The credentials go in on standard input, out of the process list.
vms=""
code=000
for _ in $(seq 1 30); do
	code="$(printf 'user = "bk-agent-stack-orchard:%s"\n' "$controller_token" |
		curl -s -K - --cacert "$WORK/ca.crt" -o "$WORK/vms.json" -w '%{http_code}' \
			--connect-to orchard.orchard.svc:6120:127.0.0.1:16120 \
			https://orchard.orchard.svc:6120/v1/vms)" || code=000

	[[ "$code" == 200 ]] && break
	sleep 1
done

[[ "$code" == 200 ]] || {
	say "orchard answered with the status $code, so the check of its machine list proves nothing"
	exit 1
}

vms="$(<"$WORK/vms.json")"

[[ -n "$vms" ]] || {
	say "orchard gave an empty body, so the check of its machine list proves nothing"
	exit 1
}

# The list is an array, or null when Orchard has no machine and encodes the empty list as null.
jq -e 'type == "array" or . == null' <<<"$vms" >/dev/null || {
	say "the machine list of orchard is not a JSON array"
	exit 1
}

for source in "controller log:$controller_log" "collector output:$collector_log" \
	"machine list of orchard:$vms"; do
	leaked "${source%%:*}" "${source#*:}" \
		"cluster agent token" "$agent_token" "Orchard token" "$controller_token"
done

say "the controller is ready, the collector has its telemetry, and no token leaked"
