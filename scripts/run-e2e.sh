#!/usr/bin/env bash
# scripts/run-e2e.sh
#
# One-command E2E test runner for local development.
#
# Usage:
#   ./scripts/run-e2e.sh              # headless (default)
#   ./scripts/run-e2e.sh --headed     # visible browser
#   ./scripts/run-e2e.sh --ui         # Playwright interactive UI
#
# What it does:
#   1. Builds and starts the cluster (coordinator + 2 nodes)
#   2. Waits for coordinator healthy + at least 1 node registered
#   3. Runs Playwright E2E tests against ng serve on :4200
#   4. Tears down the cluster (always, even on failure)
#
# Prerequisites:
#   - Docker + Docker Compose
#   - Node.js + npm
#   - Playwright browsers installed:  cd dashboard && npx playwright install chromium

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE_FILES="-f $ROOT_DIR/docker-compose.yml -f $ROOT_DIR/docker-compose.e2e.yml"
EXIT_CODE=0

# ── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

cleanup() {
  log "Tearing down cluster..."
  docker compose $COMPOSE_FILES down -v 2>/dev/null || true
}
trap cleanup EXIT

# ── 1. Start cluster ────────────────────────────────────────────────────────

log "Starting cluster..."
mkdir -p "$ROOT_DIR/state" "$ROOT_DIR/logs"
docker compose $COMPOSE_FILES up -d --build

# ── 2. Wait for coordinator healthy ─────────────────────────────────────────

log "Waiting for coordinator healthz..."
for i in $(seq 1 30); do
  if curl -sf http://127.0.0.1:8080/healthz > /dev/null 2>&1; then
    log "Coordinator is healthy."
    break
  fi
  if [ "$i" -eq 30 ]; then
    docker compose $COMPOSE_FILES logs
    fail "Coordinator not healthy after 60s"
  fi
  sleep 2
done

# ── 3. Wait for nodes to register ───────────────────────────────────────────

TOKEN=$(cat "$ROOT_DIR/state/root-token")
log "Waiting for healthy nodes..."
for i in $(seq 1 20); do
  HEALTHY=$(curl -sf -H "Authorization: Bearer $TOKEN" \
    http://127.0.0.1:8080/nodes 2>/dev/null \
    | python3 -c "import sys,json; print(sum(1 for n in json.load(sys.stdin) if n.get('healthy')))" 2>/dev/null || echo 0)
  if [ "$HEALTHY" -ge 1 ]; then
    log "$HEALTHY healthy node(s) registered."
    break
  fi
  if [ "$i" -eq 20 ]; then
    docker compose $COMPOSE_FILES logs
    fail "No healthy nodes after 60s"
  fi
  sleep 3
done

# ── 4. Run Playwright ────────────────────────────────────────────────────────

log "Running Playwright E2E tests..."
cd "$ROOT_DIR/dashboard"

# Pass through any flags (--headed, --ui, etc.)
npx playwright test "$@" || EXIT_CODE=$?

if [ "$EXIT_CODE" -eq 0 ]; then
  log "All E2E tests passed."
else
  log "E2E tests failed (exit code $EXIT_CODE). Run 'npm run e2e:report' for details."
fi

exit $EXIT_CODE
