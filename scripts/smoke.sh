#!/usr/bin/env bash
# The smoke deployment of W-21: the controller from its Helm chart and Orchard from its kustomize
# base, on a kind cluster, with the fake Stacks API and an OpenTelemetry collector. It passes when
# the controller becomes ready through its gRPC readiness probe, and the collector receives its
# metrics and its spans.
#
# Usage: scripts/smoke.sh
#
# KEEP=1 keeps the cluster after the run, for a person who wants to look at it. The tools come
# from mise: kind, ko, kubectl, kustomize, helm, and docker.
set -euo pipefail

CLUSTER="bkaso-smoke"
ROOT="$(git rev-parse --show-toplevel)"
WORK="$(mktemp -d)"

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

kubectl -n smoke create secret generic bk-agent-stack-orchard \
	--from-literal=BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN=bkct_smoke \
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

say "the controller is ready, and the collector has its metrics and its spans"
