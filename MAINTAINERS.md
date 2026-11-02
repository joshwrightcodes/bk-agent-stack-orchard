# Maintainers

The maintainers review and merge changes, make releases, and decide what the project accepts.
This is the only file that names them. Other documents say "the maintainers".

| Name | GitHub |
|---|---|
| Josh Wright | [@joshwrightcodes](https://github.com/joshwrightcodes) |

Contact the maintainers through GitHub only:

* Questions, bugs, and change requests: an
  [issue form](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/new/choose).
* Vulnerabilities: the private process in [SECURITY.md](SECURITY.md).
* Conduct: the channels in [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## What the maintainers decide

| Kind of change | Who decides |
|---|---|
| A requirement | The maintainers, as product owner |
| An ADR | The maintainers, after review in the pull request |
| The design | The maintainers, after review in the pull request. The design follows the requirements. |
| The code | The maintainers review it. The requirements and the design decide what it does. |

[`.github/CODEOWNERS`](.github/CODEOWNERS) makes the maintainers the owners of each path, so each
pull request needs a review from a maintainer. Only the maintainers merge.

## How the maintainers track work

The maintainers track work in a GitHub project. Each item is a work unit of the
[implementation plan](docs/delivery/implementation-plan.md).

1. **Assign the item** to the person who does the work, before the work starts. An item with no
   assignee is not in progress.
2. **Move it to In progress** when the work starts, and work on a branch named for the unit, for
   example `w-26-document-gate`.
3. **Open a pull request** that links the item, with `Closes #<number>` in its description. A unit
   that needs more than one pull request gets a sub-issue for each one, and the sub-issues carry
   the links.
4. **Move it to In review** when the pull request is ready for review.
5. **Merge it.** The merge closes the item and moves it to Done.

[Releasing](docs/releasing.md) gives the steps of a release.

## Continuity

The project has no second holder for these items yet:

| Item | Holder today | Successor |
|---|---|---|
| The `release` and `release-please` environments | The maintainers | `UNKNOWN` |
| The private key of the release GitHub App | The maintainers | `UNKNOWN` |
| The rulesets of the repository | The maintainers | `UNKNOWN` |

* Owner of the `UNKNOWN` values: the maintainers.
* Trigger: before version 1.0.0, or when a second maintainer joins, whichever is first.
* If no maintainer is available, no release can be made, and no pull request can merge. The
  project pauses, and the repository settings and the key stay as they are until a successor is
  named here.

The project does not name a person for these items until that person agrees.
