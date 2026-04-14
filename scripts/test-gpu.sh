#!/usr/bin/env bash
# scripts/test-gpu.sh
#
# Run the GPU-tagged integration tests. Separate from `make test` /
# `make check-full` because GitHub Actions runners don't have GPUs;
# a developer runs this locally after the normal e2e suite passes.
#
# Usage:
#   ./scripts/test-gpu.sh          # run tagged tests with default flags
#   ./scripts/test-gpu.sh -v       # verbose; forwards extra flags to `go test`
#
# Prerequisites:
#   - An NVIDIA GPU visible to the host (check with `nvidia-smi`)
#   - Go 1.21+

set -euo pipefail

if ! command -v nvidia-smi >/dev/null 2>&1; then
  echo "nvidia-smi not found on PATH — this harness only makes sense on a GPU host." >&2
  echo "Use the regular test suite (make test / go test ./...) for CPU-only work." >&2
  exit 1
fi

echo "== nvidia-smi inventory =="
nvidia-smi --list-gpus
echo

cd "$(dirname "$0")/.."

echo "== go test -tags gpu ./tests/gpu/... =="
# -count=1 bypasses the test cache; GPU tests are cheap enough that
# we always want fresh output. Forward any caller-supplied flags.
go test -tags gpu -count=1 "$@" ./tests/gpu/...
