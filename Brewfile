# SPDX-License-Identifier: AGPL-3.0-or-later
# Host tools for contributors. mise pins every other tool (mise.toml).
#
# The Tart tap is not an official tap. Trust it once before the first run:
#   brew trust openai/tools
#   brew bundle
#
# Colima and the Docker CLI are one Docker runtime. If you have another one, or do not need the
# three tasks that use Docker, skip them:
#   HOMEBREW_BUNDLE_BREW_SKIP="colima docker docker-buildx" brew bundle

tap "openai/tools"

brew "mise"                 # Go, Orchard, the linters, Helm, kind, ko, and the release tools
brew "gh"                   # pull requests
brew "jq"                   # scripts/verify-release.sh
brew "colima"               # a Docker runtime for check-c4, snapshot, and smoke
brew "docker"               # the Docker CLI
brew "docker-buildx"        # docker buildx, for smoke and the release scripts
brew "openai/tools/tart"    # the real-VM tests. The Mac hosts pin a version: see
                            # docs/testing-on-real-vms.md
brew "openai/tools/softnet" # the real-VM tests. It needs the setuid bit: see the same guide
