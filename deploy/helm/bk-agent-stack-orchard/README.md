# The Helm chart of the controller

This chart deploys the controller of bk-agent-stack-orchard on Kubernetes (target design,
section 11). It runs one replica, with the strategy `Recreate`, a ConfigMap for the configuration
file, and a Secret that the operator creates before the install.

## The values

`values.yaml` has a comment for each value. The table below groups them.

| Value | Use |
|---|---|
| `image.repository`, `image.tag`, `image.digest`, `image.pullPolicy` | The image. The tag defaults to the `appVersion` of the chart, and a digest wins over the tag. |
| `imagePullSecrets` | The Secrets that the kubelet uses to pull the image from a private registry. Each entry is `{name: <secret>}`. The default is empty. |
| `config` | The configuration file of the controller, as the [configuration reference](../../../docs/user/configuration.md) describes. It never holds a secret. A change restarts the pod. |
| `secret.name` | The existing Secret whose keys are the environment variables of the tokens. The chart creates no Secret. |
| `otel.endpoint`, `otel.protocol` | The export of the telemetry. Other SDK settings go in `extraEnv`. |
| `extraEnv` | More environment variables that are not secret, or a reference to a key of a Secret with `valueFrom.secretKeyRef`. A variable that a profile sends to a VM starts with `VM_`, and it is a secret: put it in the Secret of `secret.name`, or reference it with `secretKeyRef`. A literal `value` goes into the Deployment and the history of the release. |
| `resources` | The requests and limits of the container. |
| `networkPolicy` | The egress destinations. See the section "The NetworkPolicy". |
| `deploymentAnnotations`, `podAnnotations`, `podLabels` | Annotations of the Deployment, and annotations and labels of the pod. The selector labels of the chart win over a label of `podLabels` with the same key. |
| `priorityClassName` | The PriorityClass of the pod. Empty means the default class. |
| `terminationGracePeriodSeconds` | How long the pod may take to stop. The default is 45. The controller withdraws its stack and then stops its telemetry, each in 10 s or less, so the schema refuses a value below 20. |
| `nodeSelector`, `tolerations`, `affinity` | The placement of the pod. |

### The values schema

`values.schema.json` checks the values when `helm lint`, `helm template`, and `helm install` run.
It sets `additionalProperties: false` at each level of the values, so a misspelt key stops the
install and does not pass without effect.

The schema mirrors the rules of the controller that JSON Schema can express:

* A profile name has only lower-case letters, digits, and dashes.
* A `match` key is a lower-case literal, and it is not `queue`, `profile`, or `agent-stack`. A
  `match` value is a literal with no wildcard, comma, quote, or newline, and no ASCII white space
  at an end.
  `buildkite.queue` has the same limits, except for the wildcard.
* Each duration is a Go duration as `time.ParseDuration` reads it, such as `500ms`, `5m`, `1h30m`,
  or `1.5m`. A bare number is not a duration. This includes `reservationExpiry` and
  `jobTokenLifetime`. The schema does not check the ranges of these two values. The controller
  refuses them at start-up when `reservationExpiry` is not a whole number of seconds from 1 to
  3600, or `jobTokenLifetime` is not a whole number of seconds from 1 to 1800.
* A `softnetAllow` entry is an IPv4 CIDR range with a prefix length from 16 to 32. `@host`,
  `@peers`, IPv6 ranges, and bare addresses fail. An exact repeat of an entry fails.
* `vmCredentials.usernameEnv`, `vmCredentials.passwordEnv`, and `otel.headersEnv` start with `VM_`.
* An `extraEnv` entry whose name starts with `VM_` has no literal `value`. It takes its value from
  a Secret with `valueFrom`.
* `controllerID` has 23 characters or fewer, and it starts and ends with a letter or a digit.
* `agent.source` is `download` or `image`, `agent.url` is an `https` URL, and `agent.sha256` is 64
  lower-case hexadecimal characters, in `defaults` and in each profile.

The schema is a first check. It is not the authority. The controller checks its configuration
again when it starts, and it refuses a value that the schema cannot express. For example:

* a CIDR range with host bits set, such as `10.0.0.1/24`;
* two profiles with the same name or the same `match` map;
* an `unplaced`, `boot`, and `acquire` sum that is longer than `jobTokenLifetime`;
* a duration that is out of range, or not a whole number of seconds, for `reservationExpiry` and
  `jobTokenLifetime`;
* a `reservationRenewBefore` that is not shorter than `reservationExpiry`;
* a source interval that is not shorter than `stateFreshness`;
* a `softnetAllow` list with the same range in two spellings;
* a `softnetAllow` range wider than /29 inside `10.0.0.0/8`, `172.16.0.0/12`, or `192.168.0.0` to
  `192.168.99.255`, where Softnet puts the network of each VM and an address of the host;
* an `agent.url` without its `agent.sha256`, or the reverse;
* a `vmCredentials` block that does not agree with `startupTransport`;
* a variable that holds no value in the Secret.

A value that passes the schema can therefore still stop the controller. Read the log of the pod
when it does.

CI runs `mise run check-deploy`. The task lints the chart with `ci/values.yaml` and each file in
`ci/good/`, and it fails if a file in `ci/bad/` lints clean or fails for a reason other than the
schema. A change of the schema adds a bad file for the new rule. `.helmignore` keeps `ci/` out of
the packaged chart.

## The NetworkPolicy

The controller calls the Stacks API of Buildkite, Orchard, and the OpenTelemetry collector. It
calls nothing else, and it answers only the probes. The chart sets egress to DNS, and to the port
of each destination. A standard NetworkPolicy matches addresses, pods, and namespaces, and not DNS
names. With no `to` list, a destination allows any peer on its port. The notes of `helm install`
and `helm upgrade` then name each such destination in a warning.

For production, give each destination its peers in `to`. This example is a values file. The
addresses are placeholders from the documentation ranges of RFC 5737, and the operator replaces
them:

```yaml
networkPolicy:
  enabled: true
  buildkite:
    port: 443
    # The addresses that agent.buildkite.com resolves to, which the operator checks and updates.
    to:
      - ipBlock:
          cidr: 203.0.113.0/24
  orchard:
    port: 6120
    # The controller reaches Orchard inside the cluster, through the ClusterIP Service. The
    # policy sees the address of the Orchard pod, so an address peer does not match. Select the
    # pod by its namespace and label.
    to:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: orchard
        podSelector:
          matchLabels:
            app.kubernetes.io/name: orchard
  collector:
    port: 4317
    to:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: observability
        podSelector:
          matchLabels:
            app.kubernetes.io/name: opentelemetry-collector
```

Keep the `ipBlock` form for destinations outside the cluster, such as Buildkite and a collector
that runs outside it.

A peer CIDR for a public service is fragile, because the service can change its addresses. A
cluster whose CNI supports a policy for DNS names can limit the egress to `agent.buildkite.com`
instead. NetworkPolicies add up: a pod can send what any policy that selects it allows. A second
policy therefore cannot narrow the port 443 rule of the chart. To limit the Stacks API by name,
set `networkPolicy.enabled: false` and let the policy below carry all egress. This example is for
Cilium. It is an example, and CI does not test it. The addresses are placeholders.

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: bk-agent-stack-orchard
spec:
  endpointSelector:
    matchLabels:
      app.kubernetes.io/name: bk-agent-stack-orchard
  egress:
    # The DNS rule lets Cilium see the answers, which toFQDNs needs.
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s:k8s-app: kube-dns
      toPorts:
        - ports:
            - port: "53"
              protocol: ANY
          rules:
            dns:
              - matchPattern: "*"
    - toFQDNs:
        - matchName: agent.buildkite.com
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
    # Orchard runs in the cluster, and the Service address becomes the pod address, so select the
    # endpoint. toCIDR does not select endpoints of the cluster.
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: orchard
            k8s:app.kubernetes.io/name: orchard
      toPorts:
        - ports:
            - port: "6120"
              protocol: TCP
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: observability
            app.kubernetes.io/name: opentelemetry-collector
      toPorts:
        - ports:
            - port: "4317"
              protocol: TCP
```

Source: Cilium documentation, "Locking Down External Access with DNS-based Policies",
<https://docs.cilium.io/en/stable/security/dns/> (checked 2026-11-02). Test the policy on the
cluster before the first production run, and check that the probes still reach the pod.

## A change of the Secret

The controller reads its environment only when it starts, and Kubernetes does not restart a pod when
a Secret that it uses changes. After a change of a token or of another key of the Secret, restart
the controller:

```sh
kubectl rollout restart deployment/<release>-bk-agent-stack-orchard --namespace <namespace>
```

`<release>` is the name of the Helm release. The strategy `Recreate` stops the old pod before it
starts the new one, so two controllers never run together. Jobs that wait for the controller wait
for the time of the restart.

A controller that watches the Secret can do this restart. The Reloader of Stakater is an example.
It restarts a workload when a Secret that the workload uses changes. It reads its annotations on
the metadata of the Deployment, so set the annotation with `deploymentAnnotations`, if the
Reloader runs in the cluster:

```yaml
deploymentAnnotations:
  secret.reloader.stakater.com/reload: bk-agent-stack-orchard
```

The annotation `reloader.stakater.com/auto: "true"` is an alternative that reloads for any Secret
or ConfigMap that the workload references. The chart does not install the Reloader, and CI does not
test this example. Source: the README of Stakater Reloader, <https://github.com/stakater/Reloader>
(checked 2026-11-02).

[RFC 2119]: https://www.rfc-editor.org/rfc/rfc2119
[RFC 8174]: https://www.rfc-editor.org/rfc/rfc8174
