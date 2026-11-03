# Releasing

This guide tells the maintainers how versions are calculated and how to make a release.
Operators read [Install the controller](user/install.md) for the versioning rules that affect
them.

## Versions

The project uses [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). The binary,
the container image, and the Helm chart have the same version.
[release-please](https://github.com/googleapis/release-please) calculates each version from the
Conventional Commits on `main`.

* A release tag has the form `vMAJOR.MINOR.PATCH`, for example `v0.3.1`.
* The major version stays 0 until the maintainers decide that the project is stable. Only the
  maintainers start version 1.0.0. A pull request from anyone else does not set a major version of
  1 or more.
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

## Make a release

release-please keeps one release pull request open, with the title `chore(main): release <version>`.
Each merge to `main` that changes behaviour updates it: the next version, `CHANGELOG.md`, and the
version in `Chart.yaml`. The pull request collects all changes until the maintainers merge it.

1. Read the release pull request. Check that the version and the changelog are correct. Check that
   the Limitations section of `README.md` is still correct for the new version.
2. Run the quick start with `main` on a Mac
   (`mise run quickstart -- --queue <queue> --from-source`), and run the step that it prints.
   Check that the job passes and that Ctrl-C leaves no VM. A hosted runner cannot start a macOS
   VM, so CI does not run the quick start.
3. Write the release notes in the body of the release pull request. release-please takes the
   notes of the GitHub release from the body of the merged release pull request, and not from
   `CHANGELOG.md`. Make this edit last: release-please writes the body again on each push to
   `main`, so merge nothing else between the edit and step 4. Keep these parts of the body:
   * The two lines that are `---`. The notes are the text between them.
   * The version heading, for example `## 0.4.0 (2027-01-15)`, as the first line of the notes.
     Without it, release-please finds no version, and it creates no tag and no release.

   Under the heading, you can add a summary for operators, such as what the release needs and
   what to change, and remove entries that do not concern them. For version 0.1.0, remove the
   entries of the `fix(ci)` and `fix(runner)` commits that are on `main` from before the rule of
   `scripts/check-commits.py`. `CHANGELOG.md` keeps the list that release-please wrote, and the
   project accepts this.
4. Merge the release pull request. release-please creates the tag, and a draft release with the
   notes of step 3.
5. The tag starts the release workflow in the `release` environment. Each of its two jobs waits
   for an approval:
   1. Check that the SHA of the tag is the merge commit of the release pull request, and that the
      notes of the draft release are correct. Then approve the `build` job. It builds the
      archives and the image, and pushes the image and the chart.
   2. When the `build` job passes, approve the `sign and publish` job. It signs the artifacts,
      uploads them to the draft release, and attests their build provenance. It then verifies the
      signatures, the attestations, and the assets, and publishes the release. Do not edit the
      notes after this approval: the job compares them before and after it publishes, and fails
      when they differ, but the published release is then immutable.

After the release is published, run the quick start again with the release
(`mise run quickstart -- --queue <queue> --version v<version>`). It downloads the archive, checks
its signature and checksum, and runs one job with it.

To set a version that the commits do not give, for example a release candidate such as
`0.4.0-rc.1`, or `1.0.0`, add `"release-as": "<version>"` to `release-please-config.json` in a
pull request. Remove it in the next pull request after the release.

## When a release fails

If the workflow fails before it publishes, the release stays a draft.

* If the cause is outside the repository, for example an outage of Sigstore, run the failed jobs
  again. The workflow replaces the assets on the draft.
* If the cause needs a change in the repository, the version is lost, because the tag cannot move.
  Delete the draft, merge the correction, and release the next version.

ADR-0013 records why releases use keyless signatures, and ADR-0014 records why release-please
calculates the versions.
