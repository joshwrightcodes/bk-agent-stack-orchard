#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Check that the document set stays whole.

The documents of this project carry identifiers, and the direction of the set runs from the
product requirements to the design. This script fails when that structure breaks. It reads only;
it changes nothing.

Checks:

  1. every identifier that a document cites exists;
  2. every requirement appears in an "Implements" line of the target design, unless its status
     is `out of scope`;
  3. no ADR cites an ADR with a higher number;
  4. every relative link resolves;
  5. every line outside a table row or a fenced block is 100 characters or less;
  6. each ADR file name matches its title, and its status is a lifecycle value;
  7. no "ADR-0001 to ADR-NNNN" range stops below the highest ADR that exists;
  8. the glossary lists every identifier family that this script knows, with its digits;
  9. every citation of an identifier links to the place that defines it, and each such link
     reaches that place. scripts/link-ids.py adds the missing links and anchors.
"""

from __future__ import annotations

import os
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
REQUIREMENTS = ROOT / "docs/product/requirements.md"
BASELINE = ROOT / "docs/architecture/system-design-baseline.md"
DESIGN = ROOT / "docs/architecture/target-design.md"
STRATEGY = ROOT / "docs/quality/test-strategy.md"
PLAN = ROOT / "docs/delivery/implementation-plan.md"
ADR_DIR = ROOT / "docs/adrs"
GLOSSARY = ROOT / "docs/glossary.md"

MAX_WIDTH = 100
ADR_STATUS = {"proposed", "accepted", "rejected", "deprecated"}

errors: list[str] = []


def fail(path: pathlib.Path, line: int | None, message: str) -> None:
    where = f"{path.relative_to(ROOT)}:{line}" if line else str(path.relative_to(ROOT))
    errors.append(f"{where}: {message}")


def markdown_files() -> list[pathlib.Path]:
    files = sorted(ROOT.glob("docs/**/*.md"))
    for pattern in ("images/**/*.md", "deploy/**/*.md", "internal/**/README.md"):
        files += sorted(ROOT.glob(pattern))
    files += [
        ROOT / name
        for name in (
            "README.md",
            "CONTRIBUTING.md",
            "SECURITY.md",
            "CODE_OF_CONDUCT.md",
            ".github/pull_request_template.md",
        )
    ]
    return sorted({f for f in files if f.exists()})


def read(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8")


def find_all(path: pathlib.Path, pattern: str) -> set[str]:
    return set(re.findall(pattern, read(path), re.M)) if path.exists() else set()


# The anchor that marks where an identifier is defined, so that a citation can link to it.
ANCHOR = r'(?:<a id="[a-z0-9-]+"></a>)?'


def anchor_name(identifier: str) -> str:
    return identifier.lower()


def definitions() -> dict[str, tuple[pathlib.Path, str]]:
    """Each family, the document that defines it, and the pattern of a definition. The pattern
    allows the anchor of the definition, and its group is the identifier. A requirement keeps its
    anchor on the line before the requirement, so its pattern needs none."""
    return {
        "PR": (REQUIREMENTS, r"^\*\*(PR-\d{3}):"),
        "driver": (BASELINE, rf"^\| {ANCHOR}((?:BD|SC|QA|DR|SE|OP|CO)-\d{{2}}) \|"),
        "QS": (BASELINE, rf"^\| {ANCHOR}(QS-\d) \|"),
        "T": (STRATEGY, rf"^\| {ANCHOR}(T-\d{{3}}) \|"),
        "R": (STRATEGY, rf"^\| {ANCHOR}(R-\d{{2}}) \|"),
        "I": (STRATEGY, rf"^\* {ANCHOR}\*\*(I-\d):\*\*"),
        "E": (STRATEGY, rf"^\* {ANCHOR}\*\*(E-\d):\*\*"),
        "O": (STRATEGY, rf"^\| {ANCHOR}(O-\d) \|"),
        "W": (PLAN, rf"^\| {ANCHOR}(W-\d{{2}}) \|"),
        "D": (PLAN, rf"^\| {ANCHOR}(D-\d) \|"),
    }


def defined_identifiers() -> dict[str, set[str]]:
    adrs = {f"ADR-{p.name[:4]}" for p in ADR_DIR.glob("0*.md")}
    found = {family: find_all(path, pattern) for family, (path, pattern) in definitions().items()}
    return {**found, "ADR": adrs}


def source_of(identifier: str) -> pathlib.Path | None:
    """The document that defines an identifier: its ADR file, or the document of its family."""
    if identifier.startswith("ADR-"):
        files = sorted(ADR_DIR.glob(f"{identifier[4:]}-*.md"))
        return files[0] if files else None
    for path, pattern in definitions().values():
        if identifier in find_all(path, pattern):
            return path
    return None


def link_target(identifier: str, source: pathlib.Path, citing: pathlib.Path) -> str:
    """The relative link from the citing document to the definition of an identifier."""
    fragment = "" if identifier.startswith("ADR-") else f"#{anchor_name(identifier)}"
    if source == citing:
        return fragment or source.name
    return os.path.relpath(source, citing.parent).replace(os.sep, "/") + fragment


# The identifier families: the prefix, and the number of digits. The citation pattern and the
# glossary check both read this table, so a new family needs one line here and one row in the
# glossary.
FAMILIES = {
    "PR": 3,
    "BD": 2,
    "SC": 2,
    "QA": 2,
    "DR": 2,
    "SE": 2,
    "OP": 2,
    "CO": 2,
    "QS": 1,
    "T": 3,
    "R": 2,
    "I": 1,
    "E": 1,
    "O": 1,
    "W": 2,
    "D": 1,
    "ADR": 4,
}

CITATION = re.compile(
    r"\b(" + "|".join(rf"{prefix}-\d{{{width}}}" for prefix, width in FAMILIES.items()) + r")\b"
)

# The parts of a document where a citation needs no link: front matter, code, comments,
# headings, the text and the target of an existing link, a reference definition, and an anchor.
FRONT_MATTER = re.compile(r"\A---\n.*?\n---\n", re.S)
FENCE = re.compile(r"^ *(`{3,}|~{3,})")
COMMENT = re.compile(r"<!--.*?-->", re.S)
HEADING = re.compile(r"^ {0,3}#{1,6} .*$", re.M)
# A code span can wrap over lines, but it ends at a blank line, as its paragraph does.
CODE = re.compile(r"(`+)(?:[^`\n]|\n(?![ \t]*\n))*?\1")
# The text of a link can wrap over lines, but it ends at a blank line, as its paragraph does.
LINK_TEXT = r"\[(?:[^\]\n]|\n(?![ \t]*\n))*\]"
LINK = re.compile(r"!?" + LINK_TEXT + r"\((?:[^()\s]|\([^()\s]*\))*\)")
REFERENCE = re.compile(r"^ {0,3}\[([^\]\n]+)\]:[ \t]*<?([^\s>]+)>?.*$", re.M)
# A full or collapsed reference link, [text][label] or [text][], and a shortcut one, [label].
REFERENCE_LINK = re.compile(r"!?\[([^\]\n]+)\]\[([^\]\n]*)\]|!?\[([^\]\n]+)\](?![\[(:])")
ANCHOR_TAG = re.compile(r'<a id="[a-z0-9-]+"></a>')
ID_LINK = re.compile(r"\[(" + CITATION.pattern[3:-3] + r")\]\(([^)\s]+)\)")


def fenced(lines: list[str]) -> list[bool]:
    """For each line, whether it is in a fenced code block, its fences included. As in CommonMark,
    only a fence of the same character, at least as long as the opening one, and with nothing after
    it, closes the block."""
    inside, opening = [], None
    for line in lines:
        fence = FENCE.match(line)
        if opening is None:
            opening = fence.group(1) if fence else None
            inside.append(opening is not None)
            continue
        inside.append(True)
        if (
            fence
            and fence.group(1)[0] == opening[0]
            and len(fence.group(1)) >= len(opening)
            and not line[fence.end() :].strip()
        ):
            opening = None
    return inside


def code_spans(text: str) -> list[tuple[int, int]]:
    """The spans that a reader does not see as text: front matter, code, and comments."""
    spans: list[tuple[int, int]] = []
    front = FRONT_MATTER.match(text)
    if front:
        spans.append(front.span())
    offset = 0
    for line, inside in zip(text.split("\n"), fenced(text.split("\n")), strict=True):
        if inside:
            spans.append((offset, offset + len(line)))
        offset += len(line) + 1
    comments = [m.span() for m in COMMENT.finditer(text)]
    spans += comments
    # A backtick in a comment does not start a code span, so the comments are blanked first. The
    # blanks keep the length and the line breaks of the text.
    blanked = text
    for start, end in comments:
        blanked = blanked[:start] + re.sub(r"[^\n]", " ", blanked[start:end]) + blanked[end:]
    spans += [m.span() for m in CODE.finditer(blanked)]
    return spans


def inside(spans: list[tuple[int, int]], start: int, end: int) -> bool:
    return any(a <= start and end <= b for a, b in spans)


def reference_links(text: str) -> list[tuple[re.Match, str, str]]:
    """Each reference link with a definition in the document, outside code: its match, its text,
    and the target of its definition."""
    code = code_spans(text)
    targets = {
        m.group(1).lower(): m.group(2)
        for m in REFERENCE.finditer(text)
        if not inside(code, *m.span())
    }
    links = []
    for match in REFERENCE_LINK.finditer(text):
        if inside(code, *match.span()):
            continue
        label_text = match.group(1) or match.group(3)
        label = match.group(2) or label_text
        target = targets.get(label.lower())
        if target is not None:
            links.append((match, label_text, target))
    return links


def protected(path: pathlib.Path, text: str) -> list[tuple[int, int]]:
    """The spans of a document where a citation stays as it is."""
    spans = code_spans(text)
    for pattern in (HEADING, LINK, REFERENCE, ANCHOR_TAG):
        spans += [m.span() for m in pattern.finditer(text)]
    spans += [match.span() for match, _text, _target in reference_links(text)]
    # The definition of an identifier is its target, not a citation.
    for source, pattern in definitions().values():
        if source.resolve() == path.resolve():
            spans += [m.span(1) for m in re.finditer(pattern, text, re.M)]
    return spans


def unlinked_citations(path: pathlib.Path, text: str, known: set[str]) -> list[re.Match]:
    """The citations of known identifiers that are not links, in the order of the text."""
    spans = protected(path, text)
    return [
        m for m in CITATION.finditer(text) if m.group(1) in known and not inside(spans, *m.span())
    ]


def check_identifiers(defined: dict[str, set[str]]) -> None:
    known = set().union(*defined.values())
    for path in markdown_files():
        for number, line in enumerate(read(path).split("\n"), 1):
            for cited in CITATION.findall(line):
                if cited not in known:
                    fail(path, number, f"cites {cited}, which no document defines")


def requirement_status(block: str) -> str | None:
    """The value of the status field of a requirement block. The field is the bullet that starts
    with `Status:`, or the end of the bullet that starts with `Source:`. A status that another
    bullet quotes, such as the history, is not the field. A line break can split the field."""
    for bullet in re.split(r"\n(?=\* )", block):
        field = re.match(r"\* (?:Source: .*? )?Status: `([^`]+)`", " ".join(bullet.split()))
        if field:
            return field.group(1)
    return None


def out_of_scope_requirements() -> set[str]:
    """The requirements whose status is `out of scope`: the owner removed them from the scope, so
    no design implements them."""
    blocks = re.split(r"^(?=\*\*PR-\d{3}:)", read(REQUIREMENTS), flags=re.M)
    return {
        block[2:8]
        for block in blocks
        if block.startswith("**PR-") and requirement_status(block) == "out of scope"
    }


def check_citation_links(defined: dict[str, set[str]]) -> None:
    """Each citation links to its definition, each such link reaches it, and each definition
    carries the anchor that the links use. scripts/link-ids.py repairs the first and the last."""
    known = set().union(*defined.values())
    for family, (source, _pattern) in definitions().items():
        text = read(source) if source.exists() else ""
        for identifier in sorted(defined.get(family, set())):
            if f'<a id="{anchor_name(identifier)}"></a>' not in text:
                fail(source, None, f"defines {identifier} without its anchor")
    for path in markdown_files():
        text = read(path)
        for match in unlinked_citations(path, text, known):
            number = text.count("\n", 0, match.start()) + 1
            fail(path, number, f"cites {match.group(1)} without a link to its definition")
        code = code_spans(text)
        links = [
            (match.start(), match.group(1), match.group(2))
            for match in ID_LINK.finditer(text)
            if not inside(code, *match.span())
        ]
        links += [
            (match.start(), label_text, target)
            for match, label_text, target in reference_links(text)
            if CITATION.fullmatch(label_text)
        ]
        for start, identifier, target in links:
            source = source_of(identifier)
            if source is None:
                continue
            file, _, fragment = target.partition("#")
            reached = (path.parent / file).resolve() if file else path.resolve()
            number = text.count("\n", 0, start) + 1
            if reached != source.resolve():
                fail(path, number, f"links {identifier} to {target}, not to its definition")
            elif not identifier.startswith("ADR-") and fragment != anchor_name(identifier):
                fail(path, number, f"links {identifier} to {target}, not to its anchor")


def check_requirement_coverage(defined: dict[str, set[str]]) -> None:
    implements = "\n".join(
        line for line in read(DESIGN).split("\n") if line.startswith("**Implements.**")
    )
    retired = out_of_scope_requirements()
    for requirement in sorted(defined["PR"] - retired):
        if requirement not in implements:
            fail(DESIGN, None, f"no Implements line claims {requirement}")
    for requirement in sorted(retired):
        if requirement in implements:
            fail(DESIGN, None, f"an Implements line claims {requirement}, which is out of scope")


def check_adr_direction() -> None:
    for path in sorted(ADR_DIR.glob("0*.md")):
        own = int(path.name[:4])
        for number, line in enumerate(read(path).split("\n"), 1):
            # The rule of the ADR guide allows the status field `superseded by` to cite a later ADR.
            if line.startswith("status: superseded by "):
                continue
            for cited in re.findall(r"\bADR-(\d{4})\b", line):
                if int(cited) > own:
                    fail(path, number, f"cites the later ADR-{cited}")


def check_links() -> None:
    link = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
    # The alternative text of an image can wrap over lines, so no single line holds the link.
    wrapped_image = re.compile(r"!(?=\[[^\]\n]*\n)" + LINK_TEXT + r"\(([^)\s]+)\)")
    code_span = re.compile(r"`[^`]*`")
    for path in markdown_files():
        text = read(path)
        targets = []
        for number, line in enumerate(text.split("\n"), 1):
            # A regular expression inside backticks can look like a link. Code is not a link.
            for target in link.findall(code_span.sub("``", line)):
                targets.append((number, target))
        code = code_spans(text)
        for match in wrapped_image.finditer(text):
            if inside(code, *match.span()):
                continue
            targets.append((text.count("\n", 0, match.start()) + 1, match.group(1)))
        for number, target in targets:
            if target.startswith(("http://", "https://", "#", "mailto:")):
                continue
            resolved = (path.parent / target.split("#", 1)[0]).resolve()
            if not resolved.exists():
                fail(path, number, f"link target does not exist: {target}")


URL = re.compile(r"\]\((?:[^()\s]|\([^()\s]*\))*\)|(?:https?|ftp)://[^\s)>\]]+")


def check_width() -> None:
    for path in markdown_files():
        lines = read(path).split("\n")
        for number, (line, in_fence) in enumerate(zip(lines, fenced(lines), strict=True), 1):
            if in_fence or line.startswith("|"):
                continue
            # Only the URL and the anchor are exempt, because a reader cannot wrap them. The prose
            # is not.
            width = len(ANCHOR_TAG.sub("", URL.sub("", line)))
            if width > MAX_WIDTH:
                fail(path, number, f"line is {width} characters, the limit is {MAX_WIDTH}")


# A range can link each end to its ADR.
ADR_LINK = r"(?:\]\([^)\s]*\))?"
ADR_RANGE = re.compile(
    rf"[`\[]?ADR-0001`?{ADR_LINK}\s+(?:to|through)\s+[`\[]?ADR-(\d{{4}})`?{ADR_LINK}"
)
DATE = re.compile(r"\b\d{4}-\d{2}-\d{2}\b")


def sentence_around(text: str, match: re.Match) -> str:
    """Return the sentence that holds the match, across line breaks."""
    ends = [m.end() for m in re.finditer(r"[.!?](?=\s)|\n\n", text[: match.start()])]
    start = ends[-1] if ends else 0
    after = re.search(r"[.!?](?=\s)|\n\n", text[match.end() :])
    stop = match.end() + after.end() if after else len(text)
    return text[start:stop]


def check_adr_ranges() -> None:
    """A range that starts at ADR-0001 claims the whole set, so it must reach the last ADR.

    An ADR file is exempt, because accepted content never changes. A sentence that carries a date
    is dated history, so it is exempt too. A range can wrap across a line break, or sit in
    backticks.
    """
    numbers = [int(p.name[:4]) for p in ADR_DIR.glob("0*.md")]
    if not numbers:
        return
    highest = max(numbers)
    for path in markdown_files():
        if path.parent == ADR_DIR and path.name[:1] == "0":
            continue
        text = read(path)
        for match in ADR_RANGE.finditer(text):
            end = int(match.group(1))
            if end >= highest:
                continue
            if DATE.search(sentence_around(text, match)):
                continue
            number = text.count("\n", 0, match.start()) + 1
            fail(path, number, f"range ADR-0001 to ADR-{end:04d} stops below ADR-{highest:04d}")


def check_adr_metadata() -> None:
    for path in sorted(ADR_DIR.glob("0*.md")):
        text = read(path)
        number = path.name[:4]
        title = re.search(r"^# ADR-(\d{4}):", text, re.M)
        if not title:
            fail(path, 1, "has no '# ADR-NNNN:' title")
        elif title.group(1) != number:
            fail(path, 1, f"title says ADR-{title.group(1)}, the file name says {number}")
        status = re.search(r"^status: (.+)$", text, re.M)
        if not status:
            fail(path, 1, "has no status in its front matter")
        else:
            value = status.group(1).strip()
            if value not in ADR_STATUS and not re.fullmatch(r"superseded by ADR-\d{4}", value):
                fail(path, 2, f"status {value!r} is not a lifecycle value")


def check_glossary_families() -> None:
    """The glossary table of identifier families names each family, as `PR-nnn`: one n a digit."""
    text = read(GLOSSARY) if GLOSSARY.exists() else ""
    section = re.search(r"^## Identifier families\n(.*?)(?=^## |\Z)", text, re.M | re.S)
    listed = dict(re.findall(r"^\| `([A-Z]+)-(n+)` \|", section.group(1) if section else "", re.M))
    for prefix, width in FAMILIES.items():
        want = f"{prefix}-{'n' * width}"
        if prefix not in listed:
            fail(GLOSSARY, None, f"the table of identifier families does not list {want}")
        elif len(listed[prefix]) != width:
            fail(
                GLOSSARY,
                None,
                f"the table of identifier families lists {prefix}-{listed[prefix]}, want {want}",
            )


def main() -> int:
    defined = defined_identifiers()
    if not defined["PR"] or not defined["W"]:
        print("check-docs: the requirements or the plan gave no identifiers", file=sys.stderr)
        return 2

    check_identifiers(defined)
    check_requirement_coverage(defined)
    check_adr_direction()
    check_links()
    check_width()
    check_adr_ranges()
    check_adr_metadata()
    check_glossary_families()
    check_citation_links(defined)

    if errors:
        print(f"check-docs: {len(errors)} problem(s)\n", file=sys.stderr)
        for error in errors:
            print(f"  {error}", file=sys.stderr)
        return 1

    counts = ", ".join(f"{len(v)} {k}" for k, v in sorted(defined.items()))
    print(f"check-docs: the documents are whole ({counts})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
