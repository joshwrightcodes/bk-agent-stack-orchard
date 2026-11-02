# Runbook: Orchard worker accounts

This runbook adds a Mac host, rotates the token of one host, and revokes one host. Each Mac host
has its own Orchard service account, `worker-<host>`, and its own token (target design, section
9.2).

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## What Orchard enforces

Orchard 0.58.0 does not bind an account to a worker name (source:
`internal/controller/api_workers.go` at the tag `0.58.0`, read on 2026-11-02). Any account with
`compute:write` can register, update, and delete any worker. A token for one host therefore stops
only that host when the operator revokes it. It does not stop another host, and it does not limit
what a thief of the token can do.
The bootstrap Job never deletes an account. Orchard keeps no mark that tells an account of the Job
from an account that a person made, so a Job that pruned accounts could delete a live host after a
typo in the list. Removal is a manual step.

## Add a host

1. Make a token: `openssl rand -hex 24`. The token MUST NOT hold a character outside `A-Z a-z 0-9
   . _ ~ + / = -`, because the Job refuses it.
2. Put it in the Secret `orchard-worker-tokens` as the key `worker-<host>`. `<host>` MUST be a DNS
   label and MUST be the `orchard_worker_name` of the host.
3. Put the same token in the inventory vault as `vault_orchard_worker_tokens[<host>]`.
4. Delete the Job `orchard-bootstrap` and apply the overlay. Wait until the Job completes.
5. Run the playbook for the host. Check that the worker is online (`orchard list workers`).
6. Add `<host>` to the list of the alert "Unknown worker" (target design, section 10.6).

## Rotate the token of one host

1. Make a new token. Replace the key `worker-<host>` in the Secret, and the entry in the vault.
2. Delete the Job and apply the overlay. The Job sets the account again with the new token. From
   this point the old token fails, and the worker of the host fails to connect.
3. Run the playbook for that host at once. The role writes the new bootstrap token and restarts
   the worker.
4. Check that the worker is online again.

To limit the time that the host is out, do steps 2 and 3 one after the other without a pause. A
VM that runs on the host while it is out can fail, and the controller of this project handles the
failure like any other worker loss.

## Revoke one host

Use this procedure when a host is lost or stolen, or when the operator retires it.

1. Delete the account with an admin context: `orchard delete service-account worker-<host>`, or
   with the API. Give the credentials to `curl` on standard input, not on the argv:

   ```sh
   printf 'user = "bootstrap-admin:%s"\n' "$ADMIN_TOKEN" |
     curl -sS -K - --cacert ca.crt -X DELETE \
       https://<orchard address>:6120/v1/service-accounts/worker-<host>
   ```

   The bootstrap admin account is retired after the first bootstrap
   (`docs/runbooks/orchard-bootstrap-admin.md`). When it is retired, use another account with
   `admin:write`, or set the bootstrap admin up again for this step and retire it afterwards.
2. Remove the key `worker-<host>` from the Secret `orchard-worker-tokens`. Remove the token from
   the inventory vault (`vault_orchard_worker_tokens`). Do this before the next run of the Job: a
   key that stays in the Secret gives the account back.
3. Delete the worker record: `orchard delete worker <host>`, or
   `DELETE /v1/workers/<host>`. The account deletion stops the host from connecting, and it does
   not remove the record. A worker record that stays shows as offline.
4. Confirm that the worker is gone: `orchard list workers` does not list `<host>`, and
   `GET /v1/service-accounts` does not list `worker-<host>`. The worker process of the host fails
   its next call with HTTP 401, so a host that still runs stops reporting, and the record shows it
   offline after the offline timeout of Orchard (expected from the source, to be confirmed on the
   first host, W-22).
5. If the token is lost or stolen, also check the alert "Unknown worker" for the following days: a
   thief that has the token before step 1 could have registered other names. Delete such a worker
   record with step 3.
6. Remove `<host>` from the list of the alert "Unknown worker".

A thief that held the token can have read the job acquisition token of a VM that was running
(`compute:read` gives that read, target design section 9.2). Those tokens expire after 30 minutes
at most.
