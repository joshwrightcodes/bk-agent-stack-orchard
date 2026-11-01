"""Tests of scripts/check-commits.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_commits", pathlib.Path(__file__).with_name("check-commits.py"))
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
    " to v1.40.0")


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

    def test_a_person_signs_off(self) -> None:
        email = "josh@example.org"
        signed = "docs: say more\n\nSigned-off-by: Josh <JOSH@example.org>"
        self.assertEqual(check_commits.check_signoff("r", signed, email), [])

        unsigned = check_commits.check_signoff("r", "docs: say more", email)
        self.assertTrue(any("Signed-off-by" in p for p in unsigned), unsigned)

        other = "docs: say more\n\nSigned-off-by: Ann <ann@example.org>"
        mismatch = check_commits.check_signoff("r", other, email)
        self.assertTrue(any("author email" in p for p in mismatch), mismatch)

        for bot in ("49699333+dependabot[bot]@users.noreply.github.com",
                    "41898282+github-actions[bot]@users.noreply.github.com"):
            self.assertEqual(check_commits.check_signoff("r", "build: bump", bot), [])

    def test_the_pr_title_is_checked(self) -> None:
        self.assertEqual(check_commits.check_title("feat(reaper): delete the VM"), [])
        long = "feat(reaper): " + "a" * 60
        problems = check_commits.check_title(long)
        self.assertTrue(any("65" in p for p in problems), problems)
        bad = check_commits.check_title("Update things")
        self.assertTrue(any("Conventional Commits" in p for p in bad), bad)


if __name__ == "__main__":
    unittest.main()
