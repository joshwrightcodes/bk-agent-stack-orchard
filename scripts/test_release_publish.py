# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests of the sign phase of scripts/release-publish.sh.

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
        for name in ("x_linux_amd64.tar.gz", "x_linux_amd64.tar.gz.sbom.json"):
            data = name.encode()
            (self.work / "dist" / name).write_bytes(data)
            lines.append(f"{hashlib.sha256(data).hexdigest()}  {name}\n")
        (self.work / "dist/checksums.txt").write_text("".join(lines))
        for arch in ("amd64", "arm64"):
            (self.work / f"build/sbom/image-linux-{arch}.spdx.json").write_text("{}")
        (self.work / "build/chart/bk-agent-stack-orchard-0.1.0.tgz").write_text("chart")
        (self.work / "build/release.env").write_text(f"IMAGE_DIGEST={INDEX}\nCHART_DIGEST={CHART}\n")
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
            cwd=self.work, env=env, capture_output=True, text=True)
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
        self.assertIn(f"cosign sign --yes ghcr.io/joshwrightcodes/charts/bk-agent-stack-orchard@{CHART}", calls)
        upload = next(c for c in calls if c.startswith("gh release upload v0.1.0 dist/"))
        for name in ("x_linux_amd64.tar.gz", "x_linux_amd64.tar.gz.sbom.json", "checksums.txt",
                     "checksums.txt.sigstore.json"):
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
        self.index({"manifests": GOOD_INDEX["manifests"] + [manifest("sha256:" + "f" * 64, "linux", "s390x")]})
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
            f"IMAGE_DIGEST=$(touch {marker})\ntouch {marker}\nCHART_DIGEST={CHART}\n")
        result, calls = self.sign()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())
        self.assertFalse([c for c in calls if c.startswith(("cosign", "gh"))], calls)


if __name__ == "__main__":
    unittest.main()
