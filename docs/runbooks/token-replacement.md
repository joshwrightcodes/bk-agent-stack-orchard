# Runbook: replace the cluster agent token

This runbook replaces the Buildkite cluster agent token of one controller. It follows the
procedure of the target design, section 9.2, and the token review of T-141.

Do this procedure:

* before the token expires. The `expires_at` of the token MUST be 90 days or less, so a
  replacement is necessary at least each 90 days;
* when the token could be known to a person or a system that must not have it;
* when the controller logs that Buildkite refused the agent token.

## Why the order is important

The cluster agent token issues a job acquisition token for each job VM. Buildkite applies the
restrictions of the cluster agent token when the agent in the VM registers with its job
acquisition token. When you revoke a cluster agent token, the job acquisition tokens that it
issued also stop.

Thus, if you revoke the old token while a VM from before the restart exists, the agent in that VM
cannot acquire its job, or it loses the job. Revoke the old token last.

The controller keeps no durable state. A restart does not change the result of a job (PR-010).
After the restart, the controller finds its VMs by name, and it extends every reservation at the
first pass.

## Before you start

1. Get the `allowed_ip_addresses` of the current token. The new token MUST have the same list.
2. Confirm that the list contains:
   * the egress addresses of the controller;
   * the egress addresses of the job VMs.

   Buildkite applies the list of the cluster agent token when an agent registers with a job
   acquisition token. If the list does not contain the egress addresses of the job VMs, no agent
   can acquire a job.
3. Confirm that the controller is ready: its gRPC `readiness` service serves. If it does not,
   correct the reason first. The log record `the controller is not ready` names each reason.

   The controller serves the gRPC health checking protocol on `healthAddress`. In the Kubernetes
   reference deployment, the readiness of the pod shows it. By hand:

   ```sh
   grpc-health-probe -addr=<controller>:8080 -service=readiness
   ```

## Procedure

### 1. Create the new token

In Buildkite, create a new agent token for the cluster of the queue of the controller:

* Set `allowed_ip_addresses` to the list from "Before you start".
* Set `expires_at` to 90 days or less from today.
* Give the token a description that names the controller and the date, for example
  `bk-agent-stack-orchard macos-prod 2026-10-01`.

Keep the old token. Do not revoke it yet.

### 2. Supply the new token and restart the controller

The controller reads the token from the environment variable
`BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN` when it starts. It does not read the token again
while it runs.

In the Kubernetes reference deployment:

1. Replace the value in the Secret that supplies the variable.
2. Restart the Deployment. The Deployment uses `strategy: Recreate`, so the old process stops
   before the new process starts, and only one controller serves the queue at a time.

Write down the time of the restart. Step 4 needs it.

### 3. Confirm that the controller uses the new token

1. Wait until the controller is ready. The sources answer in the first seconds after the start, so a
   controller that stays unready for more than one minute has a problem.
2. If the controller logs that Buildkite refused the agent token, the new token is not correct. Do
   the rollback below.
3. Confirm on the Buildkite queue that the controller reserves jobs and that their VMs start.

### 4. Wait for the VMs from before the restart

A VM that the controller created before the restart has a job acquisition token that the old
token issued. The old token MUST stay valid until each of these VMs is gone.

List the VMs of the controller in Orchard. Their names start with `bk-<controllerID>-`. Compare
the `createdAt` of each VM with the time of the restart. Wait until no VM has a `createdAt` before
the restart.

Most VMs go when their job ends. The `maxVMLifetime` (25 h by default) is the longest time that a
VM can exist, because the Reaper deletes a VM that is older (row 2 of the target design, section
7.2).

### 5. Revoke the old token

When no VM from before the restart exists, revoke the old token in Buildkite.

Confirm that the controller is still ready. A controller that is not ready after this step uses the
old token, which means that step 2 did not supply the new token.

## Rollback

If the controller with the new token is not ready because Buildkite refused the token:

1. Supply the old token again, in the same way as step 2, and restart the controller.
2. Confirm that the controller is ready.
3. Do not revoke the old token.
4. Find why Buildkite refused the new token, for example a token for a different cluster, and
   start again at step 1.

The old token stays valid until step 5, so a rollback is always possible before step 5.

## After the replacement

Record these values for the next review (T-141):

* the date of the replacement;
* the `expires_at` of the new token;
* the `allowed_ip_addresses` of the new token.

## Verification

Before the pilot, do this procedure one time against a Buildkite test cluster (W-18, W-24). Until
then, the tests of this repository confirm only the part of the procedure that the controller
controls: readiness fails, and the log names the token, when the Stacks API returns HTTP 401
(T-063).
