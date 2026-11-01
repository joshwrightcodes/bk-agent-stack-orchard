#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Write the third-party licence texts of the controller binary.
#
# go-licenses saves the text of each module that the binary links into build/licenses. It skips
# the Go standard library, so this script adds the Go licence. The same files, with LICENSE and
# NOTICE, go to cmd/bk-agent-stack-orchard/kodata/licenses, which ko copies into the image at
# /var/run/ko/licenses. GoReleaser runs this script before a build (before.hooks), so a release
# and a snapshot always carry the notices. build/ and kodata/licenses are git-ignored.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

out="build/licenses"
kodata="cmd/bk-agent-stack-orchard/kodata/licenses"

# --ignore leaves this module (AGPL-3.0-or-later, the LICENSE file) out of the third-party texts.
# The warnings about assembly files are not errors: go-licenses cannot read the dependencies of
# non-Go code, and that code (xxhash, x/sys) has no other module.
go-licenses save ./cmd/bk-agent-stack-orchard --save_path "$out" --force \
  --ignore github.com/joshwrightcodes/bk-agent-stack-orchard

mkdir -p "$out/go-standard-library"
cp "$(go env GOROOT)/LICENSE" "$out/go-standard-library/LICENSE"

rm -rf "$kodata"
mkdir -p "$kodata"
cp LICENSE NOTICE "$kodata/"
mkdir -p "$kodata/third-party"
cp -R "$out/." "$kodata/third-party/"
echo "licenses: wrote $out and $kodata"
