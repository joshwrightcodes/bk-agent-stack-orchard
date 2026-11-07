# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Tests of scripts/check-commits.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import pathlib
import sys
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    "check_commits", pathlib.Path(__file__).with_name("check-commits.py")
)
check_commits = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_commits)

DEPENDABOT = "dependabot[bot]"

# The message of Dependabot pull request #90, as it wrote it.
DEPENDABOT_MESSAGE = """build(deps): Bump google.golang.org/grpc in the go-minor-and-patch group

Bumps the go-minor-and-patch group with 1 update: [google.golang.org/grpc](https://github.com/grpc/grpc-go).


Updates `google.golang.org/grpc` from 1.83.2 to 1.84.0
- [Release notes](https://github.com/grpc/grpc-go/releases)
- [Commits](https://github.com/grpc/grpc-go/compare/v1.83.2...v1.84.0)

---
updated-dependencies:
- dependency-name: google.golang.org/grpc
  dependency-version: 1.84.0
  dependency-type: direct:production
  update-type: version-update:semver-minor
  dependency-group: go-minor-and-patch
...

Signed-off-by: dependabot[bot] <support@github.com>"""


RENOVATE = "renovate[bot]"

# The shape of a Renovate message for one module: a lowercase description, and a subject that the
# module path makes longer than 72 characters.
RENOVATE_MESSAGE = (
    "build(deps): update module go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
    " to v1.40.0"
)


class CheckTest(unittest.TestCase):
    def test_a_message_of_dependabot_passes(self) -> None:
        self.assertEqual(check_commits.check("r", DEPENDABOT_MESSAGE, DEPENDABOT), [])

    def test_a_message_of_renovate_passes(self) -> None:
        self.assertEqual(check_commits.check("r", RENOVATE_MESSAGE, RENOVATE), [])

    def test_a_person_keeps_the_subject_inside_the_limit(self) -> None:
        problems = check_commits.check("r", RENOVATE_MESSAGE, "Josh")
        self.assertTrue(any("subject is" in p for p in problems), problems)

    def test_a_person_writes_the_description_in_lower_case(self) -> None:
        problems = check_commits.check("r", "fix(orchard): Trust the certificate", "Josh")
        self.assertTrue(any("capital letter" in p for p in problems), problems)

    def test_a_bot_still_uses_a_type_of_the_list(self) -> None:
        problems = check_commits.check("r", "deps: Bump grpc", DEPENDABOT)
        self.assertTrue(any("Conventional Commits" in p for p in problems), problems)

    def test_a_bot_still_ends_without_a_full_stop(self) -> None:
        problems = check_commits.check("r", "build(deps): Bump grpc.", DEPENDABOT)
        self.assertTrue(any("full stop" in p for p in problems), problems)

    def test_a_person_keeps_the_body_inside_the_limit(self) -> None:
        message = "docs: say more\n\n" + "a" * 80
        problems = check_commits.check("r", message, "Josh")
        self.assertTrue(any("body line" in p for p in problems), problems)

    def test_a_person_uses_ci_for_workflow_changes(self) -> None:
        problems = check_commits.check("r", "fix(ci): start the job again", "Josh")
        self.assertTrue(any("Use ci:" in p for p in problems), problems)
        self.assertEqual(check_commits.check("r", "ci(ci): start the job again", "Josh"), [])
        self.assertEqual(check_commits.check("r", "fix(runner): start the job", "Josh"), [])
        self.assertEqual(check_commits.check("r", "fix(reaper): delete the VM", "Josh"), [])

    def test_the_ci_rule_covers_a_breaking_fix_and_spares_other_scopes(self) -> None:
        problems = check_commits.check("r", "fix(ci)!: start the job again", "Josh")
        self.assertTrue(any("Use ci:" in p for p in problems), problems)
        self.assertEqual(check_commits.check("r", "fix(cio): start the job again", "Josh"), [])

    def test_a_person_signs_off(self) -> None:
        email = "josh@example.org"
        signed = "docs: say more\n\nSigned-off-by: Josh <JOSH@example.org>"
        self.assertEqual(check_commits.check_signoff("r", signed, email), [])

        unsigned = check_commits.check_signoff("r", "docs: say more", email)
        self.assertTrue(any("Signed-off-by" in p for p in unsigned), unsigned)

        other = "docs: say more\n\nSigned-off-by: Ann <ann@example.org>"
        mismatch = check_commits.check_signoff("r", other, email)
        self.assertTrue(any("author email" in p for p in mismatch), mismatch)

        for bot in (
            "49699333+dependabot[bot]@users.noreply.github.com",
            "41898282+github-actions[bot]@users.noreply.github.com",
        ):
            self.assertEqual(check_commits.check_signoff("r", "build: bump", bot), [])

    def test_the_pr_title_is_checked(self) -> None:
        self.assertEqual(check_commits.check_title("feat(reaper): delete the VM"), [])
        long = "feat(reaper): " + "a" * 60
        problems = check_commits.check_title(long)
        self.assertTrue(any("65" in p for p in problems), problems)
        bad = check_commits.check_title("Update things")
        self.assertTrue(any("Conventional Commits" in p for p in bad), bad)

    def test_a_sign_off_in_the_body_does_not_count(self) -> None:
        message = (
            "docs: say more\n\nSigned-off-by: Josh <josh@example.org>\n\nMore text after the line."
        )
        problems = check_commits.check_signoff("r", message, "josh@example.org")
        self.assertTrue(any("Signed-off-by" in p for p in problems), problems)

    def test_several_sign_offs_pass_with_the_author_among_them(self) -> None:
        message = (
            "docs: say more\n\nCo-authored-by: Ann <ann@example.org>\n"
            "Signed-off-by: Ann <ann@example.org>\n"
            "Signed-off-by: Josh <josh@example.org>"
        )
        self.assertEqual(check_commits.check_signoff("r", message, "josh@example.org"), [])

    def test_the_title_mode_runs_through_main(self) -> None:
        for length, code in ((65, 0), (66, 1)):
            title = "feat: " + "a" * (length - len("feat: "))
            with (
                mock.patch.object(sys, "argv", ["check-commits.py", "--title", title]),
                contextlib.redirect_stdout(io.StringIO()),
                contextlib.redirect_stderr(io.StringIO()),
            ):
                self.assertEqual(check_commits.main(), code, length)

    def test_a_usage_error_exits_2_and_reads_no_range(self) -> None:
        for argv in (["--title"], ["origin/main", "--title", "feat: x"], ["--base", "x"]):
            with (
                self.subTest(argv=argv),
                mock.patch.object(check_commits, "commits") as commits,
                contextlib.redirect_stderr(io.StringIO()),
                self.assertRaises(SystemExit) as raised,
            ):
                check_commits.main(argv)
            self.assertEqual(raised.exception.code, 2)
            commits.assert_not_called()

    def test_git_says_why_it_cannot_read_the_range(self) -> None:
        error = check_commits.subprocess.CalledProcessError(
            128, ["git"], stderr="fatal: bad revision 'nope..HEAD'\n"
        )
        with (
            mock.patch.object(check_commits, "commits", side_effect=error),
            contextlib.redirect_stderr(io.StringIO()) as err,
        ):
            self.assertEqual(check_commits.main(["nope"]), 2)
        self.assertIn("bad revision", err.getvalue())

    def test_a_base_that_looks_like_an_option_stays_a_revision(self) -> None:
        with mock.patch.object(check_commits.subprocess, "run") as run:
            run.return_value.stdout = ""
            check_commits.commits("--output=x")
        self.assertEqual(
            run.call_args.args[0][:4], ["git", "rev-list", "--no-merges", "--end-of-options"]
        )

    def test_only_a_breaking_change_footer_needs_the_mark(self) -> None:
        footer = "feat: drop a flag\n\nBody.\n\nBREAKING CHANGE: the flag is gone"
        problems = check_commits.check("r", footer)
        self.assertTrue(any("BREAKING CHANGE" in p for p in problems), problems)
        hyphen = "feat: drop a flag\n\nBREAKING-CHANGE: the flag is gone"
        self.assertTrue(check_commits.check("r", hyphen))
        marked = "feat!: drop a flag\n\nBREAKING CHANGE: gone"
        self.assertEqual(check_commits.check("r", marked), [])
        prose = (
            "docs: explain\n\nA BREAKING CHANGE footer needs a mark.\n\nSigned-off-by: A <a@b.c>"
        )
        self.assertEqual(check_commits.check("r", prose), [])


class ToolCoAuthorTests(unittest.TestCase):
    def found(self, trailers: str) -> bool:
        return check_commits.has_tool_coauthor(f"docs: say more\n\nBody.\n\n{trailers}")

    def test_a_tool_is_found_by_its_name_or_its_email(self) -> None:
        for trailer in (
            "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>",
            "Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>",
            "co-authored-by: Claude Code <someone@example.org>",
            "Co-authored-by: Copilot <198982749+Copilot@users.noreply.github.com>",
            "Co-authored-by: Cursor Agent <cursoragent@cursor.com>",
            "Co-authored-by: Codex <noreply@openai.com>",
            "Co-authored-by: Gemini CLI <noreply@google.com>",
            "Co-authored-by: ChatGPT <noreply@chatgpt.com>",
            "Co-authored-by: OpenAI Codex <someone@example.org>",
            "Co-authored-by: Claude 3.5 Sonnet <someone@example.org>",
            'Co-authored-by: "Claude Code" <someone@example.org>',
            "Co-authored-by: Claude Code",
            "Co-authored-by: devin-ai-integration[bot] <1+devin@users.noreply.github.com>",
            "Co-authored-by: google-labs-jules[bot] <1+jules@users.noreply.github.com>",
            "Co-authored-by: gemini-code-assist[bot] <1+g@users.noreply.github.com>",
            "Co-authored-by: chatgpt-codex-connector[bot] <1+c@users.noreply.github.com>",
            "Co-authored-by: claude[bot] <1+claude@users.noreply.github.com>",
            "Co-authored-by: Claude <noreply@anthropic.com>",
            "Co-authored-by: aider (gpt) <aider@aider.chat>",
            "Co-authored-by: Helper <noreply@anthropic.com>",
            "Co-authored-by: Helper <198982749+Copilot@users.noreply.github.com>",
        ):
            self.assertTrue(self.found(trailer), trailer)

    def test_a_person_or_a_bot_is_not_a_tool(self) -> None:
        for trailer in (
            "Co-authored-by: Ann Lee <ann@example.org>",
            "Co-authored-by: Claude Monet <claude@example.org>",
            "Co-authored-by: Jules Verne <jules@example.org>",
            "Co-authored-by: Claude <claude@example.org>",
            "Co-authored-by: Devin <devin@example.org>",
            "Co-authored-by: Jules <jules@example.org>",
            "Co-authored-by: Cursor <cursor@example.org>",
            "Co-authored-by: Gemini <gemini@example.org>",
            "Co-authored-by: Ann Lee",
            "Co-authored-by: renovate[bot] <29139614+renovate[bot]@users.noreply.github.com>",
            "Co-authored-by: dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>",
        ):
            self.assertFalse(self.found(trailer), trailer)

    def test_only_the_trailer_block_counts(self) -> None:
        message = (
            "docs: say more\n\n"
            "Co-authored-by: Claude Code <noreply@anthropic.com>\n\n"
            "Signed-off-by: Ann <ann@example.org>"
        )
        self.assertFalse(check_commits.has_tool_coauthor(message))
        self.assertFalse(check_commits.has_tool_coauthor("docs: say more"))

    def test_the_commit_mode_accepts_a_tool_co_author(self) -> None:
        message = (
            "docs: say more\n\n"
            "Signed-off-by: A <a@b.c>\n"
            "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"
        )
        with (
            mock.patch.object(
                check_commits, "commits", return_value=[("abc", message, "A", "a@b.c")]
            ),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(check_commits.main(["origin/main"]), 0)


class DisclosureTests(unittest.TestCase):
    def body(self, section: str) -> str:
        return (
            f"## What changes\n\nA change.\n\n## AI tools\n\n{section}\n\n## Traceability\n\nnone"
        )

    def test_a_tool_or_none_is_a_disclosure(self) -> None:
        for section in ("Claude Code drafted the tests.", "none", "None.", "NONE"):
            self.assertEqual(check_commits.check_disclosure(self.body(section), []), [], section)

    def test_a_missing_section_fails(self) -> None:
        for body in ("", "## What changes\n\nA change.", "## AI toolsets\n\nnone"):
            problems = check_commits.check_disclosure(body, [])
            self.assertEqual(len(problems), 1, body)
            self.assertIn("AI tools", problems[0])

    def test_a_blank_section_or_a_template_comment_fails(self) -> None:
        comment = '<!-- Name each tool and say what it did, or write "none". -->'
        for section in ("", "   ", comment, f"{comment}\n\n<!-- second\nline -->"):
            problems = check_commits.check_disclosure(self.body(section), [])
            self.assertEqual(len(problems), 1, section)
            self.assertIn("empty", problems[0])

    def test_the_section_stops_at_the_next_heading(self) -> None:
        body = "## AI tools\n\n## Traceability\n\nClaude Code wrote it."
        self.assertIn("empty", check_commits.check_disclosure(body, [])[0])

    def test_any_heading_ends_the_section(self) -> None:
        for heading in ("# Next", "### Next", " ## Next", "   ## Next"):
            body = f"## AI tools\n\n{heading}\n\nClaude Code wrote it."
            self.assertIn("empty", check_commits.check_disclosure(body, [])[0], heading)

    def test_a_section_with_a_tool_ignores_a_later_heading(self) -> None:
        body = "## AI tools\n\nClaude Code wrote it.\n\n### Next\n\nnone"
        self.assertEqual(check_commits.check_disclosure(body, ["abc"]), [])

    def test_a_description_from_the_github_website_has_crlf_line_endings(self) -> None:
        body = self.body("none").replace("\n", "\r\n")
        self.assertEqual(check_commits.check_disclosure(body, []), [])
        blank = self.body("<!-- only -->").replace("\n", "\r\n")
        self.assertIn("empty", check_commits.check_disclosure(blank, [])[0])

    def test_none_contradicts_a_tool_co_author(self) -> None:
        for section in ("none", "None.", "NONE", '"none"', "`none`", "**none**", "*None*."):
            problems = check_commits.check_disclosure(self.body(section), ["abc12345"])
            self.assertEqual(len(problems), 1, section)
            self.assertIn("abc12345", problems[0])
        self.assertEqual(
            check_commits.check_disclosure(self.body("Claude Code."), ["abc12345"]), []
        )

    def test_the_disclosure_mode_reads_the_description_from_the_environment(self) -> None:
        trailer = (
            "docs: say more\n\n"
            "Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>\n"
            "Signed-off-by: A <a@b.c>"
        )
        plain = "docs: say more\n\nSigned-off-by: A <a@b.c>"
        cases = (
            (self.body("none"), [("abc", plain, "A", "a@b.c")], 0),
            (self.body("none"), [("abc", trailer, "A", "a@b.c")], 1),
            (self.body("Claude Code."), [("abc", trailer, "A", "a@b.c")], 0),
            ("no section", [("abc", plain, "A", "a@b.c")], 1),
        )
        for body, found, want in cases:
            with (
                mock.patch.dict(check_commits.os.environ, {"PR_BODY": body}),
                mock.patch.object(check_commits, "commits", return_value=found),
                contextlib.redirect_stderr(io.StringIO()),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                self.assertEqual(check_commits.main(["--disclosure", "origin/main"]), want, body)

    def test_only_the_named_accounts_skip_the_disclosure(self) -> None:
        plain = "docs: say more\n\nSigned-off-by: A <a@b.c>"
        cases = (
            ("renovate[bot]", 0),
            ("dependabot[bot]", 0),
            ("bk-agent-stack-orchard-release[bot]", 0),
            ("claude[bot]", 1),
            ("copilot-swe-agent[bot]", 1),
            ("github-actions[bot]", 1),
            ("ann", 1),
            ("", 1),
        )
        for author, want in cases:
            with (
                mock.patch.dict(
                    check_commits.os.environ, {"PR_BODY": "no section", "PR_AUTHOR": author}
                ),
                mock.patch.object(
                    check_commits, "commits", return_value=[("a", plain, "A", "a@b.c")]
                ),
                contextlib.redirect_stderr(io.StringIO()),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                self.assertEqual(check_commits.main(["--disclosure", "origin/main"]), want, author)

    def test_the_disclosure_mode_needs_the_description(self) -> None:
        with (
            mock.patch.dict(check_commits.os.environ, clear=True),
            contextlib.redirect_stderr(io.StringIO()),
        ):
            self.assertEqual(check_commits.main(["--disclosure", "origin/main"]), 2)


if __name__ == "__main__":
    unittest.main()
