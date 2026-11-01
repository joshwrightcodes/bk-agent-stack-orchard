#!/usr/bin/env python3
"""Check that the document set stays whole.

The documents of this project carry identifiers, and the direction of the set runs from the
product requirements to the design. This script fails when that structure breaks. It reads only;
it changes nothing.

Checks:

  1. every identifier that a document cites exists;
  2. every requirement appears in an "Implements" line of the target design;
  3. no ADR cites an ADR with a higher number;
  4. every relative link resolves;
  5. every line outside a table row or a fenced block is 100 characters or less;
  6. each ADR file name matches its title, and its status is a lifecycle value;
  7. no "ADR-0001 to ADR-NNNN" range stops below the highest ADR that exists.
"""

from __future__ import annotations

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

# Generated files. Their producer owns their shape.
GENERATED = {ROOT / "docs/architecture/c4.md"}

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
    files += [ROOT / name for name in ("README.md", "CONTRIBUTING.md", "SECURITY.md",
                                       "CODE_OF_CONDUCT.md", ".github/pull_request_template.md")]
    return sorted({f for f in files if f.exists()})


def read(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8")


def find_all(path: pathlib.Path, pattern: str) -> set[str]:
    return set(re.findall(pattern, read(path), re.M)) if path.exists() else set()


def defined_identifiers() -> dict[str, set[str]]:
    adrs = {f"ADR-{p.name[:4]}" for p in ADR_DIR.glob("0*.md")}
    return {
        "PR": find_all(REQUIREMENTS, r"^\*\*(PR-\d{3}):"),
        "driver": find_all(BASELINE, r"^\| ((?:BD|SC|QA|DR|SE|OP|CO)-\d{2}) \|"),
        "QS": find_all(BASELINE, r"^\| (QS-\d) \|"),
        "T": find_all(STRATEGY, r"^\| (T-\d{3}) \|"),
        "R": find_all(STRATEGY, r"^\| (R-\d{2}) \|"),
        "I": find_all(STRATEGY, r"^\* \*\*(I-\d):\*\*"),
        "E": find_all(STRATEGY, r"^\* \*\*(E-\d):\*\*"),
        "O": find_all(STRATEGY, r"^\| (O-\d) \|"),
        "W": find_all(PLAN, r"^\| (W-\d{2}) \|"),
        "D": find_all(PLAN, r"^\| (D-\d) \|"),
        "ADR": adrs,
    }


CITATION = re.compile(
    r"\b(PR-\d{3}|(?:BD|SC|QA|DR|SE|OP|CO)-\d{2}|QS-\d|T-\d{3}|R-\d{2}|I-\d|E-\d|O-\d|W-\d{2}"
    r"|D-\d|ADR-\d{4})\b"
)


def check_identifiers(defined: dict[str, set[str]]) -> None:
    known = set().union(*defined.values())
    for path in markdown_files():
        for number, line in enumerate(read(path).split("\n"), 1):
            for cited in CITATION.findall(line):
                if cited not in known:
                    fail(path, number, f"cites {cited}, which no document defines")


def check_requirement_coverage(defined: dict[str, set[str]]) -> None:
    implements = "\n".join(
        line for line in read(DESIGN).split("\n") if line.startswith("**Implements.**")
    )
    for requirement in sorted(defined["PR"]):
        if requirement not in implements:
            fail(DESIGN, None, f"no Implements line claims {requirement}")


def check_adr_direction() -> None:
    for path in sorted(ADR_DIR.glob("0*.md")):
        own = int(path.name[:4])
        for number, line in enumerate(read(path).split("\n"), 1):
            for cited in re.findall(r"\bADR-(\d{4})\b", line):
                if int(cited) > own:
                    fail(path, number, f"cites the later ADR-{cited}")


def check_links() -> None:
    link = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
    code_span = re.compile(r"`[^`]*`")
    for path in markdown_files():
        for number, line in enumerate(read(path).split("\n"), 1):
            # A regular expression inside backticks can look like a link. Code is not a link.
            for target in link.findall(code_span.sub("``", line)):
                if target.startswith(("http://", "https://", "#", "mailto:")):
                    continue
                resolved = (path.parent / target.split("#", 1)[0]).resolve()
                if not resolved.exists():
                    fail(path, number, f"link target does not exist: {target}")


URL = re.compile(r"\]\((?:[^()\s]|\([^()\s]*\))*\)|(?:https?|ftp)://[^\s)>\]]+")


def check_width() -> None:
    for path in markdown_files():
        if path in GENERATED:
            continue
        in_fence = False
        for number, line in enumerate(read(path).split("\n"), 1):
            if line.lstrip().startswith("```"):
                in_fence = not in_fence
                continue
            if in_fence or line.startswith("|"):
                continue
            # Only the URL is exempt, because a reader cannot wrap it. The prose is not.
            width = len(URL.sub("", line))
            if width > MAX_WIDTH:
                fail(path, number, f"line is {width} characters, the limit is {MAX_WIDTH}")


ADR_RANGE = re.compile(r"`?ADR-0001`?\s+(?:to|through)\s+`?ADR-(\d{4})`?")
DATE = re.compile(r"\b\d{4}-\d{2}-\d{2}\b")


def sentence_around(text: str, match: re.Match) -> str:
    """Return the sentence that holds the match, across line breaks."""
    ends = [m.end() for m in re.finditer(r"[.!?](?=\s)|\n\n", text[:match.start()])]
    start = ends[-1] if ends else 0
    after = re.search(r"[.!?](?=\s)|\n\n", text[match.end():])
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
