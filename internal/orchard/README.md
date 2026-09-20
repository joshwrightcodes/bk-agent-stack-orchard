# The Orchard client

This client belongs to the project. Nothing generates it, and this file says why, what it
covers, how the project notices when the contract moves, and how to change it.

## Why the project writes it

Orchard carries the FSL-1.1-ALv2 licence, which permits every purpose except a competing use.
This project publishes a container image under the AGPL-3.0, which forbids a further restriction
on a distributed work. A binary that imports the Orchard Go client, or that carries code which a
generator produced from the Orchard OpenAPI file, cannot be distributed under the AGPL-3.0. The
decision record `docs/adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md` holds the
reasoning.

## Why generation would not help even without the licence

The published OpenAPI description of Orchard is neither complete nor current. At version 0.57.0:

* it has no `randomSerial`, `createdAt`, `last_seen`, or `scheduling_paused`, and the controller
  reads or writes all four;
* its paths omit the `/v1` prefix that the server uses;
* it describes the status message of a machine as `status_message` in the state schema, while the
  server marshals the same field from a Go tag of the same name — the two agree here, but the
  description gives a reader no way to know which one the server follows.

The Go types are what the server marshals, so they are the contract.

## What the client covers

| Call | Endpoint | Used by |
|---|---|---|
| Create a machine | `POST /v1/vms` | the launcher |
| List the machines | `GET /v1/vms` | the inventory |
| Delete a machine | `DELETE /v1/vms/{name}` | the reaper |
| List the workers | `GET /v1/workers` | admission |
| Read the events of a machine | `GET /v1/vms/{name}/events` | the acquire timeout |

Nothing else of the API is here. A new call needs a new method, a new entry in
`scripts/check-orchard-contract.py`, and a test.

## How the project notices that the contract moved

Three layers, from cheapest to strongest:

1. **The weekly upstream watch** reports an Orchard release later than the pinned version. A
   version that moves is the trigger to read the release notes.
2. **`mise run check-orchard-contract`** compares every field that this client uses with the Go
   tags of the pinned version, and follows an embedded structure. It fails when a field that the
   client depends on disappears or changes its name. It reads the upstream source and writes no
   upstream file into this repository.
3. **The integration tests** run against a real Orchard with synthetic workers at the pinned
   version. They are the only layer that proves behaviour, and a field that the API rejects fails
   the gate.

## How to change the client

1. Read the release notes of the new Orchard version.
2. Run `mise run check-orchard-contract --version <new version>` to see which fields moved.
3. Change the client and the list in `scripts/check-orchard-contract.py` together.
4. Run the integration tests against the new version.
5. Change the minimum version in the target design, section 9.2, and the driver in the system
   design baseline.

## If Orchard publishes a client or a specification under a permissive licence

That removes the reason for this client. The change needs a new decision record that supersedes
ADR-0010, and the client then becomes a generated package with a pinned specification.
