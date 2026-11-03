# Contributing

Thank you for your interest in bk-agent-stack-orchard. Issues and pull requests are welcome. The
[maintainers](MAINTAINERS.md) decide what the project accepts, and they work on it in their own
time, so there is no promise of a response time.

## Report a problem or propose a change

* Search the [issues](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues) first.
* To open an issue, use a
  [form](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/new/choose): a bug report
  for behaviour that differs from the target design, a change request for behaviour that the
  documents do not state yet, or a question.
* Never open a public issue for a vulnerability. [SECURITY.md](SECURITY.md) gives the private
  process.

## Set up your Mac

You need a Mac with Apple silicon and [Homebrew](https://brew.sh).

```sh
brew trust openai/tools   # once: the tap of Tart is not an official tap
brew bundle               # host tools that mise does not pin (Brewfile)
mise install              # Go, Orchard, the linters, and the release tools, at pinned versions
mise run check            # every gate: documents, commits, dependencies, lint, tests, and an image
```

The [development guide](docs/development.md) explains each step, the Docker runtime, and how to
run the controller locally.

These are the tasks that you use most. `mise tasks` lists all of them.

| Task | What it does |
|---|---|
| `mise run build` | Compile every package, and build the controller binary |
| `mise run test` | Run the Go tests with the race detector |
| `mise run lint` | Run golangci-lint |
| `mise run integration` | Run the integration tests against a synthetic Orchard |
| `mise run check-docs` | Check the links, line widths, and identifiers of the documents |
| `mise run check` | Run every gate before you open a pull request |
| `mise run ci` | Run the checks that the required CI jobs run |

## Make a change

1. **Agree the change first.** For more than a small correction, wait until a maintainer agrees in
   the issue, because the documents decide before the code (next section).
2. **Fork the repository, and work on a branch.**
3. **Change the documents, then the tests, then the code.** Each change of behaviour starts with a
   failing test.
4. **Run `mise run check`.**
5. **Open a pull request** from your fork, and complete the template. Put `Closes #<number>` in
   the pull request description, not only in a commit: the project squashes each pull request, and
   the squash keeps only the description. The workflows of a first pull request from a fork start
   after a maintainer approves them.
6. **A maintainer reviews and merges it.**

Nothing goes to `main` directly. A pull request must be up to date with `main` before it merges,
and a new push dismisses the approvals that it had. A pull request with no activity for 30 days
gets the `stale` label, and it closes 14 days later unless somebody comments, pushes a commit, or
removes the label. Dependency pull requests and the release pull request are exempt.

## Before code: the documents decide

The documents are the source of truth for what the controller does. They have one direction:

```text
product requirements  ->  decision records  ->  target design  ->  implementation plan
```

* A change of **behaviour** starts in the [product requirements](docs/product/requirements.md),
  with a new `PR-nnn` identifier.
* A change of **approach** needs a [decision record](docs/adrs/README.md). One ADR holds one
  decision, names its options, and records the consequences.
* A change of **mechanism** goes in the [target design](docs/architecture/target-design.md).
* A change to the **C4 model** goes in `docs/architecture/workspace.dsl` in the same commit, and
  `docs/architecture/render-c4-md.sh` regenerates `docs/architecture/c4.md`.

These documents describe the design:

| Document | What it answers |
|---|---|
| [Product requirements](docs/product/requirements.md) | What must the controller do, and for whom? |
| [System design baseline](docs/architecture/system-design-baseline.md) | Which quality targets, constraints, and assumptions limit the design? |
| [Target design](docs/architecture/target-design.md) | How does the controller work? |
| [C4 model](docs/architecture/c4.md) | What are the parts of the system, at each level? |
| [Decision records](docs/adrs/README.md) | Why did the project choose each approach? |
| [Test strategy](docs/quality/test-strategy.md) | Which risks exist, and which tests cover them? |
| [Implementation plan](docs/delivery/implementation-plan.md) | In which order does the work go? |
| [Glossary](docs/glossary.md) | What does a term or an identifier mean? |

[docs/README.md](docs/README.md) gives a reading order for your first day. The
[writing guide](docs/writing-guide.md) gives the rules for all documents.

## Code

* **Keep identifiers in the documents.** Production code, configuration, and task descriptions say
  what they do and why. They do not carry a work unit, a requirement, or a test identifier,
  because a reader of the code cannot resolve one. Test files can carry `T-nnn`, `I-n`, and `O-n`,
  because the tests are the evidence for them. A rule that exists for a recorded decision can name
  its ADR, because the reason is the useful part.
* **Fuzz the inputs.** Code that reads input from outside the controller, such as the agent rules
  of a job, a machine name from Orchard, or the configuration file, has a fuzz test that states a
  property for any input. `go test` runs its seed inputs, and `mise run fuzz` fuzzes it.
* **Keep the decisions pure.** The Matcher, the Admission function, and the Reaper decision
  function stay pure, with an injected clock. The suppliers stay behind narrow interfaces, so
  tests replace them with fakes.
* **Earn each test.** The [test strategy](docs/quality/test-strategy.md) decides the level of each
  test. A new test needs a defect signal that no cheaper test gives.
* **Use the Buildkite fake.** Do not write a second fake of the Buildkite Stacks API. The project
  uses the `FakeAgentServer` that Buildkite maintains.

## Commits and pull requests

One commit is one complete change, with its tests and its document updates. Every commit message
follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/), and
`scripts/check-commits.py` checks each commit of a pull request.

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

* The subject is imperative, lower case, without a full stop, and 72 characters or fewer, for
  example `feat(reaper): delete the VM when the job is terminal`. The scope is optional: a package
  or a document area, such as `reaper`, `orchard`, `adr`, or `design`.
* A pull request title follows the same rules, with 65 characters or fewer, because GitHub adds
  ` (#NNN)` when it squashes.
* The body says why the change is necessary, and it wraps at 72 characters.
* A change of a workflow uses `ci:`, never `fix(ci)`. release-please hides a commit by its type,
  not by its scope, so `fix(ci)` would show in the release notes.
* A breaking change has `!` before the colon, and a `BREAKING CHANGE:` footer that says what a user
  must do.
* A fix of a vulnerability names its identifier (`GO-`, `GHSA-`, or `CVE-`) in the subject or the
  body. Renovate has no field for it, so a maintainer adds it when a vulnerability update merges.
* Footers can name the source identifiers, for example `Refs: W-06, PR-001` and
  `Evidence: T-041`.
* Commits from Renovate and Dependabot keep the type and shape, but their subjects can be longer,
  because the module path belongs in the subject.

## Licence of contributions

The project uses the [GNU AGPL-3.0-or-later](LICENSE). A contribution enters the project under
that licence, and a pull request states that the author has the right to give it.

Two rules protect the licence:

* Never copy code from a source with a field-of-use restriction, such as the FSL, into this
  repository. The only exception is `internal/orchard/types_gen.go`, which `tools/orchardgen`
  generates from a pinned Orchard version. It copies only the names, JSON tags, and types of the
  Orchard fields that the controller uses. To read a new Orchard field, add it to the allow list
  of the generator.
* The controller calls the Orchard REST API with its own client. Never add an Orchard package to
  `go.mod`.

## Developer Certificate of Origin

Each commit carries a sign-off under the
[Developer Certificate of Origin 1.1](https://developercertificate.org/). The sign-off says that
you wrote the contribution, or have the right to give it under the licence of the project, and
that you know the project keeps a public record of it.

* Add the sign-off with `git commit -s`. The email of the trailer is the email of the commit
  author.
* To add the sign-off to the commits of a branch, run `git rebase --signoff <base>`. The rebase
  removes the commit signatures, and `main` requires signed commits, so sign the commits again
  before you push, for example with
  `git rebase --exec 'git commit --amend --no-edit -S' <base>`.
* Bots that use a GitHub App (Renovate, release-please, and github-actions) are exempt. Their
  author email ends with `[bot]@users.noreply.github.com`. A maintainer checks during review that
  a bot pull request comes from the bot account.

`scripts/check-commits.py` checks the sign-off of each commit, and the `commits` job fails a pull
request when one is missing. The commits of the pull request keep the trailers, and the pull
request record is the proof.

## Security

Never open a public issue for a vulnerability. [SECURITY.md](SECURITY.md) gives the process.
