# Assurance case

This document argues why the controller meets its security requirements. It gives the threat model,
the trust boundaries, the secure design principles that the design applies, and the common
weaknesses that the project counters. Each claim names the evidence: a decision record, a section of
the [target design](../architecture/target-design.md), an invariant of the
[test strategy](test-strategy.md), or a gate.

The argument has limits. The accepted risks in [SECURITY.md](../../SECURITY.md#accepted-risks) are
known, recorded, and outside these claims.

## 1. Security requirements

The controller runs untrusted code: every build in a job VM is untrusted (ADR-0004, PR-030). The
claims below are the properties that [SECURITY.md](../../SECURITY.md#what-the-project-protects)
protects, and the property of the release.

| Claim | Statement | Evidence |
|---|---|---|
| Token isolation | A job VM receives a job acquisition token for its own job only. The cluster agent token and the Orchard credentials never enter a VM. | I-3; target design section 9.1 |
| Network isolation | A job VM cannot reach the host, another VM, or a private network, except the CIDRs that its profile permits. | I-4; target design sections 9.1 and 11; the Softnet and pf tests on a real Mac |
| One job for each VM | A job VM runs one job, and it has no host directory mounts. | I-1, I-4, I-6; target design section 9.1 |
| Clean telemetry | Telemetry contains no source code, no token, and no job or VM identifier in metric attributes. | I-7; target design section 10 |
| Trusted release | A release that an operator installs is the code on `main`, built and signed by the release workflow of this repository. | ADR-0013; `scripts/verify-release.sh`; T-100 to T-105 |

## 2. Threat model

### Assets

* The cluster agent token, which opens every self-hosted queue of its cluster.
* The Orchard service account of the controller, which creates and deletes VMs.
* The job acquisition token of each job, which opens one job.
* The source code, the secrets, and the artifacts of the jobs of other pipelines.
* The Mac hosts and the networks that they reach.
* The release artifacts: the archives, the image, and the chart.

### Threat actors

| Actor | Capability | Main claims |
|---|---|---|
| A hostile build | Any author of a pipeline on the queue. The build has root in its VM and internet egress. It selects a profile with the agent tags of its job. | Token isolation, network isolation, one job for each VM |
| A network attacker | Reads or changes the traffic between the controller and Buildkite or Orchard. | Token isolation |
| A supply-chain attacker | Changes a dependency, a tool, an action, or a release artifact. | Trusted release |
| A holder of a host or Orchard credential | Has root on one Mac host, or the token of an Orchard account. | Outside the claims. SECURITY.md records the exposure as an accepted risk. |

The project does not defend against Buildkite, GitHub, or the operator's Kubernetes cluster. Those
are trusted platforms.

## 3. Trust boundaries

| Boundary | What crosses it | Controls |
|---|---|---|
| Job VM to Mac host and LAN | Network traffic of the build | Softnet gives each VM a network of its own. pf rules on each host allow only DHCP and DNS to the host, and only the `softnetAllow` CIDRs of the profiles into the private ranges (target design section 9.1). A VM has no host directory mounts, and `restart_policy` is `Never`. |
| Job VM to controller | Nothing directly | The controller has no inbound API, only its health port, and a VM cannot reach it. What a VM does reaches the controller only as the state of the VM in Orchard and the state of the job in Buildkite. |
| Pipeline author to controller | The agent tags of a job | Tags select a profile from the configured list only (ADR-0006). A job that no profile carries stays in the queue for another runner, and the controller never reserves it (ADR-0016, I-8). The rule parser has a fuzz test. |
| Controller to Buildkite | The cluster agent token, job reservations, job acquisition tokens | HTTPS with TLS 1.2 or later and certificate verification. The controller refuses a plain HTTP endpoint unless it is a loopback address or the operator sets `buildkite.plainHTTPEndpoint`. A dedicated token with an expiry and an IP allowlist (ADR-0008). |
| Controller to Orchard | The Orchard token, VM specifications | HTTPS, with the certificate authority that the operator gives. An account with the compute roles only. The client is code of this project, generated from the schema (ADR-0010). |
| Operator to controller | The configuration file and the environment | Strict decoding refuses an unknown key, so a secret that someone adds to the file stops the controller. Secrets come only from the environment. The controller refuses a configuration that is not valid, with a message that names the key. |
| Source to release | Commits, the build, and the artifacts | Signed commits and pull requests on `main`. Two approvals in the `release` environment. Keyless cosign signatures and SLSA provenance that name the workflow, the tag, and the commit (ADR-0013). `scripts/verify-release.sh` checks them, and that the commit is on `main`. |

## 4. Secure design principles

| Principle | How the design applies it |
|---|---|
| Economy of mechanism | The controller keeps no state. The Matcher, the Admission function, and the Reaper decision function are pure functions with an injected clock. |
| Fail-safe defaults | HTTPS only. A job VM is denied the host and the LAN unless a profile permits a CIDR. A configuration that is not valid stops the controller. An unmatched job is left alone. |
| Complete mediation | Every call to Buildkite and Orchard carries its credential, and the platforms check it. The Reaper acts only on VMs with the name prefix of its own controller. pf checks every connection of a VM. |
| Open design | The code and the design are public. Security rests on tokens and keyless signatures, not on secrecy of the design. |
| Separation of privilege | A release needs a merge to `main` and two separate approvals. The publish job verifies the signatures and the attestations before it publishes. The tools that build a release run in a job without the signing identity. |
| Least privilege | A dedicated token for one queue. An Orchard account with the compute roles only. A nonroot distroless image with a read-only root file system, no capabilities, and the default seccomp profile. |
| Least common mechanism | Each job gets a new VM, which no other job shares. Controllers that share an Orchard use disjoint worker pools (target design section 11). |
| Psychological acceptability | One configuration file with secure defaults. A one-command quick start. A refusal says which key is wrong and why. |
| Limited attack surface | No inbound API, only a gRPC health port with limits on streams, message size, and pings. A VM cannot reach the controller or the host. |
| Input validation with allowlists | Strict decoding of the configuration. Identifiers that match a fixed pattern. Agent tags that match a configured profile. |

## 5. Common weaknesses countered

| Weakness | Counter in the project |
|---|---|
| Improper input validation (CWE-20) | Strict decoding and validation of the configuration. Allowlist matching of agent tags. Fuzz tests for each input from outside the controller, which run every night. |
| Credentials in code, configuration, or logs (CWE-798, CWE-532) | No secret in the repository or in the configuration file. Secret scanning and push protection. No token in a VM specification (I-3) or in a metric attribute (I-7). The host playbook hides tokens from its output. |
| Cleartext transmission and certificate validation (CWE-319, CWE-295) | TLS 1.2 or later, certificate verification by default, and no plain HTTP except on loopback or by an explicit option. |
| Uncontrolled resource consumption (CWE-400) | A time limit of 10 seconds for each call to an API. Limits on the health server. A maximum VM lifetime (`maxVMLifetime`). Admission that never schedules more VMs than free slots (I-2). The Reaper deletes each VM that no job needs (I-6). |
| Race conditions (CWE-362) | The race detector in all tests. A property-based component suite over random event sequences (T-020). |
| Execution with unnecessary privileges (CWE-250) | The nonroot image, the security context of the chart, and the least-privilege accounts above. |
| Memory safety errors | Go, built with `CGO_ENABLED=0`, and no `unsafe` code. |
| Vulnerable or tampered dependencies (CWE-1395) | `go.sum`, `mise.lock` with checksums, and actions pinned by commit. govulncheck on every change. Renovate updates. The GitHub token of the upstream watcher goes only to `api.github.com`. |
| Defects that static analysis finds | golangci-lint with gosec, CodeQL, and govulncheck on every pull request. Code scanning blocks a merge with an open alert. |

## 6. Evidence and review

The gates of the [test strategy](test-strategy.md) check the invariants I-1 to I-9 on every change,
and the release gate T-100 to T-105 runs before every release. The real-VM tests of
[testing on real VMs](../testing-on-real-vms.md) check the network isolation and the host
playbook on a Mac.

Review this case when one of these changes: a new credential, a new transport between the
controller and a VM, a new inbound interface, a change of the trust model in target design section
9.1, or a new accepted risk.
