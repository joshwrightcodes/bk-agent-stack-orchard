# The Orchard client

This client belongs to the project. The project generates its wire types from a pinned Orchard
version and writes its behaviour by hand. This file says why, what the client covers, how the
project notices when the contract moves, and how to change the client.

## Why the project owns it

Orchard carries the FSL-1.1-ALv2 licence, which permits every purpose except a competing use.
This project publishes a container image under the AGPL-3.0, which forbids a further restriction
on a distributed work. A binary cannot be distributed under the AGPL-3.0 when it imports the
Orchard Go client. The same is true when it carries code that a generator produced from the
Orchard OpenAPI file. The
decision record `docs/adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md` holds the
reasoning.

The project does copy one thing from the Orchard source: the field names, the JSON tags, and the
types of the fields that the controller uses, in `types_gen.go`. It copies them so that the
controller works with the Orchard API, and it copies no more than that. It never copies other code
that is under the FSL. This is the view of the project, and a lawyer has not yet confirmed it
(`ADR-0015`).

## What is generated, and what is not

`tools/orchardgen` writes `types_gen.go` from the Orchard resource types at the pinned commit,
and `go generate ./internal/orchard/` runs it. The `go:generate` directive carries that commit,
because a tag can move. The generator resolves the tag of the version that the design states. It
stops when the tag no longer points at the pinned commit. The generated file carries the field
names, the JSON tags, and a type for each field, and only for the fields on the allow list of the
generator (the `wanted` variable in `tools/orchardgen/main.go`). The allow list names the fields
that code or a test in `internal/` reads or sets. The generator fails when a name on the list is
not an upstream field. The file starts with a header that names the source repository, the
version, the commit, the FSL-1.1-ALv2 licence, and the copyright notice of OpenAI. A person writes
everything else by hand. This
includes the requests, the retries, the errors, and the decisions about a conflict or a machine
that is already gone.

The generator reads the Go types rather than the published OpenAPI description, because the Go
types are what the server marshals. At 0.58.0 that description omits `randomSerial`, `createdAt`,
`last_seen`, and `scheduling_paused`, which the controller uses. Its paths also leave out the
`/v1` prefix that the server serves. A client generated from it would not work.

`ADR-0015` records this decision, and it replaces `ADR-0011`. `ADR-0010` records why the Orchard
Go client stays out of the binary.

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
   read the difference. That is the list of fields that moved. The run needs no commit, because a
   candidate is a question rather than a pin.
3. Change the client where a moved field matters. A new type that the controller needs gets an
   entry in the `wanted` list of the generator. A field that the controller starts to read or set
   goes on the allow list of its type in the same change, and no other field does.
4. Run the integration tests against the new version.
5. Change the minimum version in the target design, section 9.2, and the driver in the system
   design baseline.
6. Change the `-ref` of the `go:generate` directive to the commit of the new version, which
   `gh api repos/openai/orchard/commits/<version> --jq .sha` gives.

## If Orchard publishes a client or a specification under a permissive licence

That removes the reason for this client. The change needs a new decision record that supersedes
ADR-0010 and ADR-0015, and the client then becomes their package with a pinned version.

## Running the generator

The generator reads only public data, and it does not use the GitHub API. It reads the tags of
Orchard from the reference list that git reads before a clone. It reads each source file at the
pinned commit from `raw.githubusercontent.com`. It needs no token, and it sends no token, even
when the environment holds one:

```sh
mise run generate
```
