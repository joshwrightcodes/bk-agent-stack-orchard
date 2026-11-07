# AI-assisted contributions

You can use AI tools to contribute to this project. A person stays responsible for the result.
This policy applies to pull requests, issues, comments, and vulnerability reports. It applies to
the maintainers as well.

## What you do

* **Disclose the tool.** State which tool you used and what it did if it produced more than a
  line completion, a spelling fix, or formatting. Use the AI section of the pull request template.
  In an issue, a comment, or a report, add one line.
* **Add the commit trailer.** If you disclose a tool, each commit that it helped to write names it
  in a `Co-authored-by` trailer, for example
  `Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>`. The trailer records the tool, and you
  stay responsible for the change. A squash keeps this trailer on `main`.
* **Understand the change.** Read all of it before you ask for a review. Explain the change and
  the reason for it in your own words, and answer the questions of a reviewer yourself.
* **Verify the change.** If you open a pull request, run `mise run check`. A claim in a pull
  request or an issue needs evidence that you checked.
* **Agree the change first.** [CONTRIBUTING.md](CONTRIBUTING.md) gives the process, and a tool does
  not change it.

## What CI checks

The `pull request title` check fails a pull request if one of these is true:

* The description has no AI tools section, or the section is empty.
* The section says "none", but a `Co-authored-by` trailer names an AI tool.

Write "none" in the section if you have nothing to disclose. CI cannot find a tool that you did
not declare, so disclosure stays your responsibility. The project squashes each pull request and
keeps its description, so the AI tools section stays on `main`.

## Your sign-off stays your responsibility

An agent can add your `Signed-off-by` trailer when you tell it to commit. The sign-off is still
your certification under the Developer Certificate of Origin, and you are responsible for it. Do
not let an agent sign off a commit that you will not review.

## Licences

The [licence rules](CONTRIBUTING.md#licence-of-contributions) apply to all output of a tool:

* The terms of the tool cannot restrict the output in a way that conflicts with the
  AGPL-3.0-or-later.
* If the output contains material from a third party, you need permission to use it. Give notice
  and attribution in your contribution.
* A tool that regenerates copyrighted material does not remove the copyright. Do not use a tool to
  reproduce Orchard code, because a field-of-use restriction applies to that code.

## What the project does not accept

* An issue, a pull request, or a comment that an agent submits before a person reads it. If you
  run an agent that submits work to this repository without review, turn it off.
* A contribution that its author cannot explain.
* A vulnerability report that nobody reproduced. [SECURITY.md](SECURITY.md) gives the process.

The automated accounts that the maintainers configure are exempt: Renovate, Dependabot,
release-please, and CodeRabbit. Their work still goes through the normal review.

## What the maintainers do

The maintainers can close a contribution without a detailed review if it has no disclosure, or if
it takes more time to review than it gives back. You can reopen it after you fix the problem. The
maintainers can block an account that repeats the problem.

## Sources

This policy follows the [LLVM AI tool policy](https://llvm.org/docs/AIToolPolicy.html) and the
[Model Context Protocol AI policy](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/AI_POLICY.md).
The licence rules follow the
[Linux Foundation generative AI policy](https://www.linuxfoundation.org/legal/generative-ai).
