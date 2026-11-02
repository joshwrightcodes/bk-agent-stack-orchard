# Runbook: retire the Orchard bootstrap admin

This runbook removes the `bootstrap-admin` account of Orchard after the first bootstrap, and it
sets the account again for a token rotation. The bootstrap admin has all roles. It is necessary
only while the bootstrap Job runs.

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## Why the account stays

Orchard 0.58.0 does not delete `bootstrap-admin` when `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` is not set.
While the variable is set, each start makes the account again. The source and the date are in
`deploy/orchard/README.md`, section "First start and after the first bootstrap".

## Procedure

1. Confirm that the bootstrap Job completed and that the controller and the Mac hosts connect with
   their own accounts.
2. Remove the `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` entry from the StatefulSet `orchard` in the overlay.
   Apply the overlay. Wait until the pod is ready.
3. Delete the account with an admin context, `orchard delete service-account bootstrap-admin`, or
   with `DELETE /v1/service-accounts/bootstrap-admin`. Give the credentials to `curl` on standard
   input (`curl -K -`), not on the argv.
4. Delete the key `bootstrap-admin-token` from the Secret `orchard-bootstrap`.
5. Confirm that `GET /v1/service-accounts` no longer lists `bootstrap-admin`.

## Rotation

To rotate a token, put a new `bootstrap-admin-token` in the Secret and set the variable on the
StatefulSet again. Restart Orchard, delete the Job, apply the overlay, and wait for the Job to
complete. Then do the procedure from step 2.
