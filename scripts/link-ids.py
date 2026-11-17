#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Link each citation of an identifier in the documents to the place that defines it.

The script gives each definition an anchor, and turns each bare citation into a relative link to
that anchor, or to the file of an ADR. It reads the families, the definitions, and the parts of a
document that stay as they are from scripts/check-docs.py, so the two scripts agree. Run it again
after a change: it adds only what is missing. scripts/check-docs.py then checks the result.

Usage: scripts/link-ids.py
"""

from __future__ import annotations

import importlib.util
import pathlib
import re
import sys

SPEC = importlib.util.spec_from_file_location(
    "check_docs", pathlib.Path(__file__).with_name("check-docs.py")
)
check_docs = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_docs)


def add_anchors() -> int:
    """Give each definition its anchor. A requirement gets it on the line before its title, so
    that the title still starts the line. Each other definition gets it in front of its
    identifier."""
    added = 0
    for _family, (source, pattern) in check_docs.definitions().items():
        if not source.exists():
            continue
        text = check_docs.read(source)
        lines = text.split("\n")
        for index, line in enumerate(lines):
            match = re.match(pattern, line)
            if not match:
                continue
            identifier = match.group(1)
            tag = f'<a id="{check_docs.anchor_name(identifier)}"></a>'
            if tag in text:
                continue
            if identifier.startswith("PR-"):
                lines[index] = f"{tag}\n{line}"
            else:
                # The anchor goes in front of the bold markers of a definition in a list.
                at = match.start(1)
                if line[at - 2 : at] == "**":
                    at -= 2
                lines[index] = line[:at] + tag + line[at:]
            added += 1
        source.write_text("\n".join(lines), encoding="utf-8")
    return added


def link_citations() -> int:
    """Turn each bare citation of a known identifier into a link to its definition."""
    known = set().union(*check_docs.defined_identifiers().values())
    sources = {identifier: check_docs.source_of(identifier) for identifier in known}
    linked = 0
    for path in check_docs.markdown_files():
        text = check_docs.read(path)
        matches = check_docs.unlinked_citations(path, text, known)
        for match in reversed(matches):
            identifier = match.group(1)
            source = sources.get(identifier)
            if source is None:
                continue
            target = check_docs.link_target(identifier, source, path)
            text = f"{text[: match.start()]}[{identifier}]({target}){text[match.end() :]}"
            linked += 1
        if matches:
            path.write_text(text, encoding="utf-8")
    return linked


LIST_ITEM = re.compile(r"^( *)(?:[*+-]|\d+\.) ")
BREAK = re.compile(r"^ *(?:$|#|\||```|~~~|>|<!--|[*+-] |\d+\. )")
# The line that add_anchors puts above a requirement. It stays on its own line.
ANCHOR_LINE = re.compile(r'^ *<a id="[a-z0-9-]+"></a> *$')


def width(text: str) -> int:
    """The width that scripts/check-docs.py counts: a link target and an anchor count nothing."""
    return len(check_docs.ANCHOR_TAG.sub("", check_docs.URL.sub("", text)))


def rewrap(lines: list[str], index: int) -> int:
    """Wrap again the paragraph or list item that holds the line at index, and return the number
    of lines that it has now. A word never splits, so a link stays whole."""
    start = index
    while (
        start > 0
        and not BREAK.match(lines[start])
        and lines[start - 1].strip()
        and not ANCHOR_LINE.match(lines[start - 1])
    ):
        start -= 1
    end = index
    while (
        end + 1 < len(lines)
        and lines[end + 1].strip()
        and not BREAK.match(lines[end + 1])
        and not ANCHOR_LINE.match(lines[end + 1])
    ):
        end += 1
    first = lines[start]
    item = LIST_ITEM.match(first)
    lead = first[: len(first) - len(first.lstrip())]
    if item:
        lead = first[: item.end()]
    indent = " " * len(lead)
    if end > start:
        follow = lines[start + 1]
        indent = follow[: len(follow) - len(follow.lstrip())]
    words = " ".join(line.strip() for line in lines[start : end + 1])[len(lead.strip()) :].split()
    wrapped, current, fresh = [], lead, True
    for word in words:
        candidate = current + word if fresh else f"{current} {word}"
        if not fresh and width(candidate) > check_docs.MAX_WIDTH:
            wrapped.append(current)
            current = indent + word
        else:
            current = candidate
        fresh = False
    wrapped.append(current)
    lines[start : end + 1] = wrapped
    return len(wrapped)


def wrap_long_lines() -> int:
    """Wrap again each paragraph that a link made longer than the limit."""
    changed = 0
    for path in check_docs.markdown_files():
        lines = check_docs.read(path).split("\n")
        index, touched = 0, False
        while index < len(lines):
            line = lines[index]
            # A rewrap changes the number of lines, so the fences are found again for each line.
            if (
                not check_docs.fenced(lines[: index + 1])[index]
                and not line.lstrip().startswith("|")
                and width(line) > check_docs.MAX_WIDTH
            ):
                rewrap(lines, index)
                touched = True
                changed += 1
            index += 1
        if touched:
            path.write_text("\n".join(lines), encoding="utf-8")
    return changed


def main() -> int:
    anchors = add_anchors()
    links = link_citations()
    wrapped = wrap_long_lines()
    print(
        f"link-ids: {anchors} anchor(s) and {links} link(s) added, {wrapped} paragraph(s) wrapped"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
