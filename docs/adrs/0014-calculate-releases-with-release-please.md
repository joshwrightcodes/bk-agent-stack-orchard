---
status: proposed
date: 2026-10-31
decision-makers: Josh Wright
---

# ADR-0014: Calculate releases with release-please

## Summary

A release needs a version that follows the 0.x rules of the contributing guide, and it must
collect all changes since the last release. A person who selects the version from a table and
pushes a tag can select the wrong version. The decision is to let release-please calculate the
version from the Conventional Commits on `main`, and to keep a release pull request that collects
the changes until the maintainer merges it. The cost is a GitHub App with write access to the
repository, and its private key.

## Context

The commit gate makes each commit on `main` a Conventional Commit, and the pull request title
becomes the squashed commit. So the commits since the last release contain enough data to select
the next version: a breaking change or a `feat` gives the next minor version while the major
version is 0, and a `fix` or a `perf` gives the next patch version.

A release tag starts the signed release of ADR-0013. The repository makes a published release
immutable, so the assets go to a draft release first. The version of the chart must be the
version of the release.

GitHub starts no workflow for a pull request, a tag, or a release that the `GITHUB_TOKEN` of a
workflow creates (GitHub documentation, 2026-10-31). A tool that runs with that token gives a
release pull request with no CI, and a tag that starts no release workflow.

release-please (v5.0.0 of its action, 2026-04-22) keeps a release pull request open on the
default branch. It writes the next version, the changelog, and the version in other files. When
the pull request merges, it creates the tag and the GitHub release. `bump-minor-pre-major` makes
a breaking change bump the minor version below 1.0.0, `draft` makes the release a draft, and
`force-tag-creation` creates the tag of a draft release at once (release-please documentation,
2026-10-31).

## Goals and non-goals

Goals:

* **The rules select the version.** The version follows the 0.x rules without a person who reads a
  table.
* **The maintainer decides when.** Changes collect until the maintainer releases them, and many
  changes can go into one release.
* **One version everywhere.** The binary, the image, the chart, and the changelog have the same
  version, and the repository holds it.
* **The signed release still works.** The tag starts the release workflow, and the assets go to a
  draft before the publication.

Non-goals:

* The signatures and the SBOMs, which ADR-0013 decides.
* Version 1.0.0, which the maintainer decides.

## Options considered

**A tag from the maintainer.** The maintainer selects the version from the table of the
contributing guide, and pushes a tag. It needs no tool and no credential. However, a person can
select the wrong version, the chart version needs a separate step, and nothing writes a
changelog.

**semantic-release.** It calculates the version and releases on each merge to `main`. However, it
releases each change alone, unless a person starts it by hand, and then it does not show the next
release before it happens.

**An action that calculates a tag.** Actions such as `mathieudutour/github-tag-action` calculate
the next version and push a tag. However, the maintainer must still decide when to run it, they
write no changelog, and they update no other file.

**release-please.** It keeps a release pull request that shows the next version and the
changelog, and it updates both at each merge. The merge of that pull request is the release.
However, it needs a token that starts workflows, which means a GitHub App and its private key.

| Goal | Tag from the maintainer | semantic-release | Tag action | release-please |
|---|---|---|---|---|
| The rules select the version | No | Yes | Yes | Yes |
| The maintainer decides when | Yes | No | Yes | Yes |
| One version everywhere | No | Yes, with plugins | No | Yes |
| The signed release still works | Yes | Yes | Yes | Yes, with a draft and an App token |

## Decision

Calculate releases with release-please, and release when the maintainer merges its release pull
request.

* release-please runs on each push to `main`, with `bump-minor-pre-major`. It writes the version
  into `CHANGELOG.md` and into both versions of `Chart.yaml`.
* It creates the release as a draft with the changelog, and it creates the tag at once. The tag
  starts the release workflow of ADR-0013, which adds the assets and publishes the draft.
* It uses a token of a GitHub App with contents, pull requests, and issues write access. The private
  key MUST be a secret of the `release-please` environment, which permits only `main`.
* The release workflow MUST run in the `release` environment, which permits only `v*` tags and
  needs the approval of the maintainer. It MUST check that the version in `Chart.yaml` is the
  version of the tag.
* A Go module update is a `fix`, because it changes the release artifacts. An update of a
  deployment image or an action is not, because no release artifact contains it.
* To set a version that the commits do not give, such as a release candidate or 1.0.0, the
  maintainer sets `release-as` in a pull request.

## Consequences

Good:

* The version follows the rules, and the release pull request shows it before the release.
* Many changes go into one release, and the maintainer selects the time.
* The changelog and the chart version need no manual step.
* The tag check of the release workflow refuses a tag that release-please did not prepare.

Bad:

* A GitHub App with write access to the repository exists, and its private key is a credential
  to protect and to rotate.
* The release pull request adds commits from the App, and the maintainer must review them.
* The release has two approvals: the merge of the release pull request, and the deployment to the
  `release` environment.
* A release candidate or a major version needs a change of the configuration, and a second change
  to remove it.

## Confirmation

* A merge to `main` with a `feat` or a `fix` opens or updates the release pull request, and its CI
  runs.
* After the merge of the release pull request, the tag starts the release workflow in the
  `release` environment, and the workflow publishes the draft that release-please created.
* The release workflow fails when the version in `Chart.yaml` is not the version of the tag.
