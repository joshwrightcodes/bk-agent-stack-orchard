# Writing guide

This guide tells you how to write the documents of this project. It is based on the
[Kubernetes documentation style guide](https://kubernetes.io/docs/contribute/style/style-guide/)
and the [Google developer documentation style guide](https://developers.google.com/style). Where
this guide says nothing, follow the Google guide.

## Which rules apply where

| Documents | Reader | Person | RFC 2119 key words |
|---|---|---|---|
| `README.md`, `docs/user/` (the runbooks included), the READMEs of `deploy/` and `images/` | An operator | "You" | No. Use imperatives: "Set `queue`." |
| `CONTRIBUTING.md`, `MAINTAINERS.md`, `docs/development.md`, `docs/testing-on-real-vms.md`, `docs/releasing.md`, this guide | A contributor | "You" | No |
| `docs/product/`, `docs/architecture/`, `docs/quality/`, `docs/delivery/`, `docs/adrs/`, the policy in `SECURITY.md` | A reviewer of the design | Third person | Yes, in capitals, for obligations only |

A document that uses the key words starts with the BCP 14 paragraph:

> The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
> "RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
> interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
> all capitals, as shown here.

Use a key word only for a real obligation. In other sentences, rewrite the sentence rather than
use "must" in lower case.

## Language

| Rule | Do | Don't |
|---|---|---|
| Use the present tense. | The controller deletes the VM. | The controller will delete the VM. |
| Use the active voice. | Orchard schedules the VM. | The VM is scheduled by Orchard. |
| Address the reader as "you" in guides. Never use "we". | You can run more controllers. | We recommend running more controllers. |
| Use simple, direct words. | To verify a release, … | In order to verify a release, … |
| Put the condition before the instruction. | If Orchard uses a private CA, set `orchardCA`. | Set `orchardCA` if Orchard uses a private CA. |
| Write one instruction in each sentence. | Create the Secret. Then install the chart. | Create the Secret and then install the chart, making sure that … |
| Keep sentences short. Split a sentence that has more than one idea. | The job stays in the queue. Another runner can take it. | The job stays in the queue, where another runner, if one exists, can take it instead. |
| Use one term for one thing, as the [glossary](glossary.md) defines it. | job VM | job VM, runner VM, and build machine in one page |
| Do not use Latin abbreviations. | for example, that is | e.g., i.e., etc. |
| Do not use words that assume knowledge. | Run `mise run check`. | Just run `mise run check`. It is easy. |
| Do not use idioms or jargon. | The controller stops. | The controller falls over. |
| Do not write about the future in guides. Plans go in issues. | The controller supports one queue. | Support for more queues is coming soon. |
| Name a role, not a person. | The maintainers merge it. | Alex merges it. |

## Facts that change

* In the design documents, a fact that can change, such as a version, a price, or a vendor term,
  has the date it was checked and a source that a reader can follow.
* In the design documents, an unknown value is `UNKNOWN`, with an owner and a trigger. It is
  never a silent guess.
* In guides, state the current fact without a date. A guide that needs a dated record links to
  the design document that holds it.

## Identifiers

The design documents use identifiers such as `PR-001`, `T-100`, and `ADR-0006`. The
[glossary](glossary.md) lists each family.

* Use identifiers in the design documents, in tests, and in commit footers.
* Do not use them in guides. Describe the behaviour instead. A reader of a guide cannot resolve
  an identifier.
* Do not use them in production code, configuration, or task descriptions, as
  [CONTRIBUTING.md](../CONTRIBUTING.md) says.

## Format

* Lines wrap at 100 characters, except table rows, code blocks, and the text of a URL.
* Headings use sentence case: "Install the controller", not "Install The Controller".
* Use numbered lists for steps, and bulleted lists for other lists.
* Put code, commands, file names, configuration keys, and environment variables in code font.
* Link text says where the link goes: "[Install the controller](user/install.md)", not
  "[here](user/install.md)".
* Write dates as `YYYY-MM-DD`.

## Generated pages

`docs/user/configuration.md` is written from the comments of the configuration types. Do not edit
it. Change the first paragraph of the comment of a field, which is the text that an operator
reads, and run `mise run docs-reference`.

## Checks

`mise run check-docs` checks the line width, that each link resolves, that each cited identifier
exists, and that the glossary lists each identifier family. A reviewer checks the rest of this
guide.
