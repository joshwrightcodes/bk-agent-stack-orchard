# Runbook: Orchard worker accounts

This runbook adds a Mac host, rotates the token of one host, and revokes one host. Each Mac host
has its own Orchard service account, `worker-<host>`, and its own token (target design, section
9.2).

**Interim procedure.** People copy, rotate, and remove tokens by hand in this procedure. That is
not the target state. The owner decided on 2026-11-02 that only an engineer who sets or rotates a
key in the secret store on a schedule does a manual step, that the tokens on the Mac hosts rotate
automatically, and that each credential can be rotated on demand. The test strategy tracks the
work that replaces this procedure.

## What Orchard enforces

Orchard 0.58.0 does not bind an account to a worker name (source:
`internal/controller/api_workers.go` at the tag `0.58.0`, read on 2026-11-02). Any account with
`compute:write` can register, update, and delete any worker. A token for one host therefore stops
only that host when the operator revokes it. It does not stop another host, and it does not limit
what a thief of the token can do.
The bootstrap Job never deletes an account. Orchard keeps no mark that tells an account of the Job
from an account that a person made, so a Job that pruned accounts could delete a live host after a
typo in the list. Removal is a manual step.

## Access to the API and the Job

Each API call in this runbook uses the access path and the `orchard_admin` function of
`deploy/orchard/README.md`, section "Reach the API as an operator". The NetworkPolicy refuses a
workstation. Start `kubectl -n orchard port-forward svc/orchard 16120:6120` in another terminal. A
port-forward runs from the node of the pod, which the policy always admits. `orchard_admin` reads
the token of `bootstrap-admin` from the Secret `orchard-bootstrap` into the standard input of
`curl`. The Job needs that account, and it stays after the first bootstrap
(`docs/user/runbooks/orchard-bootstrap-admin.md`). Each change of a host below therefore runs the
Job again, and it needs no restart of Orchard. Do not use the controller account for these calls: it
has no `admin:read` or `admin:write`. To run the Job again, delete it and apply the overlay:
`kubectl -n orchard delete job orchard-bootstrap`, then apply the overlay.

To list the workers, run `orchard_admin GET /v1/workers`. To delete a worker record, run
`orchard_admin DELETE /v1/workers/<name>`. To list the accounts, run
`orchard_admin GET /v1/service-accounts`. If the operator deleted the bootstrap admin, re-create
it first (section "Delete the account completely" of the bootstrap admin runbook).

## Migrate from the shared account

Use this procedure for a fleet that still uses the shared account `worker` and the key
`worker-token` of the Secret `orchard-bootstrap`. The Job never deletes an account, so the shared
account stays valid, with `compute:write`, until the operator deletes it. The migration is
complete only after step 4.

1. Create the Secret `orchard-worker-tokens` with one key `worker-<host>` for each host, as in
   "Add a host", steps 1 and 2. Delete the Job `orchard-bootstrap`, apply the overlay, and wait
   until the Job completes. The shared account still works, so no host is out.
2. Move each host to its own token. Set `orchard_worker_name` and the new token for the host in
   the inventory, and run the playbook for that host. Check with `orchard_admin GET /v1/workers`
   that the worker is online. Do one host first. A worker name that was set from the host name
   before can differ from the new name. Then delete the old worker record if the name changed
   (`orchard_admin DELETE /v1/workers/<old name>`).
3. When all hosts are online with their own accounts, remove the key `worker-token` from the
   Secret `orchard-bootstrap` and the old shared token from the inventory vault, and then delete
   the shared account. The Job does not read `worker-token`, so this order is safe:

   ```sh
   orchard_admin DELETE /v1/service-accounts/worker
   ```

4. Check that `orchard_admin GET /v1/service-accounts` does not list `worker`.

## Add a host

1. Make a token: `openssl rand -hex 24`. Use only the characters `A-Z a-z 0-9
   . _ ~ + / = -` in the token, because the Job refuses any other.
2. Put it in the Secret `orchard-worker-tokens` as the key `worker-<host>`. `<host>` is a DNS
   label, and it is the `orchard_worker_name` of the host.
3. Put the same token in the inventory vault as `vault_orchard_worker_tokens[<host>]`.
4. Run the Job again (delete it and apply the overlay). Wait until the Job completes.
5. Run the playbook for the host. Check that the worker is online
   (`orchard_admin GET /v1/workers`).
6. Add `<host>` to the list of the alert "Unknown worker" (target design, section 10.6).

## Rotate the token of one host

1. Make a new token. Replace the key `worker-<host>` in the Secret, and the entry in the vault.
2. Run the Job again (delete it and apply the overlay). The Job sets the account again with the
   new token. From
   this point the old token fails, and the worker of the host fails to connect.
3. Run the playbook for that host at once. The role writes the new bootstrap token and restarts
   the worker.
4. Check that the worker is online again.

To limit the time that the host is out, do steps 2 and 3 one after the other without a pause. A
VM that runs on the host while it is out can fail, and the controller of this project handles the
failure like any other worker loss.

## Revoke one host

Use this procedure when a host is lost or stolen, or when the operator retires it.

1. Remove the key `worker-<host>` from the Secret `orchard-worker-tokens`, and the token from the
   inventory vault (`vault_orchard_worker_tokens`). Do this first, before any other step and
   before the next run of the Job: a key that stays in the Secret gives the account back at the
   next run.
2. Delete the account:

   ```sh
   orchard_admin DELETE /v1/service-accounts/worker-<host>
   ```

3. Delete the worker record: `orchard_admin DELETE /v1/workers/<host>`. The account deletion
   stops the host from connecting, and it does not remove the record. A worker record that stays
   shows as offline.
4. Confirm that the account is gone: `orchard_admin GET /v1/service-accounts` does not list
   `worker-<host>`, and `orchard_admin GET /v1/workers` does not list `<host>`. The worker process
   of the host fails its next call with HTTP 401, so a host that still runs stops reporting, and
   the record shows it offline after the offline timeout of Orchard (expected from the source of
   Orchard).
5. If the token is lost or stolen, also check the alert "Unknown worker" for the following days: a
   thief that has the token before step 2 could have registered other names. Delete such a worker
   record with step 3.
6. Remove `<host>` from the list of the alert "Unknown worker".

A thief that held the token can have read the job acquisition token of a VM that was running
(`compute:read` gives that read, target design section 9.2). Those tokens expire after 30 minutes
at most.
