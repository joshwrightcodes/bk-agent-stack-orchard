---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0008: Use a dedicated cluster agent token with an expiry and an IP allowlist

## Summary

The controller needs a cluster agent token for the Buildkite Stacks API. This token also limits
where job VM agents can register. The decision is a static token that only this controller uses,
with an expiry of 90 days or less and an IP allowlist. The cost is a static secret and a manual
replacement procedure.

## Context

The controller from ADR-0001 calls the Buildkite Stacks API, and the Stacks API accepts only a
cluster agent token. The token gives access to all self-hosted queues of its cluster. The cluster
can contain other queues (ADR-0007), so a leaked token reaches them. A leaked token also lets an
attacker register agents and take macOS jobs. The token must never go into a job VM (ADR-0004).

The token also affects each job VM. A job VM agent registers with a job acquisition token that the
controller issues. At that registration, Buildkite applies the expiry and the IP allowlist of the
agent token that issued the job acquisition token.

As of 2026-09-13, Buildkite OAuth Token Exchange issues short-lived tokens for the REST API and the
GraphQL API only. A REST API token with the `write_clusters` scope can create cluster agent tokens.
The controller can run on different platforms, so the token must not depend on one secret store.

## Goals and non-goals

Goals:

* **Small blast radius.** A leaked credential is useful for a short time, from few addresses
  (SE-03).
* **Least privilege.** The controller holds no privilege that it does not need (SE-03).
* **Reliable job registration.** Agents in job VMs can always register with their job acquisition
  tokens (PR-023).
* **Portability.** The solution does not depend on a specific platform or secret store (PR-022,
  OP-04).
* **Simple operation.** The solution is simple to operate (OP-01).
* **Reversibility.** A later change of the credential model needs no change to jobs or pipelines.

Non-goals:

* The credential for the VM orchestrator.
* Automation of the token replacement.

## Options considered

**A static, dedicated token with an expiry and an IP allowlist.** A person creates a cluster agent
token for this controller only, with `expires_at` and `allowed_ip_addresses`. The platform supplies
the token to the controller through an environment variable. The controller needs no REST API
scopes, and the allowlist can include the addresses of the job VMs. However, the token lives for up
to 90 days, and a person must replace it.

**A token that the controller creates through OAuth Token Exchange.** The controller uses OAuth
Token Exchange as a bot user to get a REST API token with the `write_clusters` scope. With that
token, it creates a cluster agent token that expires after 24 hours, and it replaces the token
before expiry. Each cluster agent token lives for only 24 hours. However, the `write_clusters`
scope can change queues and revoke other tokens. Tokens that job VMs still need must stay valid
during each replacement. The controller still stores a signing key and its orchestrator token, so
the number of stored secrets does not decrease. The controller also needs code to create, replace,
and revoke tokens.

| Goal | Static, dedicated token | Token created through OAuth Token Exchange |
|---|---|---|
| Small blast radius | Partly | Partly |
| Least privilege | Yes | No |
| Reliable job registration | Yes | Partly |
| Portability | Yes | — |
| Simple operation | Yes | No |
| Reversibility | Yes | Partly |

"—" shows that this ADR does not assess the option against the goal.

## Decision

Use a static, dedicated token with an expiry and an IP allowlist. The alternative needs a broader
privilege, and it does not decrease the number of stored secrets.

Only this controller uses the token. Its expiry MUST be 90 days or less. Its allowlist MUST contain
the egress addresses of the controller and of the job VMs, because job registration applies this
allowlist. The platform supplies the token to the controller through an environment variable. The
[target design](../architecture/target-design.md) gives the replacement procedure.

## Consequences

Good:

* A leaked token does not operate outside the allowlist, and it expires.
* The controller does not hold the `write_clusters` scope.
* Any platform can supply an environment variable.

Bad:

* A static secret with a life of up to 90 days exists.
* A leaked token reaches the other self-hosted queues of the cluster (ADR-0007).
* Token replacement is a manual procedure, or needs external automation.
* The allowlist must change when the egress addresses of the controller or the hosts change.

Review trigger:

* Review this decision if Buildkite adds a short-lived credential type for the Stacks API.

## Confirmation

* An integration test confirms that the controller fails its readiness check when the Stacks API
  returns HTTP 401.
* Before the controller goes live, a review of the token in Buildkite confirms that `expires_at` is
  90 days or less. The review also confirms that `allowed_ip_addresses` contains the egress
  addresses of the controller and of the job VMs.
