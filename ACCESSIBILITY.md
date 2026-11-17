# Accessibility

This file says how the project makes its documents and its output usable for everyone, and how
you report a barrier.

## What the project gives you

The project has no graphical interface. You use it through these parts:

* The documents of this repository, which GitHub renders from Markdown.
* The configuration file, the values of the Helm chart, and the flags of the controller.
* The log records, the metrics, and the traces that the controller writes.
* The lines that the controller writes on the job pages of Buildkite.

GitHub and Buildkite render the pages where you read these parts. The accessibility of those pages
belongs to GitHub and Buildkite. The
[GitHub accessibility statement](https://accessibility.github.com/) covers GitHub.

## The standard

The documents aim to meet the [Web Content Accessibility Guidelines 2.2](https://www.w3.org/TR/WCAG22/),
level AA, for each part that an author of this project controls. The
[writing guide](docs/writing-guide.md#accessibility) gives the rules that put this into practice:

* Each image has alt text that gives its meaning.
* Each diagram has alt text that names its elements and the main relationships between them.
* No meaning depends on colour alone. A diagram also carries each meaning in its text or its
  shapes.
* Headings follow their order, and each page has one top-level heading.
* Link text says where the link goes.
* Each table has a header row.
* Text stays text. A command or an output is in a code block, never in an image.
* Sentences are short, and the words are plain.

The output of the controller is plain text:

* The log records are JSON, one record on each line.
* A line on a job page is one line of plain text with no control characters.
* The controller writes no colour codes, and it shows no animation.
* The [telemetry reference](docs/user/telemetry.md) names each metric, each log key, and each
  value in text.

## Report a barrier

If a document or an output of the project stops you, open an issue through an
[issue form](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/new/choose). Start
the title with "Accessibility:". Say which page or output it is and what stopped you. You can also
name the assistive technology that you use, if you want to.

The maintainers work on the project in their own time. There is no promise of a response time, but
a report of a barrier gets the same attention as a bug report.
