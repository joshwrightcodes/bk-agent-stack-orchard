# Contributing

This is a personal project with one maintainer. Issues and pull requests are welcome, and the
maintainer decides what the project accepts.

## Workflow

### Contribute a change

1. **Find or open an issue.** Search the
   [issues](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues) first. To open one,
   use a [form](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/new/choose): a bug
   report for behaviour that differs from the target design, or a change request for behaviour
   that the documents do not state yet. Never open a public issue for a vulnerability:
   [SECURITY.md](SECURITY.md) gives the private process.
2. **Agree the change before the code.** For more than a small correction, wait until the
   maintainer agrees in the issue, because the documents decide first (below).
3. **Fork the repository, and work on a branch.** Follow the rules below, and run
   `mise run check` before you open a pull request.
4. **Open a pull request** from your fork, with `Closes #<number>` in its body, and complete the
   template. The workflows of a first pull request from a fork start after the maintainer
   approves them.
5. **The maintainer reviews and merges.**

The maintainer answers issues and pull requests when time permits. This is a personal project,
so there is no fixed response time. A vulnerability report gets a first answer within one week.

### The maintainer's workflow

The maintainer tracks work in a GitHub project. Each item is a work unit of the
[implementation plan](docs/delivery/implementation-plan.md).

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

A pull request must be up to date with `main` before it merges, and a new push dismisses the
approvals that it had. A pull request with no activity for 30 days gets the `stale` label, and it
closes 14 days later unless somebody comments, pushes a commit, or removes the label. Dependency
pull requests and the release pull request are exempt.

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

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## Document conventions

* [ASD-STE100 Simplified Technical English](https://www.asd-ste100.org), in the third person.
* RFC 2119 and RFC 8174 key words, in capitals, for obligations only.
* Lines wrap at 100 columns, except table rows and code blocks. Only the text of a URL is exempt
  from the width check, because a reader cannot wrap it.
* A fact that can change — a version, a price, or a vendor term — carries the date it was
  checked, and a source that a reader can follow.
* An unknown value is `UNKNOWN`, and it has an owner and a trigger. It is never a silent guess.

## Code

* **Identifiers stay in the documents.** Production code, configuration, and task descriptions say
  what they do and why. They do not carry a work unit, a requirement, or a test identifier,
  because a reader of the code cannot resolve one. A test file and the comments in it can carry
  `T-nnn`, `I-n`, and `O-n`, because the tests are the evidence for those identifiers.
  The traceability lives in the commit footers, the pull request, and the project item. A decision
  record is the exception: a rule that exists for a recorded decision may name that ADR, because
  the reason is the useful part.
* Go 1.27. `mise install` gives the pinned tool versions, and `mise run check` runs the gate.
* Test-driven development. Each change of behaviour starts with a failing test.
* Code that reads input from outside the controller, such as the agent rules of a job, a machine
  name from Orchard, or the configuration file, has a fuzz test that states a property for any
  input. `go test` runs its seed inputs, and `mise run fuzz` fuzzes it.
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
| `ci` | The workflows, the gates, and the self-hosted runner |
| `chore` | Work that fits no other type, such as a dependency bump |
| `revert` | A revert of an earlier commit |

Rules for the subject line:

* The scope is the package or the document area, for example `reaper`, `admission`, `orchard`,
  `adr`, or `design`. The scope is optional.
* The description is imperative, lowercase, and without a full stop, for example
  `feat(reaper): delete the VM when the job is terminal`.
* The subject line has a maximum of 72 characters.
* A pull request title has a maximum of 65 characters, because GitHub adds " (#NNN)" to the
  subject when it squashes. The `title` job of the `documents` workflow checks it, and it runs
  again when the title changes. The job skips a pull request that a bot opens, because the
  subject of a bot can pass the limit (see the rules for the rest of the message).

Rules for the rest of the message:

* The body says why the change is necessary, and it wraps at 72 characters.
* A commit from Renovate or Dependabot keeps the type and the shape, but its subject may pass 72
  characters, because the module path belongs in the subject. A description from Dependabot
  starts with a capital letter ("Bump …"), because its configuration sets only the type and the
  scope.
* A change of a workflow or of the runner uses `ci:`. It MUST NOT use `fix(ci)` or `fix(runner)`,
  because release-please hides a commit by its type and not by its scope, so a `fix` of this kind
  would show in the release notes. `scripts/check-commits.py` rejects both subjects.
* A commit that fixes a vulnerability names its identifier (`GO-`, `GHSA-`, or `CVE-`) in the
  subject or in the body, so that the release notes and a reader of the history can find the
  advisory. Renovate has no template field for an advisory identifier (checked 2026-11-01 in the
  Renovate documentation of templates), so the maintainer adds it when a vulnerability update
  merges.
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

## Versioning and releases

The project uses [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). The binary,
the container image, and the Helm chart have the same version.
[release-please](https://github.com/googleapis/release-please) calculates each version from the
Conventional Commits on `main`
([ADR-0014](docs/adrs/0014-calculate-releases-with-release-please.md)).

* A release tag has the form `vMAJOR.MINOR.PATCH`, for example `v0.3.1`.
* The major version is 0. It MUST stay 0 until the maintainer decides that the project is stable.
  Only the maintainer starts version 1.0.0, and a pull request MUST NOT set a major version of 1
  or more.
* Every 0.x release is a GitHub pre-release until version 1.0.0, and it is not the latest release.
  `scripts/release-finish.sh` sets the flag from the tag when it publishes the draft, because a
  published release is immutable.
* A published tag never moves, and the repository rules block the deletion of a tag. To correct a
  bad release, publish a new version.

While the major version is 0, release-please selects the next version from the commits since the
last release:

| The commits since the last release contain | Next version |
|---|---|
| A breaking change (`!` in the subject) | Next minor version, for example `v0.3.1` to `v0.4.0` |
| A `feat` commit | Next minor version |
| Only `fix`, `perf`, and `revert` commits, and commits that change no behaviour | Next patch version, for example `v0.3.1` to `v0.3.2` |
| Only `docs`, `test`, `ci`, `build`, `chore`, or `refactor` commits | No release |

A breaking change has `!` in its subject, and a `BREAKING CHANGE:` footer that tells an operator
what to change. The changelog shows each breaking change.

### Make a release

release-please keeps one release pull request open, with the title `chore(main): release <version>`.
Each merge to `main` that changes behaviour updates it: the next version, `CHANGELOG.md`, and the
version in `Chart.yaml`. The pull request collects all changes until the maintainer merges it.

1. Read the release pull request. Make sure that the version and the changelog are correct. Read
   the Known limitations section of `README.md`, and make sure that each item is closed or
   accepted. For version 0.1.0, the legal review of ADR-0015 MUST be done first: a lawyer
   confirms the licence position, and the maintainer records the result in the ADR.
2. Merge the release pull request. release-please creates the tag and a draft release with the
   changelog.
3. The tag starts the release workflow in the `release` environment. Make sure that the SHA of the
   tag is the merge commit of the release pull request, and then approve the deployment. The
   workflow builds and signs the artifacts, verifies the signatures and the assets, and then
   publishes the release
   ([ADR-0013](docs/adrs/0013-sign-releases-with-keyless-cosign-signatures.md)).
   release-please creates the draft release when the release pull request merges, before the
   workflow waits for the approval. For version 0.1.0, edit the notes of the draft release before
   the approval: remove the entries of the `fix(ci)` and `fix(runner)` commits that are on `main`
   before the rule of `scripts/check-commits.py`. release-please rebuilds the release pull request
   on each push to `main`, so an edit of `CHANGELOG.md` there does not stay. `CHANGELOG.md` keeps
   these entries for version 0.1.0, and the project accepts this.

To set a version that the commits do not give, for example a release candidate such as
`0.4.0-rc.1`, or `1.0.0`, add `"release-as": "<version>"` to `release-please-config.json` in a
pull request. Remove it in the next pull request after the release.

If the workflow fails before it publishes, the release stays a draft. If the cause is outside the
repository, for example an outage of Sigstore, run the workflow again: it replaces the assets on
the draft. If the cause needs a change in the repository, the version is lost, because the tag
cannot move. Delete the draft, merge the correction, and release the next version.

## Licence of contributions

This project uses the [GNU AGPL-3.0-or-later](LICENSE). A contribution enters the project under
that licence, and a pull request states that the author has the right to give it.

Two rules protect the licence:

* Never copy code from a source with a field-of-use restriction, such as the FSL, into this
  repository. The only exception is `internal/orchard/types_gen.go`. The generator
  `tools/orchardgen` writes it from a pinned Orchard version, and it copies only the field names,
  the JSON tags, and the types of the fields that the controller uses, for interoperability. Its
  header names the source and the licence. A change that makes the controller read a new Orchard
  field adds that field to the allow list of the generator
  ([ADR-0015](docs/adrs/0015-generate-only-the-orchard-fields-the-controller-uses.md)).
* The controller calls the Orchard REST API with its own client. Never add an Orchard package to
  `go.mod`
  ([ADR-0010](docs/adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md)).

## Developer Certificate of Origin

A contribution MUST carry a sign-off under the
[Developer Certificate of Origin 1.1](https://developercertificate.org/). The sign-off says that
the author wrote the contribution, or has the right to give it under the licence of the project,
and that the author knows the project keeps a public record of it. The full text is at that
address.

* Add the sign-off with `git commit -s`. It adds the trailer
  `Signed-off-by: Name <email>`, and the email MUST be the email of the commit author.
* To add the sign-off to the commits of a branch, run `git rebase --signoff <base>`. The rebase
  rewrites the commits and removes their signatures, and the main branch requires signed commits.
  Sign the commits again, for example with `git rebase --exec 'git commit --amend --no-edit -S'
  <base>`, before the push.
* A bot that uses a GitHub App (Renovate, release-please, and github-actions) is exempt. Its
  author email ends with `[bot]@users.noreply.github.com`. The exemption trusts the author
  email, so the maintainer checks during review that a bot pull request comes from the bot
  account.

`scripts/check-commits.py` checks the sign-off of each commit of a pull request, and the
`commits` job fails the pull request when one is missing. The project squashes each pull request,
and the squash message is the pull request description. So the commit on `main` does not keep the
trailers. The commits of the pull request keep them, and the pull request record is the proof.

## Continuity

Today the project has one maintainer, and no second holder for the items below.

| Item | Holder today | Successor |
|---|---|---|
| The `release` and `release-please` environments | The maintainer | `UNKNOWN` |
| The private key of the release GitHub App | The maintainer | `UNKNOWN` |
| The rulesets of the repository | The maintainer | `UNKNOWN` |

* Owner of the `UNKNOWN` values: the maintainer.
* Trigger: before version 1.0.0, or when a second maintainer joins, whichever is first.
* If the maintainer is unavailable, no release can be made, because only the maintainer can
  approve the environments and use the App key. A change can still be reviewed and merged only by
  the maintainer, so the project pauses. The repository settings and the key stay as they are
  until a successor is named here.

The project does not name a person for these items until that person agrees.

## Ownership and decisions

The maintainer is the product owner. The kinds of change have these deciders:

| Kind of change | Who decides |
|---|---|
| A requirement | The maintainer, as product owner |
| An ADR | The maintainer, after review in the pull request |
| The design | The maintainer, after review in the pull request; the design follows the requirements |
| The code | The maintainer reviews it, and the requirements and the design decide what it does |

[`.github/CODEOWNERS`](.github/CODEOWNERS) names the maintainer as the owner of each path. To
move this to a team, replace the one user handle with a GitHub team handle (`@org/team`) on each
line. The team MUST have write access to the repository. Contact is through GitHub only: an issue
or a private security advisory.

## Security

Never open a public issue for a vulnerability. [`SECURITY.md`](SECURITY.md) gives the process.
