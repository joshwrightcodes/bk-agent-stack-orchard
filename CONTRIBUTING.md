# Contributing

This is a personal project with one maintainer. Issues and pull requests are welcome, and the
maintainer decides what the project accepts.

## Workflow

Work is tracked in the [project](https://github.com/users/joshwrightcodes/projects/1). Each item
is a work unit of the [implementation plan](docs/delivery/implementation-plan.md).

1. **Assign the item** to its owner before any work starts. An unassigned item is not in
   progress.
2. **Move it to In progress** when the work starts, and work on a branch named for the unit, for
   example `w-26-document-gate`.
3. **Open a pull request** that links the item, with `Closes #<number>` in its body. A unit that
   needs more than one pull request gets a sub-issue for each one, and the sub-issues carry the
   links.
4. **Move it to In review** when the pull request is ready for the maintainer.
5. **The maintainer merges**, which closes the item and moves it to Done. Only the maintainer
   merges.

Nothing goes to `main` directly. Every change arrives through a pull request, and the project
squashes it into one commit.

## Before code: the documents decide

The document set is the source of truth, and it has one direction:

```
product requirements  ->  decision records  ->  target design  ->  implementation plan
```

* A change of **behaviour** starts in [`docs/product/requirements.md`](docs/product/requirements.md)
  with a new `PR-nnn` identifier.
* A change of **approach** needs a decision record in [`docs/adrs/`](docs/adrs/README.md). One ADR
  holds one decision, it names its options, and it records the consequences that the project
  accepts.
* A change of **mechanism** goes in
  [`docs/architecture/target-design.md`](docs/architecture/target-design.md). The design cites the
  requirements that it implements. It does not take its authority from an ADR.
* A change to the **C4 model** goes in `docs/architecture/workspace.dsl` in the same commit, and
  `docs/architecture/render-c4-md.sh` regenerates `c4.md`.

A requirement never cites the design or an ADR as its source. Each document section names the
identifiers it implements, so the direction stays visible.

## Document conventions

* [ASD-STE100 Simplified Technical English](https://www.asd-ste100.org), in the third person.
* RFC 2119 and RFC 8174 key words, in capitals, for obligations only.
* Lines wrap at 100 columns, except table rows and code blocks.
* A fact that can change — a version, a price, or a vendor term — carries the date it was
  checked, and a source that a reader can follow.
* An unknown value is `UNKNOWN`, and it has an owner and a trigger. It is never a silent guess.

## Code

* **Identifiers stay in the documents.** Code, configuration, and task descriptions say what they
  do and why. They do not carry a work unit, a requirement, or a test identifier, because a
  reader of the code cannot resolve one. The traceability lives in the commit footers, the pull
  request, and the project item. A decision record is the exception: a rule that exists for a
  recorded decision may name that ADR, because the reason is the useful part.
* Go 1.27. `mise install` gives the pinned tool versions, and `mise run check` runs the gate.
* Test-driven development. Each change of behaviour starts with a failing test.
* The Matcher, the Admission function, and the Reaper decision function stay pure, with an
  injected clock. The suppliers stay behind narrow interfaces, so tests replace them with fakes.
* [`docs/quality/test-strategy.md`](docs/quality/test-strategy.md) decides the level of each test.
  A new test needs a unique defect signal; a test that repeats a cheaper test does not earn its
  place.
* Never write a second fake of the Buildkite Stacks API. The project uses the `FakeAgentServer`
  that Buildkite maintains.

## Commits and pull requests

One commit is one complete change, with its tests and its document updates.

Every commit message MUST follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>[optional scope][optional !]: <description>

[optional body]

[optional footers]
```

| Type | Use it for |
|---|---|
| `feat` | A new capability of the controller |
| `fix` | A correction of behaviour that was wrong |
| `docs` | The documents, the README, and the policies |
| `test` | Tests and test harnesses only |
| `refactor` | A change of structure with no change of behaviour |
| `perf` | A change that makes the controller faster or smaller |
| `build` | `go.mod`, `mise.toml`, GoReleaser, and the image |
| `ci` | The workflows and the gates |
| `chore` | Work that fits no other type, such as a dependency bump |
| `revert` | A revert of an earlier commit |

Rules for the subject line:

* The scope is the package or the document area, for example `reaper`, `admission`, `orchard`,
  `adr`, or `design`. The scope is optional.
* The description is imperative, lowercase, and without a full stop, for example
  `feat(reaper): delete the VM when the job is terminal`.
* The subject line has a maximum of 72 characters.

Rules for the rest of the message:

* The body says why the change is necessary, and it wraps at 72 characters.
* A commit from Dependabot keeps the type and the shape, but its subject may pass 72 characters,
  because the module path belongs in the subject.
* A breaking change has `!` before the colon, and a `BREAKING CHANGE:` footer that says what
  a user of the project must do.
* Footers name the source identifiers, for example `Refs: W-06, PR-001` and `Evidence: T-041`.

A pull request title follows the same rules, because the project squashes each pull request into
one commit. The pull request template asks for the work unit (`W-nn`), the requirements
(`PR-nnn`), and the evidence (`T-nnn`) that proves the change.

[`.github/`](.github) holds the templates and the owners. An issue uses one of the two forms: a
bug report for behaviour that differs from the target design, or a change request for behaviour
that the documents do not state yet.

CI checks every commit of a pull request with `scripts/check-commits.py`, and a message that does
not follow these rules fails the gate. Run `mise run check` before you open a pull request, which
also checks the documents and the C4 model.

## Licence of contributions

This project uses the [GNU AGPL-3.0-or-later](LICENSE). A contribution enters the project under
that licence, and a pull request states that the author has the right to give it.

Two rules protect the licence:

* Never copy code from a source with a field-of-use restriction, such as the FSL, into this
  repository, and never generate code from such a file.
* The controller calls the Orchard REST API with its own client. Never add an Orchard package to
  `go.mod`
  ([ADR-0010](docs/adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md)).

## Security

Never open a public issue for a vulnerability. [`SECURITY.md`](SECURITY.md) gives the process.
