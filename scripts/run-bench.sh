#!/usr/bin/env bash
# scripts/run-bench.sh
#
# Runs Go runtime benchmarks and saves timestamped results.
#
# Usage:
#   ./scripts/run-bench.sh                    # default: 5s benchtime
#   ./scripts/run-bench.sh --benchtime=10s    # custom benchtime
#
# Results are saved to benchmarks/YYYY-MM-DD_HHMMSS_<short-sha>.txt
# with machine info, git ref, and full benchmark output.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BENCH_DIR="$ROOT_DIR/benchmarks"
mkdir -p "$BENCH_DIR"

# ── Build result filename ────────────────────────────────────────────────────
TIMESTAMP=$(date +%Y-%m-%d_%H%M%S)
SHORT_SHA=$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
RESULT_FILE="$BENCH_DIR/${TIMESTAMP}_${SHORT_SHA}.txt"

# ── Collect machine info ─────────────────────────────────────────────────────
{
  echo "# Helion v2 — Benchmark Results"
  echo "# Date:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# Commit: $(git -C "$ROOT_DIR" rev-parse HEAD 2>/dev/null || echo 'unknown')"
  echo "# Branch: $(git -C "$ROOT_DIR" branch --show-current 2>/dev/null || echo 'unknown')"
  echo "# OS:     $(uname -s) $(uname -r) $(uname -m)"
  echo "# Go:     $(go version 2>/dev/null || echo 'unknown')"
  echo "# CPU:    $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | xargs || echo 'unknown')"
  echo "# Cores:  $(nproc 2>/dev/null || echo 'unknown')"
  echo "#"
  echo ""
} > "$RESULT_FILE"

# ── Run benchmarks ───────────────────────────────────────────────────────────
BENCHTIME="${1:---benchtime=5s}"
# Strip leading -- if user passes --benchtime=10s
BENCHTIME="${BENCHTIME#--}"

echo "==> Running benchmarks ($BENCHTIME)..."
echo "==> Results will be saved to: $RESULT_FILE"

cd "$ROOT_DIR"
go test -run=^$ -bench=. -"$BENCHTIME" -benchmem -count=1 ./tests/bench/... 2>&1 | tee -a "$RESULT_FILE"

echo ""
echo "==> Results saved to: $RESULT_FILE"
