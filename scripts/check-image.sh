#!/usr/bin/env bash
# Check the container image that GoReleaser and ko build (test strategy, T-120).
#
# The design states a distroless base and a non-root user. An image that runs as root, or that
# uses another base, fails here rather than in a cluster.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

expected_base="gcr.io/distroless/static-debian13:nonroot"
expected_user="65532"

if ! grep -q "base_image: $expected_base" .goreleaser.yaml; then
  echo "check-image: .goreleaser.yaml does not set base_image to $expected_base" >&2
  exit 1
fi

# GoReleaser loads a snapshot image into the local daemon under goreleaser.ko.local.
image="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep '^goreleaser.ko.local' | head -1)"
if [ -z "$image" ]; then
  echo "check-image: no snapshot image found. Run: goreleaser release --snapshot --clean" >&2
  exit 1
fi

user="$(docker inspect --format '{{.Config.User}}' "$image")"
if [ "$user" != "$expected_user" ]; then
  echo "check-image: $image runs as user '${user:-root}', the design states $expected_user" >&2
  exit 1
fi

echo "check-image: $image runs as $user on $expected_base"
