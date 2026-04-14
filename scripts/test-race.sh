#!/usr/bin/env bash
# scripts/test-race.sh
#
# Runs `go test -race` inside a golang:1.26 Docker container so
# Windows developers (who typically don't have gcc installed) can
# catch data races before pushing.
#
# This mirrors what CI does (`go test -race -count=1 ./...` on an
# ubuntu-latest runner). Keeping the toolchain version pinned to the
# coordinator's golang image (1.26) ensures identical behaviour.
#
# Usage:
#   ./scripts/test-race.sh                # races on every package
#   ./scripts/test-race.sh ./internal/... # target a subset
#
# Exits with go test's status.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGETS="${*:-./...}"

# golang:1.26 (bullseye) ships with gcc, which -race requires.
# GOWORK=off mirrors the Makefile convention (no workspace leakage).
# GOFLAGS=-buildvcs=false skips the .git version-stamp check which
# fails inside a non-root container.
#
# MSYS_NO_PATHCONV=1 disables Git-Bash path translation on Windows so
# that `/src` inside the container doesn't get rewritten to a Windows
# path when this script is invoked from MSYS/Git-Bash.
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$ROOT_DIR":/src \
  -w //src \
  -e GOWORK=off \
  -e CGO_ENABLED=1 \
  -e GOFLAGS=-buildvcs=false \
  --tmpfs //tmp:exec \
  golang:1.26 \
  go test -race -count=1 $TARGETS
