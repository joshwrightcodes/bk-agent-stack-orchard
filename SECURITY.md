# Security

## Reporting a vulnerability

Report a vulnerability through
[GitHub private security advisories](https://github.com/joshwrightcodes/bk-agent-stack-orchard/security/advisories/new).
Do not open a public issue, and do not include a working exploit in the first message.

This is a personal project with one maintainer. Expect a first answer within one week. The
maintainer will agree a disclosure date with the reporter after the cause is clear.

## What the project protects

The controller runs untrusted code. Every build in a job VM is untrusted, by decision
([ADR-0004](docs/adrs/0004-treat-all-builds-as-untrusted.md)). The project protects these
properties, and a defect in one of them is a vulnerability:

* A job VM receives a job acquisition token for its own job only. The Buildkite cluster agent
  token and the Orchard credentials never enter a VM.
* A job VM cannot reach the host, another VM, or a private network, except the addresses that its
  profile permits.
* A job VM has no host directory mounts, and it never runs a second job.
* Source code checkouts, build data, and artifacts stay on owner infrastructure, in the operating
  country.
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
* Job logs go to Buildkite SaaS, and they can contain parts of source code, such as compiler
  errors.

## Secrets

The repository contains no secret, and no configuration file holds one. Secrets reach the
controller through environment variables only. A report of a committed secret is welcome through
the same private channel.
