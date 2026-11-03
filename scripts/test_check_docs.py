# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests of scripts/check-docs.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_docs", pathlib.Path(__file__).with_name("check-docs.py"))
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
        self.saved = (check_docs.ROOT, check_docs.ADR_DIR, check_docs.GENERATED,
                      check_docs.GLOSSARY, check_docs.TELEMETRY_PAGE, check_docs.METRICS_CODE,
                      check_docs.REAPER_CODE)
        check_docs.ROOT = self.root
        check_docs.ADR_DIR = self.root / "docs/adrs"
        check_docs.GENERATED = set()
        check_docs.GLOSSARY = self.root / "docs/glossary.md"
        check_docs.TELEMETRY_PAGE = self.root / "docs/user/telemetry.md"
        check_docs.METRICS_CODE = self.root / "internal/telemetry/metrics.go"
        check_docs.REAPER_CODE = self.root / "internal/reaper/decide.go"
        check_docs.errors.clear()
        self.addCleanup(self.restore)

    def restore(self) -> None:
        (check_docs.ROOT, check_docs.ADR_DIR, check_docs.GENERATED,
         check_docs.GLOSSARY, check_docs.TELEMETRY_PAGE, check_docs.METRICS_CODE,
         check_docs.REAPER_CODE) = self.saved
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

    def test_an_adr_range_must_match_the_files(self) -> None:
        self.write("README.md", "The set runs from ADR-0001 to ADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("ADR-0014", check_docs.errors[0])

    def test_a_full_adr_range_and_a_subset_range_pass(self) -> None:
        self.write("README.md",
                   "The set runs from ADR-0001 to ADR-0014.\n"
                   "ADR-0002 to ADR-0008 are accepted.\n")
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
        self.write("README.md",
                   "On 2026-09-19 the header said ADR-0001 to ADR-0009. It is fixed now.\n")
        check_docs.check_adr_ranges()
        self.assertEqual([], check_docs.errors)
        self.write("README.md", "On 2026-09-19 the header was fixed. It says ADR-0001 to ADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def glossary(self, rows: list[str]) -> None:
        self.write("docs/glossary.md",
                   "# Glossary\n\n## Identifier families\n\n| Family | Meaning | Defined in |\n"
                   "|---|---|---|\n" + "".join(f"| `{row}` | x | y |\n" for row in rows)
                   + "\n## Next\n\n| `ZZ-n` | a table of another section | z |\n")

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

    def telemetry_code(self) -> None:
        self.write("internal/telemetry/metrics.go",
                   'const prefix = "bkx."\n'
                   'const (\n\tattributeProfile = prefix + "profile"\n)\n'
                   'const (\n\tSlotsFree  SlotState = "free"\n)\n'
                   'm.c, err = meter.Int64Counter(prefix+"job.failed",\n'
                   'm.w, err = meter.Int64ObservableUpDownCounter("cicd.worker.count",\n')
        self.write("internal/reaper/decide.go", 'const (\n\tReasonOrphan = "orphan"\n)\n')

    def test_a_telemetry_page_that_names_everything_passes(self) -> None:
        self.telemetry_code()
        self.write("docs/user/telemetry.md",
                   "`bkx.job.failed` `bkx.profile` `cicd.worker.count` (`free`) `orphan`\n")
        check_docs.check_telemetry_page()
        self.assertEqual([], check_docs.errors)

    def test_a_name_that_the_telemetry_page_lacks_fails_with_the_name(self) -> None:
        self.telemetry_code()
        self.write("docs/user/telemetry.md", "`bkx.job.failed` `bkx.profile` `cicd.worker.count`\n")
        check_docs.check_telemetry_page()
        self.assertEqual(2, len(check_docs.errors), check_docs.errors)
        self.assertIn("`free`", check_docs.errors[0])
        self.assertIn("`orphan`", check_docs.errors[1])

    def test_a_fenced_block_inside_a_list_item_is_code_for_the_width_check(self) -> None:
        self.write("README.md", "1. Run:\n\n   ```sh\n   " + "x" * 120 + "\n   ```\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)


if __name__ == "__main__":
    unittest.main()
