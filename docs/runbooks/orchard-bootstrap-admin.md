# Runbook: retire and rotate the Orchard bootstrap admin

This runbook takes the bootstrap admin token out of the Orchard pod after the first bootstrap, and
it rotates the token. The `bootstrap-admin` account has all roles. Only the bootstrap Job needs it.
The Job runs again for each add, rotation, migration, or revocation of a host, so the account and
its token stay in the Secret `orchard-bootstrap`, and nothing else holds them.

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## Why the account stays

Orchard 0.58.0 does not delete `bootstrap-admin` when `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` is not set.
While the variable is set, each start makes the account again. The source and the date are in
`deploy/orchard/README.md`, section "First start and after the first bootstrap". The account
stays on purpose: a Job run for a host change needs it, and with the account in place the change
needs no restart of Orchard.

## Access to the API

Each API command in this runbook uses the access path and the `orchard_admin` function of
`deploy/orchard/README.md`, section "Reach the API as an operator". Start the port-forward in
another terminal first (`kubectl -n orchard port-forward svc/orchard 16120:6120`). The NetworkPolicy
refuses a workstation, and the port-forward runs from the node of the pod, which the policy always
admits. The commands read the token from the Secret into the standard input of `curl`. No token goes
on an argv or into a file. The controller account cannot do these checks: it has no `admin:read`.

## Procedure: retire

1. Confirm that the bootstrap Job completed and that the controller and the Mac hosts connect with
   their own accounts.
2. Remove the `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` entry from the StatefulSet `orchard` in the overlay
   (the patch is in the README). Apply the overlay. Wait until the pod is ready.
3. Rotate the token (the section "Rotation" below), so the token of the first start does not stay.
4. Check that only the Job uses the Secret `orchard-bootstrap`, and that the pod has no token:

   ```sh
   kubectl -n orchard get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.volumes[*].secret.secretName}{"\t"}{.spec.containers[*].env[*].valueFrom.secretKeyRef.name}{"\n"}{end}' |
     grep orchard-bootstrap
   kubectl -n orchard get pod orchard-0 -o jsonpath='{.spec.containers[*].env[*].name}' |
     grep ORCHARD_BOOTSTRAP_ADMIN_TOKEN
   ```

   The first command MUST show only pods of the Job. The second MUST print nothing.
5. Check the account: `orchard_admin GET /v1/service-accounts` lists `bootstrap-admin`.
6. Limit who can read the Secret. Kubernetes RBAC MUST give `get`, `list`, and `watch` on Secrets
   of the namespace `orchard` to few people only, and to no workload.

## Rotation

Rotate the token on a schedule of the operator, after a person who could read the Secret leaves,
and after any suspected exposure. This sets a new token through the API and then puts it in the
Secret. Keep the shell open until the last command succeeds, because the new token is only in
the shell variable until then. The new token gets only `admin:read` and `admin:write`, which the
Job needs (a restart with the variable set gives the account all roles again).

```sh
new="$(openssl rand -hex 24)"
kubectl -n orchard get secret orchard-bootstrap \
  -o go-template='{{index .data "bootstrap-admin-token" | base64decode}}{{"\n"}}' |
  {
    read -r old
    printf 'user = "bootstrap-admin:%s"\n' "$old"
    printf 'data = "{\\"name\\":\\"bootstrap-admin\\",\\"token\\":\\"%s\\",' "$new"
    printf '\\"roles\\":[\\"admin:read\\",\\"admin:write\\"]}"\n'
  } |
  curl -sS -K - --cacert ca.crt -X PUT -H 'Content-Type: application/json' \
    --connect-to orchard.orchard.svc:6120:127.0.0.1:16120 \
    https://orchard.orchard.svc:6120/v1/service-accounts/bootstrap-admin
printf '{"stringData":{"bootstrap-admin-token":"%s"}}' "$new" |
  kubectl -n orchard patch secret orchard-bootstrap --type merge --patch-file /dev/stdin
unset new
```

Then run `orchard_admin GET /v1/service-accounts`. It MUST list `bootstrap-admin`, and it uses
the new token from the Secret. The old token is refused from the PUT on.

## Delete the account completely

A fleet with no planned change of a host MAY delete the account. Do the steps 1 and 2 of the
procedure. Then delete the account, check that the token is refused, and delete the key:

```sh
orchard_admin DELETE /v1/service-accounts/bootstrap-admin
orchard_admin GET /v1/service-accounts
kubectl -n orchard patch secret orchard-bootstrap --type json \
  -p '[{"op":"remove","path":"/data/bootstrap-admin-token"}]'
```

The second command MUST fail with an HTTP 401 error. Delete the key only after that check,
because Orchard needs no key for it. After the deletion, the next change of a host needs the
account back. To re-create it:

1. Put `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` back in the StatefulSet from the key `bootstrap-admin-token`
   of the Secret `orchard-bootstrap`, and put a new token in that key.
2. Restart Orchard. The API is out for a short time, and the workers and the controller connect
   again after it.
3. Run the bootstrap Job (delete it, apply the overlay) and wait until it completes.
4. Retire again with the steps 2 to 6 of the procedure.

## Warning: a start with an empty PVC can log an admin token

After the retirement, the loss of the PVC gives Orchard an empty database and no variable. Orchard
0.58.0 then makes `bootstrap-admin` with a random token that has all roles, and it prints the token
in its log. A log shipper copies the log. Treat the log of the Orchard pod as a secret.

* Prevention: before a start with an empty PVC, put the variable back with the token of the key
  `bootstrap-admin-token`. Orchard then uses the known token and logs `<hidden>`
  (`internal/command/controller/bootstrap.go`, tag `0.58.0`, read on 2026-11-02). Retire again
  after the Job completes.
* Recovery, if it already happened: set the account again with a new token (the rotation above).
  Purge the log lines where the logging system allows it.
