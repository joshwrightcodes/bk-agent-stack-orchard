# The Orchard client

This client belongs to the project: its wire types are generated from a pinned Orchard version,
and its behaviour is written by hand. This file says why, what it covers, how the project notices
when the contract moves, and how to change it.

## Why the project owns it

Orchard carries the FSL-1.1-ALv2 licence, which permits every purpose except a competing use.
This project publishes a container image under the AGPL-3.0, which forbids a further restriction
on a distributed work. A binary that imports the Orchard Go client, or that carries code which a
generator produced from the Orchard OpenAPI file, cannot be distributed under the AGPL-3.0. The
decision record `docs/adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md` holds the
reasoning.

## What is generated, and what is not

`tools/orchardgen` writes `types_gen.go` from the Orchard resource types at the pinned version,
and `go generate ./internal/orchard/` runs it. The generated file carries the field names, the
JSON tags, and a type for each field. Everything else here is written by hand: the requests, the
retries, the errors, and the decisions about a conflict or a machine that is already gone.

The generator reads the Go types rather than the published OpenAPI description, because the Go
types are what the server marshals. At 0.57.0 that description omits `randomSerial`, `createdAt`,
`last_seen`, and `scheduling_paused`, which the controller uses, and its paths leave out the
`/v1` prefix that the server serves. A client generated from it would not work.

`ADR-0011` records this decision, and `ADR-0010` records why the Orchard Go client stays out of
the binary.

## What the client covers

| Call | Endpoint | Used by |
|---|---|---|
| Create a machine | `POST /v1/vms` | the launcher |
| List the machines | `GET /v1/vms` | the inventory |
| Delete a machine | `DELETE /v1/vms/{name}` | the reaper |
| List the workers | `GET /v1/workers` | admission |
| Read the events of a machine | `GET /v1/vms/{name}/events` | the acquire timeout |

Nothing else of the API is here. A new call needs a new method and a test of its own.

## How the project notices that the contract moved

Three layers, from cheapest to strongest:

1. **The weekly upstream watch** reports an Orchard release later than the pinned version. A
   version that moves is the trigger to read the release notes.
2. **`mise run check-generate`** writes the generated files again and fails when the result is
   different from the tree. A field that upstream renames, adds, or removes appears in that
   difference with its old name and its new one. CI runs it on every pull request.
3. **The integration tests** run against a real Orchard with synthetic workers at the pinned
   version. They are the only layer that proves behaviour, and a field that the API rejects fails
   the gate.

## How to change the client

1. Read the release notes of the new Orchard version.
2. Run `go run ./tools/orchardgen -version <new version> -out internal/orchard/types_gen.go` and
   read the difference. That is the list of fields that moved.
3. Change the client where a moved field matters. A new type that the controller needs gets an
   entry in the `wanted` list of the generator.
4. Run the integration tests against the new version.
5. Change the minimum version in the target design, section 9.2, and the driver in the system
   design baseline.

## If Orchard publishes a client or a specification under a permissive licence

That removes the reason for this client. The change needs a new decision record that supersedes
ADR-0010 and ADR-0011, and the client then becomes their package with a pinned version.

## Running the generator

The generator reads the upstream source through the GitHub API, which limits an anonymous caller
to sixty calls each hour. A local run therefore uses a token, and CI gives one to the job:

```sh
GITHUB_TOKEN=$(gh auth token) mise run generate
```
