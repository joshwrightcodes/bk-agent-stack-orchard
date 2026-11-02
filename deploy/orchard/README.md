# The Orchard controller on Kubernetes

This directory is a kustomize base for the Orchard controller of the fleet (target design,
section 11). An overlay of the operator sets the namespace, the image, the namespace of the
controller and the CIDRs of the Mac hosts in the NetworkPolicy, an internal load balancer, and the
endpoint of the collector.

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## The image

Orchard publishes its image to `ghcr.io/openai/orchard`, and that package does not answer a pull
without credentials (2026-09-26). The last public image is `ghcr.io/cirruslabs/orchard:0.55.0`,
which is older than the minimum version of the design.
[openai/orchard#501](https://github.com/openai/orchard/issues/501) asks for the images of the
later versions. When Orchard publishes them, the overlay can name that image and this directory
can drop `image/`. `image/Dockerfile` builds the same image from the release of
Orchard: the release binary on distroless, with its data in `/data`. It checks the archive against
the SHA-256 that `mise.toml` pins, and `scripts/check-deps.py` keeps the two in agreement.

```sh
mise run orchard-image
docker tag orchard:0.58.0 <registry of the fleet>/orchard:0.58.0
docker push <registry of the fleet>/orchard:0.58.0
```

The image carries the licence of Orchard, FSL-1.1-ALv2, in `/usr/share/doc/orchard/LICENSE`. It is
not part of the image of the controller of this project, which carries no Orchard code
(ADR-0010). Push it only to the registry of the fleet.

## The Secrets

The base creates no Secret, so no token goes through the repository. Create these two first.

`orchard-bootstrap`, with three tokens that you generate:

| Key | Use |
|---|---|
| `bootstrap-admin-token` | The `bootstrap-admin` account of Orchard, which the bootstrap Job uses |
| `controller-token` | The `bk-agent-stack-orchard` account: `BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN` of the controller |
| `worker-token` | The `worker` account of the Mac hosts |

`orchard-tls`, a TLS Secret with `tls.crt`, `tls.key`, and `ca.crt`, for example from
cert-manager. The certificate MUST name the Service (`orchard`, `orchard.<namespace>.svc`) and the
name of the load balancer that the Mac hosts use. `ca.crt` is the PEM that the controller of this
project takes in `BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT`. Orchard can make a certificate for
itself, but then each client must trust that one certificate, and a new PVC gives a new one.

## The overlay

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: orchard
resources:
  - github.com/joshwrightcodes/bk-agent-stack-orchard/deploy/orchard?ref=<tag>
images:
  - name: orchard
    newName: <registry of the fleet>/orchard
    newTag: 0.58.0
patches:
  # The namespace of the controller of this project, and the CIDRs of the Mac hosts. The base holds
  # the placeholders bk-agent-stack-orchard and 192.0.2.0/24 (RFC 5737), so the base admits no real
  # host. test/smoke/orchard shows the patch of the namespace.
  - target: {kind: NetworkPolicy, name: orchard}
    patch: |
      - op: replace
        path: /spec/ingress/0/from/0/namespaceSelector/matchLabels/kubernetes.io~1metadata.name
        value: <namespace of the controller>
      - op: replace
        path: /spec/ingress/0/from/2/ipBlock/cidr
        value: <CIDR of the Mac hosts>
configMapGenerator:
  - name: orchard-telemetry
    behavior: replace
    literals:
      - OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector>:4317
      - OTEL_EXPORTER_OTLP_PROTOCOL=grpc
      - OTEL_SERVICE_NAME=orchard
```

The base keeps Orchard inside the cluster. The Mac hosts are outside it, so they need an internal
load balancer. `deploy/orchard-overlays/internal-lb` is an example that sets `type: LoadBalancer`
with the annotation of one cloud. It names the annotations of GKE, AWS, and AKS with their sources,
checked on 2026-11-02. Use it as a resource of the overlay, or copy its patch. An overlay MUST NOT
make the load balancer public.

## What it runs

- `statefulset.yaml`: one Orchard controller, with a PVC at `/data`. Orchard keeps its state in an
  embedded database and runs one scheduler with no leader election, so at most one pod may write
  to `/data`. A StatefulSet starts no replacement until the old pod is gone, also when a node
  fails, which a Deployment does not promise. A deletion of the StatefulSet leaves the PVC. The
  loss of the PVC is recoverable, because the workers register again and the controller of this
  project removes the VMs of its jobs, so there is no backup.
- `service.yaml`: the headless governing Service of the StatefulSet, and a ClusterIP Service on 6120
  that the controller and the bootstrap Job reach. The Mac hosts reach Orchard through the overlay
  of the internal load balancer.
- `networkpolicy.yaml`: the Orchard pod accepts TCP 6120 from the controller of this project (by
  namespace and pod label), from the bootstrap Job, and from the CIDRs of the Mac hosts, and from
  nothing else. See "The NetworkPolicy".
- `bootstrap-job.yaml`: sets the accounts `bk-agent-stack-orchard` and `worker`, each with
  `compute:read` and `compute:write` only, through the REST API of Orchard. It replaces an account
  that exists and creates one that does not, so a run after a rotation of a token sets the new
  token. Run it again after each rotation: delete the Job and apply the overlay. It gives the
  token and the body to `curl` on standard input, so no token is on the argv of `curl`.

## The NetworkPolicy

A cluster enforces a NetworkPolicy only when its CNI plugin does. kind uses kindnet, which may not
enforce it, so a pass of `mise run smoke` does not prove that the policy blocks other callers.
The smoke test proves only that the controller and the bootstrap Job reach Orchard with the policy
in place. An operator MUST test the policy on the CNI of the cluster: a pod of another namespace
MUST NOT reach 6120. The probes of the StatefulSet are TCP probes of the kubelet. On most CNI
plugins the policy does not block them, and the policy has no rule for them. Check this on the CNI
of the cluster.

## First start and after the first bootstrap

Orchard 0.58.0 behaves as follows (source: `internal/command/controller/bootstrap.go` at the tag
`0.58.0` of the Orchard repository, read on 2026-11-02; `orchard delete service-account` from the
installed 0.58.0 binary):

- When `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` is set, each start makes the `bootstrap-admin` account
  again with that token and all roles, also when other accounts exist.
- When the variable is not set and the database holds service accounts, Orchard does no bootstrap
  and starts. It does not delete `bootstrap-admin`. The account and its token stay in the database.
- When the variable is not set and the database holds no service account, Orchard makes
  `bootstrap-admin` with a random token and prints that token in its log.
- Orchard does not refuse to start in any of these cases.

So an unset variable is not enough: the account stays and its token stays valid. The operator MUST
remove the variable and then delete the account. The order is important: while the variable is
set, a start makes the account again.

1. Do the first start with the variable set, and wait for the bootstrap Job to complete.
2. Remove `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` from the StatefulSet in the overlay, and wait until the
   new pod is ready.
3. Delete the account. Use a context for `bootstrap-admin` or the API:
   `orchard delete service-account bootstrap-admin`, or
   `DELETE /v1/service-accounts/bootstrap-admin`.
   Give the credentials to `curl` on standard input, as `smoke.sh` does.
4. Delete the key `bootstrap-admin-token` from the Secret `orchard-bootstrap`.
5. Check that `GET /v1/service-accounts` with the controller account no longer lists
   `bootstrap-admin`, and that the controller and the Mac hosts still connect.

To rotate a token later, do the first start procedure again with a new token: set the variable
and the key, restart Orchard, delete the Job and apply the overlay, and then do steps 2 to 4.
The runbook is `docs/runbooks/orchard-bootstrap-admin.md`.

The Mac hosts, and the way that each worker gets its credentials, are W-22.
