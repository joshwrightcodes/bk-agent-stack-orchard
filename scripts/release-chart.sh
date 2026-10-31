#!/usr/bin/env bash
# Package the Helm chart with the version of a release, into build/chart.
#
# The chart and the controller have one version, and the Git tag is its only source, so the
# version in Chart.yaml is a placeholder that this replaces. GoReleaser adds the package to the
# checksums and to the release, so the signature of the checksums also covers the chart.
#
# Usage: scripts/release-chart.sh <version>    (without the "v", for example 0.3.1)
set -euo pipefail

version="${1:?usage: release-chart.sh <version>}"

root="$(git rev-parse --show-toplevel)"
cd "$root"

rm -rf build/chart
mkdir -p build/chart
helm package deploy/helm/bk-agent-stack-orchard \
  --version "$version" --app-version "$version" --destination build/chart
