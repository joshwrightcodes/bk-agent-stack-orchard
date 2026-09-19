<!--
The title of this pull request MUST follow Conventional Commits, because the
project squashes each pull request into one commit. For example:
  feat(reaper): delete the VM when the job is terminal
CONTRIBUTING.md gives the types, the scopes, and the rules.
-->

## What changes

<!-- One or two sentences. Say what the project does now that it did not do before. -->

## Why

<!-- The problem this solves. Link the requirement, the driver, or the decision. -->

## Traceability

* Work unit: <!-- W-nn, or "none" -->
* Requirements: <!-- PR-nnn, or "none" -->
* Evidence: <!-- T-nnn that proves this change -->

## Checks

- [ ] The title follows Conventional Commits.
- [ ] `mise run check` passes.
- [ ] The evidence above passes in CI, and a new behaviour started with a failing test.
- [ ] The documents follow the change: a new behaviour updates the product requirements, a new
      approach has an ADR, and a new mechanism updates the target design.
- [ ] A change to a component, a container, or a relationship updates `workspace.dsl`, and
      `render-c4-md.sh` regenerated `c4.md`.
- [ ] No secret, token, or customer source code is in the diff or in the test data.
- [ ] No code with a field-of-use restriction entered the repository, and `go.mod` has no Orchard
      dependency (ADR-0010).
