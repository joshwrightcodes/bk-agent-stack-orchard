# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Tests of scripts/check-docs.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_docs", pathlib.Path(__file__).with_name("check-docs.py")
)
check_docs = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_docs)


class CheckDocsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name).resolve()
        (self.root / "docs/adrs").mkdir(parents=True)
        for number in range(1, 15):
            (self.root / f"docs/adrs/{number:04d}-x.md").write_text("# x\n")
        self.saved = (check_docs.ROOT, check_docs.ADR_DIR, check_docs.GLOSSARY)
        check_docs.ROOT = self.root
        check_docs.ADR_DIR = self.root / "docs/adrs"
        check_docs.GLOSSARY = self.root / "docs/glossary.md"
        check_docs.errors.clear()
        self.addCleanup(self.restore)

    def restore(self) -> None:
        (check_docs.ROOT, check_docs.ADR_DIR, check_docs.GLOSSARY) = self.saved
        check_docs.errors.clear()

    def write(self, name: str, text: str) -> None:
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def test_a_file_in_images_is_checked(self) -> None:
        self.write("images/README.md", "x" * 101 + "\n")
        check_docs.check_width()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("images/README.md:1", check_docs.errors[0])

    def test_a_url_line_skips_only_the_url(self) -> None:
        url = "https://example.com/" + "a" * 150
        self.write("README.md", f"See [the page]({url}) for details.\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)
        self.write("README.md", f"{'word ' * 21}see {url} for details.\n")
        check_docs.check_width()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def test_a_link_target_with_parentheses_leaves_no_stray_bracket(self) -> None:
        target = "https://example.com/Foo_(bar)_" + "a" * 150
        self.write("README.md", f"{'word ' * 15}[the page]({target}) end.\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)

    def test_an_image_with_wrapped_text_has_its_target_checked(self) -> None:
        self.write("docs/a.svg", "<svg/>\n")
        self.write("docs/x.md", "Intro.\n\n![The first line\nand the second.](a.svg)\n")
        check_docs.check_links()
        self.assertEqual([], check_docs.errors)
        self.write("docs/x.md", "Intro.\n\n![The first line\nand the second.](b.svg)\n")
        check_docs.check_links()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("docs/x.md:3", check_docs.errors[0])

    def test_a_wrapped_image_in_code_is_not_checked(self) -> None:
        self.write("docs/x.md", "Intro.\n\n`![The first line\nand the second.](b.svg)`\n")
        check_docs.check_links()
        self.assertEqual([], check_docs.errors)

    def test_a_backtick_in_a_comment_does_not_hide_a_wrapped_image(self) -> None:
        self.write("docs/x.md", "<!-- ` -->\n\n![The first line\nand the second.](b.svg)\n\n`x`\n")
        check_docs.check_links()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("docs/x.md:3", check_docs.errors[0])

    def test_an_adr_range_must_match_the_files(self) -> None:
        self.write("README.md", "The set runs from ADR-0001 to ADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("ADR-0014", check_docs.errors[0])

    def test_a_full_adr_range_and_a_subset_range_pass(self) -> None:
        self.write(
            "README.md",
            "The set runs from ADR-0001 to ADR-0014.\nADR-0002 to ADR-0008 are accepted.\n",
        )
        check_docs.check_adr_ranges()
        self.assertEqual([], check_docs.errors)

    def test_a_wrapped_or_backticked_adr_range_is_checked(self) -> None:
        self.write("README.md", "The set runs from ADR-0001 to\nADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("README.md:1", check_docs.errors[0])
        check_docs.errors.clear()
        self.write("README.md", "The set runs from `ADR-0001` to `ADR-0009`.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def test_an_adr_file_and_a_dated_history_line_are_exempt_from_the_range_rule(self) -> None:
        self.write("docs/adrs/0014-y.md", "The set runs from ADR-0001 to ADR-0009.\n")
        self.write(
            "README.md", "On 2026-09-19 the header said ADR-0001 to ADR-0009. It is fixed now.\n"
        )
        check_docs.check_adr_ranges()
        self.assertEqual([], check_docs.errors)
        self.write(
            "README.md", "On 2026-09-19 the header was fixed. It says ADR-0001 to ADR-0009.\n"
        )
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def glossary(self, rows: list[str]) -> None:
        self.write(
            "docs/glossary.md",
            "# Glossary\n\n## Identifier families\n\n| Family | Meaning | Defined in |\n"
            "|---|---|---|\n"
            + "".join(f"| `{row}` | x | y |\n" for row in rows)
            + "\n## Next\n\n| `ZZ-n` | a table of another section | z |\n",
        )

    def all_families(self) -> list[str]:
        return [f"{prefix}-{'n' * width}" for prefix, width in check_docs.FAMILIES.items()]

    def test_a_glossary_that_lists_every_family_passes(self) -> None:
        self.glossary(self.all_families())
        check_docs.check_glossary_families()
        self.assertEqual([], check_docs.errors)

    def test_a_family_that_the_glossary_does_not_list_fails_with_its_name(self) -> None:
        self.glossary([row for row in self.all_families() if not row.startswith("QS-")])
        check_docs.check_glossary_families()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("QS-n", check_docs.errors[0])

    def test_a_family_with_the_wrong_number_of_digits_fails(self) -> None:
        self.glossary([row.replace("PR-nnn", "PR-nn") for row in self.all_families()])
        check_docs.check_glossary_families()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("PR-nnn", check_docs.errors[0])

    def test_the_citation_pattern_knows_each_family(self) -> None:
        for prefix, width in check_docs.FAMILIES.items():
            cited = f"{prefix}-{'1' * width}"
            self.assertEqual([cited], check_docs.CITATION.findall(f"see {cited}."), prefix)

    def test_a_fenced_block_inside_a_list_item_is_code_for_the_width_check(self) -> None:
        self.write("README.md", "1. Run:\n\n   ```sh\n   " + "x" * 120 + "\n   ```\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)

    def coverage(
        self,
        status: str,
        claim: str = "**Implements.** PR-001.\n",
        field: str = "* Source: an owner. Status: `{}`.\n",
    ) -> None:
        requirements = self.root / "docs/product/requirements.md"
        design = self.root / "docs/architecture/target-design.md"
        self.write(
            "docs/product/requirements.md",
            "**PR-001: One.**\n\n* Status: `accepted`.\n\n**PR-002: Two.**\n\n"
            + field.format(status),
        )
        self.write("docs/architecture/target-design.md", claim)
        saved = (check_docs.REQUIREMENTS, check_docs.DESIGN)
        check_docs.REQUIREMENTS, check_docs.DESIGN = requirements, design
        self.addCleanup(setattr, check_docs, "REQUIREMENTS", saved[0])
        self.addCleanup(setattr, check_docs, "DESIGN", saved[1])
        check_docs.check_requirement_coverage({"PR": {"PR-001", "PR-002"}})

    def test_a_requirement_that_no_implements_line_claims_fails(self) -> None:
        self.coverage("accepted")
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("PR-002", check_docs.errors[0])

    def test_a_requirement_out_of_scope_needs_no_implements_line(self) -> None:
        self.coverage("out of scope")
        self.assertEqual([], check_docs.errors)

    def test_an_unclaimed_requirement_still_fails_beside_an_out_of_scope_one(self) -> None:
        self.coverage("out of scope", claim="")
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("PR-001", check_docs.errors[0])

    def test_a_status_after_a_line_break_is_the_status(self) -> None:
        self.coverage("out of scope", field="* Source: an owner.\n  Status:\n  `{}`.\n")
        self.assertEqual([], check_docs.errors)

    def test_a_status_that_the_history_quotes_is_not_the_status(self) -> None:
        self.coverage(
            "accepted",
            field="* Status: `{}`.\n* History: it had the Status: `out of scope` once.\n",
        )
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("PR-002", check_docs.errors[0])

    def test_an_implements_line_that_claims_an_out_of_scope_requirement_fails(self) -> None:
        self.coverage("out of scope", claim="**Implements.** PR-001, PR-002.\n")
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("PR-002", check_docs.errors[0])

    def documents(self, strategy: str, citing: str) -> None:
        """A test strategy that defines T-001, and a design document that cites it."""
        for name in ("REQUIREMENTS", "BASELINE", "DESIGN", "STRATEGY", "PLAN"):
            self.addCleanup(setattr, check_docs, name, getattr(check_docs, name))
        check_docs.REQUIREMENTS = self.root / "docs/product/requirements.md"
        check_docs.BASELINE = self.root / "docs/architecture/system-design-baseline.md"
        check_docs.DESIGN = self.root / "docs/architecture/target-design.md"
        check_docs.STRATEGY = self.root / "docs/quality/test-strategy.md"
        check_docs.PLAN = self.root / "docs/delivery/implementation-plan.md"
        self.write("docs/quality/test-strategy.md", strategy)
        self.write("docs/architecture/target-design.md", citing)
        check_docs.check_citation_links(check_docs.defined_identifiers())

    def test_a_linked_citation_and_an_anchored_definition_pass(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "See [T-001](../quality/test-strategy.md#t-001) and [ADR-0001](../adrs/0001-x.md).\n",
        )
        self.assertEqual([], check_docs.errors)

    def test_a_bare_citation_fails_with_its_line(self) -> None:
        self.documents('| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n', "x\nSee T-001.\n")
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("target-design.md:2: cites T-001 without a link", check_docs.errors[0])

    def test_a_definition_without_its_anchor_fails(self) -> None:
        self.documents("| ID |\n|---|\n| T-001 |\n", "")
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("defines T-001 without its anchor", check_docs.errors[0])

    def test_a_link_to_the_wrong_anchor_fails(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "See [T-001](../quality/test-strategy.md#t-002).\n",
        )
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("not to its anchor", check_docs.errors[0])

    def test_code_a_heading_front_matter_and_a_comment_need_no_link(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "---\nnote: T-001\n---\n# T-001\n\n`T-001`\n\n<!-- T-001 -->\n\n```\nT-001\n```\n",
        )
        self.assertEqual([], check_docs.errors)

    def test_a_link_does_not_reach_over_a_blank_line(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "[unfinished\n\nSee T-001.\n\n[details](x.md)\n",
        )
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("target-design.md:3: cites T-001 without a link", check_docs.errors[0])

    def test_a_backtick_in_a_comment_does_not_hide_a_citation(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "<!-- ` -->\n\nSee T-001.\n\n`x`\n",
        )
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("target-design.md:3: cites T-001 without a link", check_docs.errors[0])

    def test_a_link_with_wrapped_text_around_a_citation_needs_no_other_link(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "![The flow of\nT-001](x.svg)\n",
        )
        self.assertEqual([], check_docs.errors)

    def test_a_reference_link_around_a_citation_needs_no_inline_link(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "See [T-001][t] and [T-001].\n\n[t]: ../quality/test-strategy.md#t-001\n"
            "[T-001]: ../quality/test-strategy.md#t-001\n",
        )
        self.assertEqual([], check_docs.errors)

    def test_a_reference_link_to_the_wrong_anchor_fails(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "See [T-001][t].\n\n[t]: ../quality/test-strategy.md#t-002\n",
        )
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("target-design.md:1: links T-001", check_docs.errors[0])
        self.assertIn("not to its anchor", check_docs.errors[0])

    def test_a_fence_closes_only_on_its_own_kind_of_fence(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "````md\n```\nT-001\n```\nT-001\n````\n\n~~~\n```\nT-001\n~~~\n",
        )
        self.assertEqual([], check_docs.errors)

    def test_a_link_in_code_or_a_comment_is_not_checked(self) -> None:
        self.documents(
            '| ID |\n|---|\n| <a id="t-001"></a>T-001 |\n',
            "Write `[T-001](x.md)`.\n\n<!-- [T-001](x.md) -->\n\n```\n[T-001](x.md)\n```\n",
        )
        self.assertEqual([], check_docs.errors)

    def test_a_long_line_in_a_tilde_fence_is_not_measured(self) -> None:
        self.write("README.md", "~~~\n" + "x" * 101 + "\n~~~\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)

    def test_a_linked_adr_range_is_still_checked(self) -> None:
        self.write(
            "docs/x.md",
            "[ADR-0001](adrs/0001-x.md) to [ADR-0013](adrs/0013-x.md).\n",
        )
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("stops below ADR-0014", check_docs.errors[0])

    def test_an_anchor_does_not_count_for_the_width(self) -> None:
        self.write("README.md", '* <a id="i-1"></a>**I-1:** ' + "x" * 88 + "\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)


if __name__ == "__main__":
    unittest.main()
