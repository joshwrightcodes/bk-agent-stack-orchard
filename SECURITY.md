# Security

## Reporting a vulnerability

Report a vulnerability through
[GitHub private security advisories](https://github.com/joshwrightcodes/bk-agent-stack-orchard/security/advisories/new).
Do not open a public issue, and do not include a working exploit in the first message.

The maintainers work on the project in their own time. Expect a first answer within one week.
The maintainers agree a disclosure date with the reporter after the cause is clear.

When the maintainers confirm a vulnerability, they publish a GitHub security advisory for it and
request a CVE through that advisory.

## Supported versions

The first release is v0.1.0. Until 1.0.0, only the latest 0.x minor release receives security
fixes. A fix does not go to an older minor release. A user of an older release has to upgrade to
the latest minor release to get a fix.

## Safe harbour

Research in good faith is welcome, if it follows this policy and the researcher runs it on the
researcher's own deployment of the project. The maintainers do not pursue legal action for such
research. This policy does not authorise testing of a system that the researcher does not own or
operate. That includes the deployments of other people, Buildkite, and GitHub.

## What the project protects

The controller runs untrusted code. Every build in a job VM is untrusted, by decision
([ADR-0004](docs/adrs/0004-treat-all-builds-as-untrusted.md)). The project protects these
properties, and a defect in one of them is a vulnerability:

* A job VM receives a job acquisition token for its own job only. The Buildkite cluster agent
  token and the Orchard credentials never enter a VM.
* A job VM cannot reach the host, another VM, or a private network, except the addresses that its
  profile permits.
* A job VM has no host directory mounts, and it never runs a second job.
* Telemetry contains no source code, no token, and no job or VM identifier in metric attributes.

[`docs/quality/test-strategy.md`](docs/quality/test-strategy.md) holds these properties as
invariants I-1 to I-8, and the gates assert them.

## Accepted risks

These are known, recorded, and accepted. They are not vulnerabilities in this project:

* Buildkite has no agent token with a scope smaller than a cluster, so a leaked cluster agent
  token reaches the other self-hosted queues of the same cluster
  ([ADR-0007](docs/adrs/0007-use-a-dedicated-queue-for-the-macos-fleet.md),
  [ADR-0008](docs/adrs/0008-use-a-dedicated-cluster-agent-token.md)). An expiry of 90 days or less
  and an IP allowlist limit it.
* The Orchard `compute:write` role also permits the creation and the deletion of workers, because
  Orchard has no narrower role.
* Each Mac host has its own Orchard service account, but Orchard 0.58.0 does not tie an account to a
  worker name. A stolen host token can register, update, and delete any worker name, and it can
  read and write VMs, because the roles `compute:read` and `compute:write` are the roles that a
  worker needs. The controls are a token for each host, so the operator revokes one host without
  a change on the others (runbook
  [`docs/user/runbooks/orchard-worker-accounts.md`](docs/user/runbooks/orchard-worker-accounts.md)), and the
  alert on an unknown worker name (target design, section 10.6). The alert sees only a name that
  the operator does not know. A stolen token that registers a known name, for example the record of
  one host, is not detected. This is a residual risk.
* Job logs go to Buildkite SaaS, and they can contain parts of source code, such as compiler
  errors.
* Artifacts go to Buildkite-managed storage, in the region of Buildkite. The owner accepts this
  risk (owner statement, 2026-11-02; PR-032). Checkouts and build data stay on owner
  infrastructure, in the operating country, only as far as configuration holds. The residency
  setting protects against configuration mistakes. It is not a boundary against a hostile build. A
  build that has root in its VM and internet egress can send source code, build data, and
  artifacts to any address that its profile permits. The mitigation is the egress allowlist of
  each profile.
* An Orchard account with the `compute:read` role can read the job acquisition token of a job
  VM, because the token is in the VM specification for the full life of the VM. The token opens
  one job only. It ends when the reservation of its job ends, and it expires after a maximum of
  30 minutes (target design, sections 3 and 9.1). The
  mitigation is to give `compute:read` to few accounts and to no build.
* One compromised Mac host exposes the jobs of the whole fleet, and not only the jobs of that
  host. Each worker account has `compute:read`, because an Orchard worker needs it, and that role
  reads the specification of every VM on every host. Root on one host, for example after a VM
  escape, or anyone who holds the Orchard token of one host, can therefore read the job
  acquisition token of every job that has a VM. That person can then acquire a job before its VM
  does, and run the job with its secrets on a machine of their choice. The IP allowlist of the
  cluster agent token gives no protection here: Buildkite applies it when an agent registers with
  a job acquisition token, and
  [ADR-0008](docs/adrs/0008-use-a-dedicated-cluster-agent-token.md) requires it to include the
  egress of the job VMs, which is the egress of the hosts. A request from any host therefore
  passes. The limits are that a token opens one job, that it ends when the reservation of its job
  ends, which the controller stops extending once the start of the VM fails, and that it expires
  one minute after the start timeouts of its profile in any case (23 minutes with the defaults,
  and never more than `jobTokenLifetime`, at most 30 minutes). The controls are a token for each
  host, so that the operator revokes one host alone (runbook
  [`docs/user/runbooks/orchard-worker-accounts.md`](docs/user/runbooks/orchard-worker-accounts.md)),
  and the pf isolation of each host from its VMs. Orchard has no role narrower than
  `compute:read` for a worker. The residual risk remains while the job acquisition token is in the
  VM specification.
* When a profile enables agent telemetry, the credential of the OpenTelemetry collector (the
  headers) enters the VM. A hostile build can read it. The mitigation is that the collector
  uses its own ingest token, with the lowest privilege, that can send telemetry and do nothing
  else.
* Orchard 0.58.0 makes a `bootstrap-admin` account with all roles, and prints its random token in
  its log, when it starts with an empty database and without `ORCHARD_BOOTSTRAP_ADMIN_TOKEN`. This
  can happen at a first start without the kustomize component
  `deploy/orchard-overlays/first-start`, and after the loss of the PVC. The bootstrap Job then
  fails with a message that names the component. The control is the runbook
  [`docs/user/runbooks/orchard-bootstrap-admin.md`](docs/user/runbooks/orchard-bootstrap-admin.md): add the
  component before a start with an empty PVC, treat the log of the Orchard pod as a secret, and
  set the account again if its token was logged. A start with the component sets the account
  again with the token of the Secret, so the logged token stops working. The residual risk is
  that a log shipper keeps the token until the operator purges it.
* The bootstrap admin of Orchard has all roles, and its token stays in the Kubernetes Secret
  `orchard-bootstrap` after the first bootstrap, because each add, rotation, migration, or
  revocation of a host runs the bootstrap Job again. The controls are that only the Job mounts
  the Secret (the Orchard pod reads it only while the overlay has the component
  `deploy/orchard-overlays/first-start`, for the first start), that Kubernetes RBAC limits who can
  read Secrets of the `orchard` namespace, and that the operator rotates the token (runbook
  [`docs/user/runbooks/orchard-bootstrap-admin.md`](docs/user/runbooks/orchard-bootstrap-admin.md)). The
  same Secret holds the token of the `operator` account, with `compute:read` and `compute:write`,
  which the runbooks use for workers and VMs, and the same controls cover it. The
  environment of the Orchard pod holds no admin token after the first start. The data volume of
  Orchard does, while the account exists: Orchard 0.58.0 keeps the token in its database, and
  writes it in plain text to `/data/orchard.yml` at each start. The residual risk is that a
  person or a workload with the right to read Secrets of the namespace, to run commands in the
  Orchard pod, or to read its PVC, or a copy of the cluster state or of the PVC, gives full
  control of Orchard, and that a late rotation leaves an old token valid. A fleet with no planned
  host change MAY delete the account, which also takes a working token off the data volume.
* Releases have one maintainer as the only reviewer, and the rules of the main branch have an
  administrator bypass. No second person reviews a release. The mitigation is that the release
  gate (T-100 to T-105) and the pipeline checks run before every release.

## Secrets

The repository contains no secret, and no configuration file holds one. Secrets reach the
controller through environment variables only. A report of a committed secret is welcome through
the same private channel.
