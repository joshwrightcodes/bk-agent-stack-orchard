---
status: accepted
date: 2026-09-20
decision-makers: Josh Wright
---

# ADR-0011: Generate the Orchard wire types from the pinned version

## Summary

ADR-0010 gave the controller its own Orchard client, and it forbade generating that client from an
Orchard file. A client that a person writes by hand has no way to learn that the API changed, and
the check that the project wrote instead needed a second list that a person kept current. The
decision is to generate the wire types from the Orchard resource types of the pinned version, and
to make `go generate` with a difference check the way that the project learns about a change. The
client behaviour stays handwritten. The cost is a generated file in the repository whose shape
comes from another project.

## Context

The controller calls the Orchard REST API. ADR-0010 keeps the Orchard Go client out of the binary,
because Orchard carries a licence with a field-of-use restriction and this project publishes an
image under the AGPL-3.0. That decision holds, and this one does not change it.

ADR-0010 went further: it forbade generating the client from an Orchard file, because generated
code from a file carries the terms of that file. The result was a client that a person wrote from
reading the API, and three observations followed.

The first is that the handwritten client was wrong. It read the status message of a machine as
`statusMessage`, and the server marshals `status_message`. The unit test did not catch it, because
the same person wrote the fake server and the client, and both carried the same mistake. The
Reaper puts that message on the job page when a machine fails, so a failed job would have shown an
empty reason.

The second is that a check needs a description of the contract, and a description that a person
maintains is a second place to forget. The project wrote that list twice: once in a script, and
once as a test that read the client by reflection.

The third is that the Go types, not the published description, are the contract. The OpenAPI file
of Orchard 0.57.0 omits `randomSerial`, `createdAt`, `last_seen`, and `scheduling_paused`, which
the controller uses, and its paths leave out the `/v1` prefix that the server serves. A generator
that reads the description would therefore produce a client that does not work.

## Goals and non-goals

Goals:

* **The project learns when the contract moves.** A change upstream fails a gate, and the gate
  names the field.
* **One description of the contract.** No list that a person keeps current beside the code.
* **The distributed work carries one licence.** The binary contains no Orchard code.
* **A reader sees what the controller depends on.** The shape of the API is in the repository, not
  in the memory of the person who wrote the client.

Non-goals:

* The behaviour of the client: the retries, the errors, and the decisions about a conflict stay
  handwritten, because they are this project's behaviour and not Orchard's.
* A client for the whole API. The generator writes the four types that the controller uses.
* The Orchard Go client as a dependency, which ADR-0010 settled.

## Options considered

**Keep the handwritten client and a handwritten list.** The project had this. It needs no tooling.
However, the list is a second place to forget, and it already carried a mistake that a test could
not see.

**Keep the handwritten client and read the list by reflection.** A test reads the fields of the
client and compares them with the upstream types. This needs no list, and it was the state before
this decision. However, it can only check what the client already has: a field that upstream adds,
and that the controller should use, stays invisible, and a reader of the repository still cannot
see the shape of the API.

**Generate the wire types from the upstream Go types.** A generator reads the resource types at the
pinned version and writes the structures. `go generate ./...` with a difference check turns any
upstream change into a failed gate, and the generated file shows a reader the whole shape.
However, the file in the repository has a shape that another project decides, and the generator
needs the network.

**Generate from the OpenAPI description.** This is the usual answer for a REST API. However, the
description of Orchard omits fields that the controller uses, so the result would not work.

| Goal | Handwritten list | Reflection | Generate from the Go types | Generate from the description |
|---|---|---|---|---|
| Learns when the contract moves | Partly | Partly | Yes | Partly |
| One description of the contract | No | Yes | Yes | Yes |
| One licence for the distributed work | Yes | Yes | Yes | Yes |
| A reader sees the shape | No | No | Yes | Yes |
| Works with the API as it is | Yes | Yes | Yes | No |

## Decision

Generate the Orchard wire types from the Orchard resource types at the pinned version.

* `tools/orchardgen` reads the upstream types with the Go parser and writes
  `internal/orchard/types_gen.go`. `go generate ./internal/orchard/` runs it.
* The generated file carries the field names, the JSON tags, and a type for each field. It carries
  no behaviour of Orchard. A field whose type this project does not read carries
  `json.RawMessage`, so the shape stays complete and the content stays out.
* The version comes from the target design, so one document decides it, and `-version` checks a
  candidate release before a document pins it.
* CI runs `go generate ./...` and then a difference check. A change upstream fails the gate.
* The behaviour of the client stays handwritten: the retries, the errors, the conflict on create,
  and the machine that is already gone on delete.

This decision replaces the sentence of ADR-0010 that forbids generating from an Orchard file. The
rest of ADR-0010 holds: no Orchard module in `go.mod`, no `replace` directive, and no file of the
Orchard repository in this repository.

## Consequences

Good:

* A field that upstream renames fails a gate, with the old name and the new one in the difference.
* The repository shows the shape of the API that the controller talks to.
* One place describes the contract, and a person maintains none of it.
* A candidate Orchard release can be checked before the documents pin it.

Bad:

* The repository holds a file whose shape another project decides, and a reviewer must read a
  generated file when upstream moves.
* The generator needs the network and a GitHub token for its rate limit, so it cannot run in an
  environment without either.
* The licence position is narrower than before. The generated file carries the names and the types
  of an interface, and no implementation, which this project treats as a description of an
  interface rather than a copy of the software. A reader who disagrees would have to treat the
  file as covered by the upstream terms.
* An upstream field that the controller does not use still appears in the generated file, which
  makes the file longer than the need.

Review trigger:

* Review this decision if Orchard publishes its client or its description under a permissive
  licence, which would remove the reason for a generator of this project.
* Review it if the upstream types move to a package that the generator cannot read.

## Confirmation

* CI runs `go generate ./...` and fails when a generated file is different from the file in the
  tree.
* A test of the generator confirms that it flattens an embedded structure and that it resolves a
  named type.
* The integration tests against a real Orchard confirm the behaviour, which no generator can.
