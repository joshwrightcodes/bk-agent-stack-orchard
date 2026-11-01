---
status: proposed
date: 2026-11-01
decision-makers: Josh Wright
supersedes: ADR-0011
---

# ADR-0015: Generate only the Orchard fields that the controller uses

## Summary

ADR-0011 decided to generate the Orchard wire types from the Orchard Go source at the pinned
version. The generated file copied every field of four Orchard types, and it carried no notice of
its source. At the same time, the contributing guide, the notice file, and the readme said that
the project never generates code from a file under the FSL. Those statements were false, and the
file copied more than the controller needs. The decision is to keep the generator, to limit it to
an allow list of the fields that the controller uses, and to write a header in the generated file
that names the source, the version, the commit, the licence, and the copyright notice of the
source. This ADR restates the whole decision of ADR-0011 with these two changes, and it supersedes
ADR-0011. The cost is a list that a person keeps current when the controller reads a new field.

## Context

The controller calls the Orchard REST API, and it imports no Orchard package. ADR-0010 keeps the
Orchard Go client out of the binary. Orchard carries the FSL-1.1-ALv2 licence (copyright 2023-2026
OpenAI, checked on 2026-11-01 in the LICENSE file of the pinned commit), which has a field-of-use
restriction. This project publishes an image under the AGPL-3.0-or-later, which forbids a further
restriction on a distributed work.

ADR-0010 forbade generating the client from an Orchard file. A client that a person wrote by hand
then had three problems, which ADR-0011 recorded and which still hold:

* The handwritten client was wrong. It read the status message of a machine as `statusMessage`,
  and the server marshals `status_message`. The unit test did not catch it, because the same
  person wrote the fake server and the client.
* A check needs a description of the contract, and a description that a person maintains is a
  second place to forget.
* The Go types, not the published description, are the contract. The OpenAPI file of Orchard
  0.58.0 omits `randomSerial`, `createdAt`, `last_seen`, and `scheduling_paused`, which the
  controller uses, and its paths leave out the `/v1` prefix that the server serves.

A review on 2026-11-01 found two faults in the state after ADR-0011:

* The generated file `internal/orchard/types_gen.go` came from Orchard source, but
  `CONTRIBUTING.md`, `NOTICE`, `README.md`, and `internal/orchard/README.md` still said that the
  project never generates code from a file under the FSL.
* The file carried 69 fields of four Orchard types, and the controller used 37 of them. The
  interoperability exception of Australian copyright law covers copying to the extent that the
  copying is necessary to make an independently created program work with another program. A
  field that the controller does not read is not necessary for that purpose.

The position of this project is that a field name, a JSON tag, and a type, for a field that the
controller sends or reads, are copied only to the extent that interoperability needs. That
position is the view of the project. It is not legal advice, and a lawyer has not confirmed it.
The owner MUST ask a lawyer to confirm it before the project relies on it for a release.

## Goals and non-goals

Goals:

* **The project learns when the contract moves.** A change upstream fails a gate, and the gate
  names the field.
* **One description of the contract.** No list that a person keeps current beside the code, other
  than the list of the fields that the controller uses.
* **The copy stays within what interoperability needs.** The generated file holds only the fields
  that the controller uses, and the named types that those fields need.
* **A reader sees the source and the terms.** The file states where its content comes from, under
  which licence, and who holds the copyright.
* **The binary contains no Orchard code.** The project imports no Orchard package.

Non-goals:

* The behaviour of the client: the retries, the errors, and the decisions about a conflict stay
  handwritten.
* A client for the whole API.
* The Orchard Go client as a dependency, which ADR-0010 settled.
* A legal opinion. This ADR records a position that a lawyer is to confirm.

## Options considered

**Keep the handwritten client and a handwritten list.** No tooling and no copy of Orchard names
beyond the fields in use. However, the list is a second place to forget, and the project already
lost a field name this way.

**Keep the handwritten client and read the list by reflection.** A test compares the fields of
the client with the upstream types. However, it checks only what the client already has, and a
reader cannot see the shape of the API.

**Generate every field of the four types (ADR-0011).** The file shows the whole shape, and every
upstream change appears in the difference. However, the file copies fields that the controller
does not read, which is more than interoperability needs, and it had no notice of its source.

**Generate only the fields on an allow list, with a header.** The generator writes the fields
that the controller reads or sends, and the named types that those fields need. The header names
the source and its terms. The generator fails when a name on the list no longer exists upstream,
so a rename still fails the gate. However, a person adds a field to the list when the controller
starts to use it.

**Generate from the OpenAPI description.** The description omits fields that the controller
uses, so the result would not work.

| Goal | Handwritten list | Reflection | All fields (ADR-0011) | Allow list and header | Description |
|---|---|---|---|---|---|
| Learns when the contract moves | Partly | Partly | Yes | Yes | Partly |
| One description of the contract | No | Yes | Yes | Yes, with one short list | Yes |
| Copy limited to what is necessary | Yes | Yes | No | Yes | No |
| Reader sees the source and the terms | Not applicable | Not applicable | No | Yes | No |
| Works with the API as it is | Yes | Yes | Yes | Yes | No |

## Decision

Generate the Orchard wire types from the Orchard resource types at the pinned version, and write
only the fields that the controller uses.

* `tools/orchardgen` reads the upstream types with the Go parser and writes
  `internal/orchard/types_gen.go`. `go generate ./internal/orchard/` runs it.
* The generator MUST write only the fields on an allow list. The allow list names the Go field
  names of each upstream type. A field is on the list when production code or a test in
  `internal/` reads or sets it, or when the server needs it in a request that the controller
  sends. The generator MUST fail when a name on the list is not a field of the upstream type.
* The generated file carries the field names, the JSON tags, and a type for each of those fields.
  It carries no behaviour of Orchard. A field whose type this project does not read carries
  `json.RawMessage`.
* The generated file MUST carry a header. The header names the source repository, the version,
  the commit, the FSL-1.1-ALv2 licence, and the copyright notice of the source.
* The version comes from the target design, and the `-ref` of the `go:generate` directive carries
  the commit. The generator stops when the tag no longer points at that commit. `-version` checks
  a candidate release before a document pins it.
* CI runs `go generate ./...` and then a difference check. A change upstream fails the gate.
* The behaviour of the client stays handwritten.
* The project never imports Orchard code, and it never copies other code that is under the FSL.
* A change that makes the controller read a new Orchard field adds that field to the allow list
  in the same change.

This decision replaces ADR-0011, and it replaces the sentence of ADR-0010 that forbids generating
from an Orchard file. The rest of ADR-0010 holds: no Orchard module in `go.mod`, no `replace`
directive, and no file of the Orchard repository in this repository.

## Consequences

Good:

* The generated file copies less, and the amount of the copy matches the need.
* A reader sees the source, the version, the commit, the licence, and the copyright notice at the
  top of the file.
* A field that upstream renames or removes still fails a gate, with the old name in the message.
* The statements of the contributing guide, the notice file, and the readmes match the code.

Bad:

* A person keeps the allow list current. A field that the controller starts to use, and that is
  missing from the list, fails to compile, so the omission is visible.
* The licence position is the view of the project, and a lawyer has not confirmed it. If a lawyer
  disagrees, the project MUST stop generating from Orchard source and return to a handwritten
  client.
* The generator needs the network and a GitHub token for its rate limit.

Review trigger:

* Review this decision when the owner receives the advice of a lawyer.
* Review it if Orchard publishes its client or its description under a permissive licence.
* Review it if the upstream types move to a package that the generator cannot read.

## Confirmation

* A test of the generator confirms that a field that is not on the allow list is not written.
* A test of the generator confirms that the header names the source repository, the version, the
  licence, and the copyright notice.
* A test of the generator confirms that the generator fails when a name on the allow list is not
  an upstream field.
* CI runs `go generate ./...` and fails when a generated file is different from the file in the
  tree.
* The integration tests against a real Orchard confirm the behaviour, which no generator can.
