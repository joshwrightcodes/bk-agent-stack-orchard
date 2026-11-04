# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests of the vex and sign phases of scripts/release-publish.sh.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'

The tests source the script, which defines the phases and runs nothing else, and run the sign
phase in a directory that holds the files of the build job. Stubs of docker, cosign, and gh, first
on PATH, give the image index and record each call. The tests check that the phase signs and
attests exactly linux/amd64 and linux/arm64, and that it signs nothing when the files of the build
job are not what it expects.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("release-publish.sh")

AMD64 = "sha256:" + "a" * 64
ARM64 = "sha256:" + "b" * 64
INDEX = "sha256:" + "c" * 64
CHART = "sha256:" + "d" * 64

STUBS = {
    "docker": '#!/bin/sh\necho "docker $*" >> "$LOG"\ncat "$INDEX_FILE"\n',
    # sign-blob writes the bundle that --bundle names, as cosign does.
    "cosign": (
        '#!/bin/sh\necho "cosign $*" >> "$LOG"\nprev=""\n'
        'for a in "$@"; do [ "$prev" = "--bundle" ] && : > "$a"; prev="$a"; done\nexit 0\n'
    ),
    "gh": '#!/bin/sh\necho "gh $*" >> "$LOG"\n',
}


def manifest(digest: str, os_: str, arch: str) -> dict[str, object]:
    return {"digest": digest, "platform": {"os": os_, "architecture": arch}}


GOOD_INDEX = {
    "manifests": [
        manifest(ARM64, "linux", "arm64"),
        manifest(AMD64, "linux", "amd64"),
        manifest("sha256:" + "e" * 64, "unknown", "unknown"),
    ]
}


class SignTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(subprocess.run, ["rm", "-rf", str(self.tmp)], check=True)
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        for name, text in STUBS.items():
            stub = self.bin / name
            stub.write_text(text)
            stub.chmod(0o755)
        self.work = self.tmp / "work"
        for directory in ("dist", "build/sbom", "build/chart"):
            (self.work / directory).mkdir(parents=True)
        lines = []
        for name in (
            "x_linux_amd64.tar.gz",
            "x_linux_amd64.tar.gz.sbom.json",
            "bk-agent-stack-orchard_0.1.0.openvex.json",
        ):
            data = name.encode()
            (self.work / "dist" / name).write_bytes(data)
            lines.append(f"{hashlib.sha256(data).hexdigest()}  {name}\n")
        (self.work / "dist/checksums.txt").write_text("".join(lines))
        for arch in ("amd64", "arm64"):
            (self.work / f"build/sbom/image-linux-{arch}.spdx.json").write_text("{}")
        (self.work / "build/chart/bk-agent-stack-orchard-0.1.0.tgz").write_text("chart")
        (self.work / "build/release.env").write_text(
            f"IMAGE_DIGEST={INDEX}\nCHART_DIGEST={CHART}\n"
        )
        self.index(GOOD_INDEX)

    def index(self, document: dict[str, object]) -> None:
        (self.tmp / "index.json").write_text(json.dumps(document))

    def sign(self) -> tuple[subprocess.CompletedProcess[str], list[str]]:
        log = self.tmp / "log"
        env = {
            **os.environ,
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "LOG": str(log),
            "INDEX_FILE": str(self.tmp / "index.json"),
        }
        result = subprocess.run(
            ["bash", "-c", f'source "{SCRIPT}"; tag=v0.1.0; sign'],
            cwd=self.work,
            env=env,
            capture_output=True,
            text=True,
        )
        calls = log.read_text().splitlines() if log.exists() else []
        return result, calls

    def attested(self, calls: list[str]) -> list[str]:
        return [c.split()[-1] for c in calls if c.startswith("cosign attest ")]

    def test_it_signs_and_attests_exactly_amd64_and_arm64(self) -> None:
        result, calls = self.sign()
        self.assertEqual(result.returncode, 0, result.stderr)
        image = "ghcr.io/joshwrightcodes/bk-agent-stack-orchard"
        self.assertEqual(self.attested(calls), [f"{image}@{AMD64}", f"{image}@{ARM64}"])
        self.assertIn(f"cosign sign --yes --recursive {image}@{INDEX}", calls)
        chart = "ghcr.io/joshwrightcodes/charts/bk-agent-stack-orchard"
        self.assertIn(f"cosign sign --yes {chart}@{CHART}", calls)
        upload = next(c for c in calls if c.startswith("gh release upload v0.1.0 dist/"))
        for name in (
            "x_linux_amd64.tar.gz",
            "x_linux_amd64.tar.gz.sbom.json",
            "checksums.txt",
            "checksums.txt.sigstore.json",
        ):
            self.assertIn(f"dist/{name}", upload.split())

    def test_an_index_with_one_platform_attests_nothing(self) -> None:
        self.index({"manifests": [manifest(AMD64, "linux", "amd64")]})
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exactly amd64 and arm64", result.stderr)
        self.assertEqual(self.attested(calls), [])

    def test_an_index_with_no_linux_platform_fails(self) -> None:
        self.index({"manifests": [manifest(AMD64, "unknown", "unknown")]})
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.attested(calls), [])

    def test_an_index_with_a_third_platform_fails(self) -> None:
        third = manifest("sha256:" + "f" * 64, "linux", "s390x")
        self.index({"manifests": GOOD_INDEX["manifests"] + [third]})
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.attested(calls), [])

    def test_an_archive_that_checksums_txt_does_not_name_stops_before_any_signature(self) -> None:
        (self.work / "dist/extra.tar.gz").write_text("extra")
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse([c for c in calls if c.startswith(("cosign", "gh"))], calls)

    def test_a_changed_archive_stops_before_any_signature(self) -> None:
        (self.work / "dist/x_linux_amd64.tar.gz").write_text("changed")
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse([c for c in calls if c.startswith(("cosign", "gh"))], calls)

    def test_the_digest_file_is_read_and_never_run(self) -> None:
        marker = self.tmp / "ran"
        (self.work / "build/release.env").write_text(
            f"IMAGE_DIGEST=$(touch {marker})\ntouch {marker}\nCHART_DIGEST={CHART}\n"
        )
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())
        self.assertFalse([c for c in calls if c.startswith(("cosign", "gh"))], calls)


def openvex(*statements: dict[str, object]) -> str:
    return json.dumps(
        {
            "@context": "https://openvex.dev/ns/v0.2.0",
            "author": "Unknown Author",
            "statements": list(statements),
        }
    )


def statement(vulnerability: str, status: str) -> dict[str, object]:
    return {"vulnerability": {"name": vulnerability}, "status": status}


class VexTest(unittest.TestCase):
    """The vex phase writes the OpenVEX document of the release and lists it in checksums.txt."""

    NAME = "bk-agent-stack-orchard_0.1.0.openvex.json"

    def vex(
        self, output: str, status: int = 0
    ) -> tuple[subprocess.CompletedProcess[str], pathlib.Path]:
        tmp = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(subprocess.run, ["rm", "-rf", str(tmp)], check=True)
        bin_dir = tmp / "bin"
        bin_dir.mkdir()
        (tmp / "out.json").write_text(output)
        stub = bin_dir / "govulncheck"
        stub.write_text(
            f'#!/bin/sh\necho "$*" > "{tmp}/args"\ncat "{tmp}/out.json"\nexit {status}\n'
        )
        stub.chmod(0o755)
        work = tmp / "work"
        (work / "dist").mkdir(parents=True)
        (work / "dist/checksums.txt").write_text(f"{'0' * 64}  x_linux_amd64.tar.gz\n")
        env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}
        result = subprocess.run(
            ["bash", "-c", f'source "{SCRIPT}"; tag=v0.1.0; vex'],
            cwd=work,
            env=env,
            capture_output=True,
            text=True,
        )
        self.assertEqual((tmp / "args").read_text().split()[:2], ["-format", "openvex"])
        return result, work

    def test_the_document_is_written_and_listed_in_the_checksums(self) -> None:
        result, work = self.vex(openvex())
        self.assertEqual(result.returncode, 0, result.stderr)
        document = work / "dist" / self.NAME
        self.assertEqual(
            json.loads(document.read_text())["author"],
            "the release workflow of joshwrightcodes/bk-agent-stack-orchard",
        )
        digest = hashlib.sha256(document.read_bytes()).hexdigest()
        self.assertIn(f"{digest}  {self.NAME}", (work / "dist/checksums.txt").read_text())

    def test_a_vulnerability_that_the_code_does_not_reach_is_recorded(self) -> None:
        result, work = self.vex(openvex(statement("GO-2026-0001", "not_affected")))
        self.assertEqual(result.returncode, 0, result.stderr)
        statements = json.loads((work / "dist" / self.NAME).read_text())["statements"]
        self.assertEqual([s["status"] for s in statements], ["not_affected"])

    def test_a_vulnerability_that_the_code_reaches_stops_the_release(self) -> None:
        result, work = self.vex(
            openvex(
                statement("GO-2026-0001", "not_affected"), statement("GO-2026-0002", "affected")
            )
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("GO-2026-0002", result.stderr)
        self.assertNotIn("GO-2026-0001", result.stderr)
        self.assertFalse((work / "dist" / self.NAME).exists())
        self.assertNotIn(self.NAME, (work / "dist/checksums.txt").read_text())

    def test_a_failed_govulncheck_stops_the_release(self) -> None:
        result, work = self.vex("", status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((work / "dist" / self.NAME).exists())


if __name__ == "__main__":
    unittest.main()
