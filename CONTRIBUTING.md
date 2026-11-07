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
| `mise run coverage` | Run the Go tests with coverage, and fail below the threshold |
| `mise run lint` | Run golangci-lint |
| `mise run lint-scripts` | Lint the shell and Python scripts and the workflows |
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

## How a change is reviewed

Every change reaches `main` through a pull request, and a maintainer reviews each one.
[`.github/CODEOWNERS`](.github/CODEOWNERS) makes the maintainers the owners of each path.
CodeRabbit also reviews each pull request, and the author answers each of its comments in its
thread.

The reviewer checks these items:

* **The checks pass.** Every required check is green, and code scanning has no new alert at the
  threshold that [SECURITY.md](SECURITY.md#static-analysis-findings) states.
* **The documents decide.** The change follows the requirements and the design, or it changes
  them in the same pull request, with an ADR for a new approach.
* **The tests prove it.** A change of behaviour has a test that failed before the change. The test
  is at the level that the [test strategy](docs/quality/test-strategy.md) gives.
* **The code is clear and safe.** It follows the style of its language, it handles its errors, and
  it adds no secret, and no dependency without a review of its licence
  ([SECURITY.md](SECURITY.md#dependencies-and-vulnerabilities)). A change that adds a trust
  boundary, or changes one, is acceptable only when the same pull request updates the
  [assurance case](docs/quality/assurance-case.md).
* **The history is clean.** The commits follow Conventional Commits, carry a sign-off, and are
  signed. The pull request template is complete.

A change is acceptable when every item holds, and every comment of the review is answered or
resolved. The maintainers decide the rest, as [MAINTAINERS.md](MAINTAINERS.md) states.

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

Follow the style of each language. CI checks each one, and a change that fails the check does not
merge.

| Language | Style | Check |
|---|---|---|
| Go | [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments). gofumpt and goimports format the code. | `mise run lint` and `mise run lint-format` (golangci-lint, with the linters of [`.golangci.yaml`](.golangci.yaml)) |
| Shell | [Google Shell Style Guide](https://google.github.io/styleguide/shellguide.html). shfmt formats the scripts. | `mise run lint-scripts` (shellcheck, shfmt) |
| Python | [PEP 8](https://peps.python.org/pep-0008/), as ruff applies it with [`ruff.toml`](ruff.toml) | `mise run lint-scripts` (ruff) |
| GitHub Actions workflows | Actions pinned by commit, and the least permissions for each job | `mise run lint-scripts` (actionlint, zizmor) |
| Documents | The [writing guide](docs/writing-guide.md) | `mise run check-docs` (Vale, links, identifiers) |

* **Start each source file with its licence and copyright.** A Go, shell, or Python file starts
  with an `SPDX-License-Identifier: AGPL-3.0-or-later` comment, after the shebang of a script,
  and an `SPDX-FileCopyrightText: <year> The bk-agent-stack-orchard Authors` comment on the next
  line. Each contributor keeps the copyright of their work and licenses it under the AGPL with
  their sign-off, so the line names the authors together, and the history of the repository
  records who wrote what. A test of `internal/repocheck` checks both lines. `REUSE.toml` gives
  them for the files that cannot carry a comment.
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
* A commit that an AI tool helped to write names it in a `Co-authored-by` trailer.
* Footers can name the source identifiers, for example `Refs: W-06, PR-001` and
  `Evidence: T-041`.
* Commits from Renovate and Dependabot keep the type and shape, but their subjects can be longer,
  because the module path belongs in the subject.

## AI tools

You can use AI tools. [AI_POLICY.md](AI_POLICY.md) states what you disclose, and what the project
does not accept. Agents read [AGENTS.md](AGENTS.md). A person stays responsible for each change,
even when an agent adds the sign-off.

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
