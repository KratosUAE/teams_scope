#!/usr/bin/env bash
set -euo pipefail

# Derive a version string from the current git state: exact tag if one
# points at HEAD, otherwise "<nearest-tag>-<n>-g<sha>" or just "<sha>"
# for un-tagged checkouts, with "-dirty" suffixed if the working tree
# is not clean. Falls back to "dev" when git is unavailable.
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X teams_con/internal/version.Version=${VERSION}" \
    -o teams_con .

mkdir -p "$HOME/.aux/bin"
cp teams_con "$HOME/.aux/bin/teams_con"

echo "installed to ~/.aux/bin/teams_con (${VERSION})"
