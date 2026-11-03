# Runbook: retire and rotate the Orchard bootstrap admin

This runbook takes the bootstrap admin token out of the environment of the Orchard pod after the
first bootstrap, and it rotates the token. The `bootstrap-admin` account has all roles. Only the
bootstrap Job needs it. The Job runs again for each add, rotation, migration, or revocation of a
host, so the account and its token stay in the Secret `orchard-bootstrap`, which only the Job
mounts. While the account exists, the data volume of Orchard also holds the token (see "Why the
account stays"). To take it off the data volume, delete the account (see "Delete the account
completely").

**Rotation is your responsibility.** The project does not automate the rotation of these tokens.
This procedure is the way to do it by hand. You can automate it with the tools of your own
platform. Machine identity is out of the scope of this project. You can put a machine-identity
system in front of Orchard.

## Why the account stays

Orchard 0.58.0 does not delete `bootstrap-admin` when `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` is not set.
While the variable is set, each start makes the account again. The source and the date are in
`deploy/orchard/README.md`, section "First start and after the first bootstrap". The account
stays on purpose: a Job run for a host change needs it, and with the account in place the change
needs no restart of Orchard.

While the account exists, Orchard 0.58.0 keeps its token in its database, and at each start it
writes the token in plain text to `/data/orchard.yml`. Both are on the PVC of Orchard, so a person
who can run commands in the Orchard pod or read its PVC can read the token. A rotation reaches the
file at the next start of Orchard. The source and the date are in `deploy/orchard/README.md`, in
the same section.

## Access to the API

Each API command in this runbook uses the access path and the `orchard_admin` function of
`deploy/orchard/README.md`, section "Reach the API as an operator". Start the port-forward in
another terminal first (`kubectl -n orchard port-forward svc/orchard 16120:6120`). The NetworkPolicy
refuses a workstation, and the port-forward runs from the node of the pod, which the policy always
admits. The commands read the token from the Secret into the standard input of `curl`. No token goes
on an argv or into a file. The controller account cannot do these checks: it has no `admin:read`.
A check of a worker uses the `orchard_operator` function of the same section, because the rotated
bootstrap admin has no `compute` role.

## Procedure: retire

1. Confirm that the bootstrap Job completed and that the controller and the Mac hosts connect with
   their own accounts. `orchard_operator GET /v1/workers` lists each host as online.
2. Remove the component `deploy/orchard-overlays/first-start` from the overlay, which takes
   `ORCHARD_BOOTSTRAP_ADMIN_TOKEN` out of the StatefulSet `orchard`. Apply the overlay. Wait until
   the pod is ready.
3. Rotate the token (the section "Rotation" below), so the token of the first start does not stay.
4. Check that only the Job uses the Secret `orchard-bootstrap`, and that the environment of the
   pod has no token:

   ```sh
   kubectl -n orchard get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.volumes[*].secret.secretName}{"\t"}{.spec.containers[*].env[*].valueFrom.secretKeyRef.name}{"\n"}{end}' |
     grep orchard-bootstrap
   kubectl -n orchard get pod orchard-0 -o jsonpath='{.spec.containers[*].env[*].name}' |
     grep ORCHARD_BOOTSTRAP_ADMIN_TOKEN
   ```

   The first command shows only pods of the Job. The second prints nothing. If either shows more,
   stop and find the other user of the Secret.
5. Check the account: `orchard_admin GET /v1/service-accounts` lists `bootstrap-admin`.
6. Limit who can read the Secret. Give `get`, `list`, and `watch` on Secrets of the namespace
   `orchard` to few people only, and to no workload.

## Rotation

Rotate the token on a schedule of the operator, after a person who could read the Secret leaves,
and after any suspected exposure. This sets a new token through the API and then puts it in the
Secret. Keep the shell open until the last command succeeds, because the new token is only in
the shell variable until then. The new token gets only `admin:read` and `admin:write`, which the
Job needs (a restart with the variable set gives the account all roles again). The account then
cannot read or delete workers or VMs. The runbooks use the `operator` account for those calls.

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
    -o /dev/null -w 'HTTP %{http_code}\n' \
    --connect-to orchard.orchard.svc:6120:127.0.0.1:16120 \
    https://orchard.orchard.svc:6120/v1/service-accounts/bootstrap-admin
printf '{"stringData":{"bootstrap-admin-token":"%s"}}' "$new" |
  kubectl -n orchard patch secret orchard-bootstrap --type merge --patch-file /dev/stdin
unset new
```

The answer of Orchard holds the new token, so the command sends it to `/dev/null` and shows only the
HTTP status. Check that it is `HTTP 200`. Then run `orchard_admin GET /v1/service-accounts`, and
check that it lists `bootstrap-admin`. This call uses the new token from the Secret. Orchard refuses
the old token from the PUT on.

## Delete the account completely

If your fleet has no planned change of a host, you can delete the account. This also takes a
working admin token off the data volume of Orchard: the file `/data/orchard.yml` keeps the old
token, which Orchard then refuses. Do the steps 1 and 2 of the procedure. Then delete the
account, check that the token is refused, and delete the key:

```sh
orchard_admin DELETE /v1/service-accounts/bootstrap-admin
orchard_admin GET /v1/service-accounts
kubectl -n orchard patch secret orchard-bootstrap --type json \
  -p '[{"op":"remove","path":"/data/bootstrap-admin-token"}]'
```

Check that the second command ends with `HTTP 401`. Delete the key only after that check,
because Orchard needs no key for it. After the deletion, the next change of a host needs the
account back. To re-create it:

1. Put a new token in the key `bootstrap-admin-token` of the Secret `orchard-bootstrap`, and add
   the component `deploy/orchard-overlays/first-start` to the overlay again. The component reads
   that key.
2. Apply the overlay, which restarts Orchard. The API is out for a short time, and the workers and
   the controller connect again after it.
3. Run the bootstrap Job (delete it, apply the overlay) and wait until it completes.
4. Retire again with the steps 2 to 6 of the procedure.

## Warning: a start with an empty PVC can log an admin token

After the retirement, the loss of the PVC gives Orchard an empty database and no variable. Orchard
0.58.0 then makes `bootstrap-admin` with a random token that has all roles, and it prints the token
in its log. A log shipper copies the log. Treat the log of the Orchard pod as a secret.

* Prevention: before a start with an empty PVC, add the component
  `deploy/orchard-overlays/first-start` to the overlay again. It gives Orchard the token of the key
  `bootstrap-admin-token`. Orchard then uses the known token and logs `<hidden>`
  (`internal/command/controller/bootstrap.go`, tag `0.58.0`, read on 2026-11-02). Retire again
  after the Job completes.
* Recovery, if it already happened: set the account again with a new token (the rotation above).
  Purge the log lines where the logging system allows it.
