#!/usr/bin/env bash
# Give the Orchard binary that mise installed an ad hoc signature, if its own signature fails.
#
# Since Orchard 0.56.0, the macOS release binary has a signature whose designated requirement looks
# for the Developer ID marker on the root certificate, not on the intermediate, so the binary never
# satisfies it (openai/orchard#456). macOS 26 then kills the process at launch with SIGKILL, before
# it writes any output. An ad hoc signature, as the Go linker gives to a binary built from source,
# runs. mise checks the SHA-256 of the archive before this changes the signature.
#
# It changes nothing on Linux, or when the signature of the binary passes.
set -euo pipefail

[ "$(uname -s)" = "Darwin" ] || exit 0

binary="${ORCHARD_BIN:-$(mise which orchard)}"
# -v makes codesign check the designated requirement too. Without it, the check passes.
if codesign --verify --strict -v "$binary" 2> /dev/null; then
  exit 0
fi

echo "sign-orchard: $binary fails its own signature check (openai/orchard#456), signing it ad hoc" >&2
codesign --force --sign - "$binary"
codesign --verify --strict -v "$binary"
