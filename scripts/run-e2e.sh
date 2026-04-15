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
  COMPOSE_PROFILES=analytics,ml docker compose $COMPOSE_FILES down -v 2>/dev/null || true
}
trap cleanup EXIT

# ── 1. Start cluster ────────────────────────────────────────────────────────

log "Starting cluster..."
# E2E overlay uses a named Docker volume (e2e-state) instead of the host
# bind mount (./state), so E2E tests never read or pollute user data.
# `docker compose down -v` removes the volume, giving each run a clean DB.
#
# COMPOSE_PROFILES=analytics,ml activates:
#   - analytics-db (PostgreSQL) — backs /api/analytics/* endpoints and
#     the dashboard /analytics page.
#   - minio + minio-bootstrap — backs the feature-11 artifact store so
#     HELION_ARTIFACTS_BACKEND=s3 (set in docker-compose.e2e.yml) has
#     something to talk to, and the feature-11 live-S3 integration
#     test (`go test ./internal/artifacts/ -run TestS3_LiveIntegration`
#     with MINIO_TEST_* set) has a real endpoint.
mkdir -p "$ROOT_DIR/logs"
COMPOSE_PROFILES=analytics,ml docker compose $COMPOSE_FILES up -d --build

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

# Token file inside container is owned by user helion (mode 0600) — read via docker exec
# Double-slash prevents Git Bash (MSYS2) from converting /app/... to C:/Program Files/Git/app/...
TOKEN=$(docker exec helion-coordinator cat //app/state/root-token)
export E2E_TOKEN="$TOKEN"
log "Waiting for healthy nodes..."
for i in $(seq 1 20); do
  HEALTHY=$(curl -sf -H "Authorization: Bearer $TOKEN" \
    http://127.0.0.1:8080/nodes 2>/dev/null \
    | python3 -c "
import sys,json
d=json.load(sys.stdin)
nodes=d.get('nodes',d) if isinstance(d,dict) else d
print(sum(1 for n in nodes if n.get('healthy') or n.get('health')=='healthy'))
" 2>/dev/null || echo 0)
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

# ── 4. Run live-MinIO artifact-store integration tests ─────────────────────
#
# Feature 11's live-S3 path (internal/artifacts/s3.go + the Stager's
# upload loop via internal/staging/) has excellent unit coverage but
# never talks to a real MinIO in CI unless we point it there explicitly.
# The --profile ml cluster has MinIO up on 127.0.0.1:9000 with the
# `helion` bucket pre-created; export the MINIO_TEST_* env that the
# Go tests look for and run them against it. Adds ~3 s to a full
# e2e run.
#
# Tests that run here:
#   - TestS3_LiveIntegration            (internal/artifacts)
#   - TestLiveS3ArtifactRoundtrip       (tests/integration — added in
#                                        this same commit; covers the
#                                        node-agent → Stager → MinIO
#                                        path end-to-end)

log "Running live-MinIO artifact-store tests..."
(
  cd "$ROOT_DIR"
  export MINIO_TEST_ENDPOINT=127.0.0.1:9000
  export MINIO_TEST_BUCKET=helion
  export MINIO_TEST_ACCESS=helion
  export MINIO_TEST_SECRET=helion-dev-secret
  export MINIO_TEST_SSL=0
  export HELION_TEST_MINIO=1
  go test -count=1 -timeout 60s \
      ./internal/artifacts/ -run TestS3_LiveIntegration \
      ./tests/integration/ -run TestLiveS3Artifact || EXIT_CODE=$?
)
if [ "${EXIT_CODE:-0}" -ne 0 ]; then
  log "Live-MinIO artifact tests failed (exit $EXIT_CODE); continuing to Playwright so we get the full report."
fi

# ── 5. Run Playwright ────────────────────────────────────────────────────────

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
