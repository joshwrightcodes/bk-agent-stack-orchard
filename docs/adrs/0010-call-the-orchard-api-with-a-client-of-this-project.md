---
status: accepted
date: 2026-09-19
decision-makers: Josh Wright
---

# ADR-0010: Call the Orchard API with a client of this project

## Summary

The controller talks to Orchard through its REST API. ADR-0005 selected the Orchard Go client
package for this work. That package carries the FSL-1.1-ALv2 licence, which permits every use
except a competing use. The project publishes its own source under the AGPL-3.0, which forbids a
further restriction on a distributed work. The decision is a small client in this project that
calls the Orchard REST API. The cost is the work to write and to maintain that client.

## Context

The fleet runs Orchard and Tart. This does not change: ADR-0005 selected them, and the controller
still needs them. The question is only how the controller code reaches the Orchard API.

The Orchard Go client is a package in the Orchard repository. A binary that imports it contains
Orchard code. The FSL-1.1-ALv2 grant of that code covers "any purpose other than a Competing Use",
which is a restriction on the field of use. The AGPL-3.0 states that a distributed work must not
carry a further restriction. Because of this, an image or a binary of this project cannot contain
both this project under the AGPL-3.0 and the Orchard client under the FSL.

The project publishes a container image (target design, section 15), so this is a real conflict,
not a theoretical one. Three facts limit the answer:

* The controller uses a small part of the Orchard API: create a VM, list VMs, delete a VM, list
  workers, and read the events of a VM.
* Orchard publishes an OpenAPI description of its API in its repository. That file is also under
  the FSL, so code that a generator produces from the file can carry the same restriction. The
  project therefore does not copy the file, and it does not generate code from it.
* An interface is not an implementation. A client that this project writes, from the public
  behaviour of the API, contains no Orchard code.

The controller and Orchard stay separate programs that talk over HTTP. The fleet operator runs
Orchard for an internal purpose, which the FSL permits.

## Goals and non-goals

Goals:

* **One licence for the distributed work.** An image or a binary of this project carries only the
  licence of this project and of its permissive dependencies (PR-022).
* **Same behaviour.** The controller uses the same endpoints and the same fields, and the
  integration evidence against a real Orchard does not weaken.
* **Small surface.** The client covers only the calls of the design, not the whole API.
* **Visible drift.** A change in the Orchard API fails a gate, not a production job (CO-02).

Non-goals:

* The selection of Orchard and Tart, which ADR-0005 owns.
* The way that the fleet operator installs and runs Orchard.
* A client for the whole Orchard API, or a client for other projects to use.

## Options considered

**Import the Orchard Go client.** This is the mechanism that ADR-0005 assumed. It needs no client
work, and it follows upstream changes without effort. However, the Orchard module path is still
`github.com/cirruslabs/orchard`, so `go.mod` needs a `replace` directive, and the repository has
no semver tag, so the directive needs a pseudo-version. More important, the binary then contains
FSL code, so the project cannot distribute it under the AGPL-3.0.

**Generate a client from the Orchard OpenAPI file.** A generator such as `oapi-codegen` produces
a complete client with little work. However, the OpenAPI file is a file of the Orchard repository
under the FSL. Generated code from that file is a derivative of it, so the licence problem stays.

**Write a client in this project.** The project writes a small HTTP client for the endpoints that
the design uses, from the public behaviour of the API. The binary then contains no Orchard code,
and `go.mod` needs no `replace` directive and no pseudo-version. However, the project owns this
code, and an Orchard API change needs a change here.

**Change the licence of this project to match Orchard.** A source-available licence such as the
FSL removes the conflict. However, the owner selected a licence that requires other parties to
publish their changes, and the FSL does not do this.

| Goal | Import the client | Generate from the file | Write a client | Match the Orchard licence |
|---|---|---|---|---|
| One licence for the distributed work | No | No | Yes | Yes |
| Same behaviour | Yes | Yes | Yes | Yes |
| Small surface | No | No | Yes | No |
| Visible drift | Partly | Partly | Yes | Partly |
| Keeps the licence intent of the owner | Yes | Yes | Yes | No |

## Decision

Write the Orchard client in this project.

The client MUST cover only these calls: create a VM, list VMs, delete a VM, list workers, and read
the events of a VM. It MUST NOT contain a file of the Orchard repository, and the project MUST NOT
generate it from an Orchard file. `go.mod` MUST NOT contain an Orchard dependency, and it MUST NOT
contain a `replace` directive for one.

The client MUST define its own types for the fields that the design uses. The integration tests
MUST run against a real Orchard controller with synthetic workers, so that a change of the API
fails a gate.

This decision replaces the dependency mechanism of ADR-0005 and its `go.mod` confirmation. It does
not change the selection of Tart and Orchard.

## Consequences

Good:

* An image or a binary of this project carries one licence, and the owner can publish the source
  under the AGPL-3.0.
* `go.mod` loses the `replace` directive and the pseudo-version, so the dependency graph becomes
  simple and a reader of `go.mod` sees the truth.
* The client covers five calls, so it is small enough to read in one sitting.
* The controller depends on the API of Orchard, not on its internal types. An upstream change of a
  Go type no longer breaks the build.

Bad:

* The project writes and maintains client code that upstream would otherwise give.
* An Orchard API change needs a change here. Only the integration tests detect it, so the
  integration gate becomes more important.
* The project cannot use the helper behaviour of the upstream client, such as its retry policy,
  and it writes that behaviour again.
* The controller still needs the Orchard server, and the fleet operator installs it under the
  FSL. The licence of the fleet does not change.

Review trigger:

* Review this decision if Orchard publishes a client under a permissive licence, or if the owner
  changes the licence of this project.

## Confirmation

* A CI check confirms that `go.mod` has no Orchard dependency and no `replace` directive.
* A CI check confirms that the repository contains no file of the Orchard repository.
* The integration tests create, list, and delete a VM against `orchard dev --synthetic` with the
  minimum Orchard version, and they fail when the API does not accept a field.
