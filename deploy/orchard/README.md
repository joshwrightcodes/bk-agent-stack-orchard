# The Orchard controller on Kubernetes

This directory is a kustomize base for the Orchard controller of the fleet (target design,
section 11). An overlay of the operator sets the namespace, the image, the annotation of the
internal load balancer, and the endpoint of the collector.

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
docker tag orchard:0.57.0 <registry of the fleet>/orchard:0.57.0
docker push <registry of the fleet>/orchard:0.57.0
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
    newTag: 0.57.0
patches:
  - target: {kind: Service, name: orchard}
    patch: |
      - op: add
        path: /metadata/annotations
        value: {networking.gke.io/load-balancer-type: Internal}
configMapGenerator:
  - name: orchard-telemetry
    behavior: replace
    literals:
      - OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector>:4317
      - OTEL_EXPORTER_OTLP_PROTOCOL=grpc
      - OTEL_SERVICE_NAME=orchard
```

## What it runs

- `statefulset.yaml`: one Orchard controller, with a PVC at `/data`. Orchard keeps its state in an
  embedded database and runs one scheduler with no leader election, so at most one pod may write
  to `/data`. A StatefulSet starts no replacement until the old pod is gone, also when a node
  fails, which a Deployment does not promise. A deletion of the StatefulSet leaves the PVC. The
  loss of the PVC is recoverable, because the workers register again and the controller of this
  project removes the VMs of its jobs, so there is no backup.
- `service.yaml`: the headless governing Service of the StatefulSet, and a load balancer on 6120
  that the Mac hosts and the controller reach. The overlay makes the load balancer internal.
- `bootstrap-job.yaml`: sets the accounts `bk-agent-stack-orchard` and `worker`, each with
  `compute:read` and `compute:write` only, through the REST API of Orchard. It replaces an account
  that exists and creates one that does not, so a run after a rotation of a token sets the new
  token. Run it again after each rotation: delete the Job and apply the overlay.

The Mac hosts, and the way that each worker gets its credentials, are W-22.
