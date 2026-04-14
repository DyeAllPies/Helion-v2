#!/usr/bin/env bash
# scripts/test-race.sh
#
# Runs `go test -race` so data races are caught before pushing. The
# race detector requires CGO / a C compiler (gcc), which may not be on
# $PATH on a typical Windows dev machine. Strategy:
#
#   1. If gcc is already on PATH → run `go test -race` natively (fast).
#   2. If WinLibs MinGW-w64 is installed via winget at the canonical
#      path but not on PATH → prepend it and run natively.
#   3. Otherwise → fall back to running inside the same golang:1.26
#      Docker image CI uses (portable, no host toolchain needed).
#
# Mirrors CI's `go test -race -count=1 ./...` step.
#
# Usage:
#   ./scripts/test-race.sh                # every package
#   ./scripts/test-race.sh ./internal/... # target a subset
#
# Exits with go test's status.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGETS="${*:-./...}"

run_native() {
  echo "==> go test -race (native, using $(which gcc))"
  cd "$ROOT_DIR"
  GOWORK=off CGO_ENABLED=1 go test -race -count=1 $TARGETS
}

run_docker() {
  echo "==> go test -race (Docker golang:1.26)"
  # MSYS_NO_PATHCONV=1 disables Git-Bash path translation on Windows so
  # that `/src` inside the container isn't rewritten to a Windows path.
  # golang:1.26 (bullseye) ships with gcc, which -race requires.
  # GOFLAGS=-buildvcs=false skips the .git version-stamp check that
  # fails inside a non-root container.
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$ROOT_DIR":/src \
    -w //src \
    -e GOWORK=off \
    -e CGO_ENABLED=1 \
    -e GOFLAGS=-buildvcs=false \
    --tmpfs //tmp:exec \
    golang:1.26 \
    go test -race -count=1 $TARGETS
}

# 1. gcc on PATH → fastest path
if command -v gcc >/dev/null 2>&1; then
  run_native
  exit $?
fi

# 2. Canonical winget WinLibs install? If so, prepend to PATH.
WINLIBS="$USERPROFILE/AppData/Local/Microsoft/WinGet/Packages/BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe/mingw64/bin"
if [ -x "$WINLIBS/gcc.exe" ]; then
  export PATH="$WINLIBS:$PATH"
  run_native
  exit $?
fi

# 3. Fallback: Docker (always works if Docker is installed)
if ! command -v docker >/dev/null 2>&1; then
  echo "FAIL: no gcc on PATH, no WinLibs install at expected location, and no Docker." >&2
  echo "Install one of:" >&2
  echo "  - WinLibs MinGW-w64: winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT" >&2
  echo "  - Docker Desktop" >&2
  exit 1
fi
run_docker
