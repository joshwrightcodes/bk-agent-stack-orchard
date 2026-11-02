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

The base creates no Secret, so no token goes through the repository. Create these three first.

`orchard-bootstrap`, with two tokens that you generate:

| Key | Use |
|---|---|
| `bootstrap-admin-token` | The `bootstrap-admin` account of Orchard. After the first bootstrap only the bootstrap Job mounts it (see "After the first bootstrap") |
| `controller-token` | The `bk-agent-stack-orchard` account: `BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN` of the controller |

`orchard-worker-tokens`, with one key for each Mac host and no other key:

| Key | Use |
|---|---|
| `worker-<host>` | The token of the account `worker-<host>` of that host. `<host>` MUST be a DNS label (lower case letters, digits, and hyphens, a maximum of 63 characters), and it MUST be the `orchard_worker_name` of the host in the inventory |

The keys are the list of hosts, so the tokens stay in a Secret and no ConfigMap holds a token.
A fleet that has the shared account `worker` MUST follow "Migrate from the shared account" in
`docs/runbooks/orchard-worker-accounts.md`, because the Job does not delete that account. The
bootstrap Job refuses a key whose host is not a DNS label, and it fails when the Secret has no
such key. Each host has its own token (`openssl rand -hex 24`). The operator keeps the same token
in the inventory vault (`deploy/hosts/README.md`).

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
- `bootstrap-job.yaml`: sets the accounts `bk-agent-stack-orchard` and `worker-<host>` for each key
  of the Secret `orchard-worker-tokens`, each with `compute:read` and `compute:write` only (the
  roles that an Orchard worker needs, target design section 9.2), through the REST API of Orchard.
  It never deletes an account: a host that leaves the Secret keeps its account until the operator
  revokes it (`docs/runbooks/orchard-worker-accounts.md`). It replaces an account
  that exists and creates one that does not, so a run after a rotation of a token sets the new
  token. Run it again after each change of a host: delete the Job and apply the overlay. It needs
  the bootstrap admin, so that account and its Secret key stay (see "First start and after the
  first bootstrap"). It gives the
  token and the body to `curl` on standard input, so no token is on the argv of `curl`.

## The NetworkPolicy

A cluster enforces a NetworkPolicy only when its CNI plugin does. kind uses kindnet. The kindnet
manifest of kind 0.33.0 grants kindnet the right to read NetworkPolicy objects (checked in the
kind binary on 2026-11-02), so `mise run smoke` runs with the policy in force. The smoke test
therefore proves the admitted paths: the controller and the bootstrap Job reach Orchard. It does
not prove the refused paths. An operator MUST test the policy on the CNI of the cluster: a pod of
another namespace MUST NOT reach 6120.

The probes of the StatefulSet are TCP probes of the kubelet. The Kubernetes documentation says
that traffic to and from the node where a pod runs is always allowed, so the policy has no rule
for the probes (checked 2026-11-02,
`https://kubernetes.io/docs/concepts/services-networking/network-policies/`). A CNI plugin MAY
differ. The first smoke run on kindnet is the check: if Orchard never becomes ready, add an
ingress rule for the node CIDR. Do not remove the policy.

## Reach the API as an operator

The policy admits TCP 6120 only from the controller of this project, the bootstrap Job, and the
Mac hosts. It refuses a workstation or a bastion, on the ClusterIP and on the internal load
balancer. An operator MUST NOT add the CIDR of an office to the policy to get round this. Use a
port-forward:

```sh
kubectl -n orchard port-forward svc/orchard 16120:6120
```

`kubectl port-forward` makes the container runtime open the connection inside the network
namespace of the pod, so Orchard sees a local connection and not a connection from the network.
The Kubernetes documentation says that traffic from the node of a pod is always allowed, so the
base policy does not refuse it (checked 2026-11-02, same page as above). `scripts/smoke.sh` uses
this path with the policy in force. The certificate names the Service and not `localhost`, so
`curl` connects to the forwarded port under the name of the Service:

```sh
kubectl -n orchard get secret orchard-tls -o go-template='{{index .data "ca.crt" | base64decode}}' > ca.crt
```

`ca.crt` is a public certificate, so the file is safe to keep. The calls that need an admin use the
token of `bootstrap-admin` from the Secret `orchard-bootstrap`. The token goes from `kubectl`
into a shell variable and into the standard input of `curl`, never into the argv and never into a
file. This function wraps it (`orchard_admin GET /v1/service-accounts`):

```sh
orchard_admin() {
  kubectl -n orchard get secret orchard-bootstrap \
    -o go-template='{{index .data "bootstrap-admin-token" | base64decode}}{{"\n"}}' |
    { read -r token; printf 'user = "bootstrap-admin:%s"\n' "$token"; } |
    curl -sS -K - --cacert ca.crt -X "$1" \
      --connect-to orchard.orchard.svc:6120:127.0.0.1:16120 \
      "https://orchard.orchard.svc:6120$2"
}
```

Change `orchard` to the namespace of the overlay. Do not run `orchard context create` for these
calls, because it takes the token as a flag. The runbooks give every call in this form
(`GET /v1/service-accounts`, `GET /v1/workers`, `DELETE /v1/service-accounts/<name>`,
`DELETE /v1/workers/<name>`). The controller account has `compute:read` and `compute:write` only,
so it cannot list service accounts (`admin:read`). Use the controller account for none of these.

## First start and after the first bootstrap

Orchard 0.58.0 behaves as follows (source: `internal/command/controller/bootstrap.go` at the tag
`0.58.0` of the Orchard repository, read on 2026-11-02; `orchard delete service-account` from
the orchard 0.58.0 binary that mise installs, `mise x -- orchard delete --help`):

- When `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` is set, each start makes the `bootstrap-admin` account
  again with that token and all roles, also when other accounts exist.
- When the variable is not set and the database holds service accounts, Orchard does no bootstrap
  and starts. It does not delete `bootstrap-admin`. The account and its token stay in the database.
- When the variable is not set and the database holds no service account, Orchard makes
  `bootstrap-admin` with a random token and prints that token in its log.
- Orchard does not refuse to start in any of these cases.

The bootstrap admin is retired in this sense: the Orchard pod no longer holds its token, and the
token stays in one place, the Secret `orchard-bootstrap`, which only the bootstrap Job mounts.
The account stays in Orchard, because every change of a host (add, rotate, migrate, revoke) runs
the Job again, and the Job needs it. Each of these changes then works with no restart of Orchard.

1. Do the first start with the variable set, and wait for the bootstrap Job to complete.
2. Remove `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` from the StatefulSet with this patch in the overlay, and
   wait until the new pod is ready. Orchard then does not make the account again at each start,
   and the Orchard pod holds no admin token in its environment:

   ```yaml
   patches:
     - target: {kind: StatefulSet, name: orchard}
       patch: |
         - op: remove
           path: /spec/template/spec/containers/0/env/0
   ```

3. Rotate the token now, so the token of the first start is not the standing one (the procedure
   is in `docs/runbooks/orchard-bootstrap-admin.md`, "Rotation"). Rotate it again on a schedule
   of the operator and when a person who could read the Secret leaves.
4. Check that no other pod mounts or reads the Secret. In this repository, only
   `bootstrap-job.yaml` references `orchard-bootstrap` after step 2, and the command below lists
   every user of it in the namespace. Only the pods of the Job MUST appear:

   ```sh
   kubectl -n orchard get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.volumes[*].secret.secretName}{"\t"}{.spec.containers[*].env[*].valueFrom.secretKeyRef.name}{"\n"}{end}' |
     grep orchard-bootstrap
   ```

   Kubernetes RBAC then decides who can read the Secret. Give `get`, `list`, and `watch` on
   Secrets of the namespace to few people, and to no service account of a workload.
5. Check that the account works and that the Orchard pod has no token. The first command lists
   the accounts with the bootstrap admin (see "Reach the API as an operator"). The second prints
   nothing:

   ```sh
   orchard_admin GET /v1/service-accounts
   kubectl -n orchard get pod orchard-0 -o jsonpath='{.spec.containers[*].env[*].name}' |
     grep ORCHARD_BOOTSTRAP_ADMIN_TOKEN
   ```

### Delete the account completely

A fleet with no planned change of a host MAY delete `bootstrap-admin` and its Secret key instead.
First do steps 1 and 2. Then delete the account, check that the token of the Secret is refused
(the answer is an HTTP 401 error), and only then delete the key:

```sh
orchard_admin DELETE /v1/service-accounts/bootstrap-admin
orchard_admin GET /v1/service-accounts
kubectl -n orchard patch secret orchard-bootstrap --type json \
  -p '[{"op":"remove","path":"/data/bootstrap-admin-token"}]'
```

To change a host later, re-create the account:
put the variable back in the StatefulSet and the key `bootstrap-admin-token` in the Secret,
restart Orchard (a short outage of the API for the workers and the controller), run the Job, and
retire again with steps 2 to 5.

### Warning: a start with an empty PVC can log an admin token

The loss of the PVC is recoverable, but it has a side effect. After the retirement above, a new
PVC gives Orchard an empty database and no variable. Orchard then makes `bootstrap-admin` with a
random token that has all roles, and it prints that token in its log. A log shipper copies it. The
operator MUST treat the log of the Orchard pod as a secret.

- Prevention: before a start with an empty PVC, put the variable back (with the token of the key
  `bootstrap-admin-token`, which stays in the Secret unless the operator deleted it). Orchard then
  uses the known token and logs `<hidden>`. Source: `internal/command/controller/bootstrap.go` at
  the tag `0.58.0`, read on 2026-11-02, which sets `serviceAccountTokenToDisplay = "<hidden>"`
  when the variable is present. Do steps 2 and 3 after the bootstrap Job completes.
- Recovery, if it already happened: set the account again with a new token (the rotation in the
  runbook), and purge the log lines where the logging system allows it.

The Mac hosts, and the way that each worker gets its credentials, are W-22. Orchard does not tie
an account to a worker name, so a host with the token of another host can register as any name.
The alert "Unknown worker" of the target design (section 10.6) sees only a name that the operator
does not know. It does not see a known name that another host uses.
