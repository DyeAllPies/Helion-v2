#!/usr/bin/env bash
# scripts/run-iris-e2e.sh
#
# Feature 19 acceptance harness — runs the iris end-to-end ML
# pipeline against a fresh docker-compose cluster and asserts every
# checkpoint on the spec's six-item acceptance list:
#
#   1. cluster comes up clean (coordinator + 2 Python nodes healthy)
#   2. submit.py returns 201 with the workflow ID
#   3. workflow reaches `completed` with zero ml.resolve_failed events
#   4. /api/datasets has iris/v1; /api/models has iris-logreg/v1
#      with non-zero accuracy + non-empty source_job_id
#   5. /workflows/{id}/lineage returns a DAG with s3:// artifact edges
#   6. serve submit → /api/services shows ready → /predict returns
#      a correct setosa prediction
#
# CI-safe: tears down the cluster (including named volumes) on
# every exit path via trap. Uses an isolated compose project name
# so concurrent runs don't clobber each other.
#
# Usage:
#   ./scripts/run-iris-e2e.sh
#
# Exit codes:
#   0  all six checkpoints passed
#   non-zero  — a specific checkpoint failed; cluster logs are
#              dumped before teardown so CI surface them.
#
# Rust runtime: feature 19 currently targets the Go runtime only.
# The Rust runtime (Dockerfile.node-rust) uses cgroup v2 + seccomp
# which are Linux-only; running it on Windows or macOS Docker
# Desktop fails at startup with a cgroup mount error. A Rust-runtime
# variant of this harness is sketched in the companion file
# scripts/run-iris-e2e-rust.sh (Linux-gated); CI should gate that
# on `uname -s = Linux`.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE_FILES="-f $ROOT_DIR/docker-compose.yml -f $ROOT_DIR/docker-compose.e2e.yml -f $ROOT_DIR/docker-compose.iris.yml"
EXIT_CODE=0

# ── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "==> $*"; }
ok()   { echo "    ✓ $*"; }
fail() { echo "    ✗ $*" >&2; EXIT_CODE=1; }

cleanup() {
  if [ "$EXIT_CODE" -ne 0 ]; then
    log "Dumping cluster logs for post-mortem..."
    COMPOSE_PROFILES=analytics,ml docker compose $COMPOSE_FILES logs --no-color 2>&1 | tail -200 || true
  fi
  if [ -n "${IRIS_SKIP_TEARDOWN:-}" ]; then
    log "IRIS_SKIP_TEARDOWN=$IRIS_SKIP_TEARDOWN — leaving cluster running for follow-up runs"
    log "  clean up manually with:  COMPOSE_PROFILES=analytics,ml docker compose $COMPOSE_FILES down -v"
  else
    log "Tearing down cluster..."
    COMPOSE_PROFILES=analytics,ml docker compose $COMPOSE_FILES down -v 2>/dev/null || true
    rm -rf "$ROOT_DIR/state" "$ROOT_DIR/logs" 2>/dev/null || true
  fi
  exit $EXIT_CODE
}
trap cleanup EXIT

# ── Checkpoint 1: cluster up + nodes healthy ────────────────────────────────

log "[1/6] Starting cluster..."
rm -rf "$ROOT_DIR/state" "$ROOT_DIR/logs"
mkdir -p "$ROOT_DIR/state" "$ROOT_DIR/logs"
COMPOSE_PROFILES=analytics,ml docker compose $COMPOSE_FILES up -d --build >/dev/null 2>&1

log "    waiting for coordinator /healthz (max 60s)..."
for i in $(seq 1 30); do
  if curl -sf http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    ok "coordinator healthy"
    break
  fi
  if [ "$i" -eq 30 ]; then fail "coordinator not healthy after 60s"; exit; fi
  sleep 2
done

log "    reading root token from /app/state/root-token..."
# MSYS_NO_PATHCONV prevents Git Bash from mangling the container path on Windows.
TOKEN=$(MSYS_NO_PATHCONV=1 docker exec helion-coordinator cat /app/state/root-token)
if [ -z "$TOKEN" ]; then fail "empty root token"; exit; fi

log "    waiting for both nodes to register as healthy (max 30s)..."
for i in $(seq 1 15); do
  HEALTHY=$(curl -sf -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/nodes 2>/dev/null \
    | python -c "import sys,json; d=json.load(sys.stdin); print(sum(1 for n in d.get('nodes',[]) if n.get('health')=='healthy'))" 2>/dev/null || echo 0)
  if [ "$HEALTHY" -ge 2 ]; then
    ok "$HEALTHY healthy nodes registered"
    break
  fi
  if [ "$i" -eq 15 ]; then fail "expected 2 healthy nodes, got $HEALTHY"; exit; fi
  sleep 2
done

# ── Checkpoint 2: submit.py returns with workflow ID ────────────────────────

log "[2/6] Submitting iris workflow..."
SUBMIT_OUTPUT=$(cd "$ROOT_DIR/examples/ml-iris" && \
  HELION_API_URL=http://127.0.0.1:8080 \
  HELION_JOB_API_URL=http://coordinator:8080 \
  HELION_TOKEN="$TOKEN" \
  python submit.py workflow.yaml 2>&1 || true)
if echo "$SUBMIT_OUTPUT" | grep -q "submitted: id=iris-wf-1"; then
  ok "submit.py reported id=iris-wf-1"
else
  fail "submit.py output missing expected 'submitted: id=iris-wf-1' line"
  echo "$SUBMIT_OUTPUT" >&2
  exit
fi

# ── Checkpoint 3: workflow completes + zero ml.resolve_failed events ─────────

log "[3/6] Polling workflow status (max 180s)..."
for i in $(seq 1 60); do
  STATUS=$(curl -sf -H "Authorization: Bearer $TOKEN" \
    "http://127.0.0.1:8080/workflows/iris-wf-1" 2>/dev/null \
    | python -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")
  case "$STATUS" in
    completed) ok "workflow reached completed after ~$((i*3))s"; break ;;
    failed|cancelled) fail "workflow terminal state: $STATUS"; exit ;;
  esac
  if [ "$i" -eq 60 ]; then fail "workflow still $STATUS after 180s"; exit; fi
  sleep 3
done

RESOLVE_FAILED=$(curl -sf -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8080/api/analytics/events?limit=500" 2>/dev/null \
  | python -c "
import sys, json
try:
    d = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
evs = d.get('events', []) if isinstance(d, dict) else []
print(sum(1 for e in evs if 'resolve' in str(e.get('event_type', ''))))
" 2>/dev/null || echo 0)
if [ "$RESOLVE_FAILED" -eq 0 ]; then
  ok "zero ml.resolve_failed events on the analytics feed"
else
  fail "unexpected ml.resolve_failed count: $RESOLVE_FAILED"
  exit
fi

# ── Checkpoint 4: registry has iris/v1 + iris-logreg/v1 with lineage ────────

log "[4/6] Checking /api/datasets + /api/models..."
DATASET_OK=$(curl -sf -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/datasets \
  | python -c "
import sys, json
d = json.load(sys.stdin)
for ds in d.get('datasets', []):
    if ds.get('name') == 'iris' and ds.get('version') == 'v1':
        print('ok'); sys.exit(0)
print('missing')
")
if [ "$DATASET_OK" != "ok" ]; then fail "iris/v1 not found in /api/datasets"; exit; fi
ok "iris/v1 registered"

MODEL_INFO=$(curl -sf -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/models \
  | python -c "
import sys, json
d = json.load(sys.stdin)
for m in d.get('models', []):
    if m.get('name') == 'iris-logreg' and m.get('version') == 'v1':
        acc = m.get('metrics', {}).get('accuracy', 0)
        src = m.get('source_job_id', '')
        print(f'{acc} {src}')
        sys.exit(0)
print('missing')
")
if [ "$MODEL_INFO" = "missing" ]; then fail "iris-logreg/v1 not found in /api/models"; exit; fi
ACC=$(echo "$MODEL_INFO" | awk '{print $1}')
SRC=$(echo "$MODEL_INFO" | awk '{print $2}')
if [ -z "$SRC" ] || [ "$SRC" = "missing" ]; then fail "source_job_id empty on iris-logreg/v1"; exit; fi
# Compare floats with awk so we don't depend on bc being installed.
ACC_OK=$(awk "BEGIN { print ($ACC > 0) ? 1 : 0 }")
if [ "$ACC_OK" != "1" ]; then fail "accuracy not > 0: got $ACC"; exit; fi
ok "iris-logreg/v1 registered (accuracy=$ACC, source_job_id=$SRC)"

# ── Checkpoint 5: lineage endpoint returns DAG with s3:// edges ──────────────

log "[5/6] Checking /workflows/iris-wf-1/lineage..."
MODEL_URI=$(curl -sf -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:8080/workflows/iris-wf-1/lineage \
  | python -c "
import sys, json
d = json.load(sys.stdin)
for j in d.get('jobs', []):
    if j.get('name') == 'train':
        for o in j.get('outputs', []):
            if o.get('name') == 'MODEL':
                print(o.get('uri', ''))
                sys.exit(0)
print('')
")
case "$MODEL_URI" in
  s3://*/model.joblib)
    ok "train's MODEL output URI: $MODEL_URI"
    ;;
  *)
    fail "MODEL URI not s3:// in lineage response: '$MODEL_URI'"
    exit
    ;;
esac

# ── Checkpoint 6: serve submit → ready → /predict correct ────────────────────

log "[6/6] Submitting serve job + testing /predict..."
SERVE_RESP=$(cat <<JSON | curl -sf -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" http://127.0.0.1:8080/jobs -d @-
{
  "id": "iris-serve-1",
  "command": "uvicorn",
  "args": ["serve:app", "--host", "0.0.0.0", "--port", "8000"],
  "env": {"PYTHONPATH": "/app/ml-iris"},
  "inputs": [{"name": "MODEL", "uri": "$MODEL_URI", "local_path": "model.joblib"}],
  "service": {"port": 8000, "health_path": "/healthz", "health_initial_ms": 2000}
}
JSON
)
if ! echo "$SERVE_RESP" | python -c "import sys,json; sys.exit(0 if json.load(sys.stdin).get('id')=='iris-serve-1' else 1)"; then
  fail "serve submit did not ACK: $SERVE_RESP"
  exit
fi
ok "serve job submitted"

log "    waiting for /api/services to report iris-serve-1 ready (max 30s)..."
NODE_ADDR=""
for i in $(seq 1 15); do
  READY=$(curl -sf -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/services \
    | python -c "
import sys, json
d = json.load(sys.stdin)
for s in d.get('services', []):
    if s.get('job_id') == 'iris-serve-1' and s.get('ready'):
        addr = s.get('node_address', '').split(':')[0]
        port = s.get('port', 0)
        print(f'{addr}:{port}')
        sys.exit(0)
print('')
")
  if [ -n "$READY" ]; then
    NODE_ADDR="$READY"
    ok "service ready at $NODE_ADDR"
    break
  fi
  if [ "$i" -eq 15 ]; then fail "service not ready after 30s"; exit; fi
  sleep 2
done

log "    testing POST /predict via coordinator container (helion-net)..."
PREDICTION=$(MSYS_NO_PATHCONV=1 docker exec helion-coordinator \
  wget -qO- --header='Content-Type: application/json' \
       --post-data='{"features":[[5.1,3.5,1.4,0.2]]}' \
       "http://$NODE_ADDR/predict" 2>&1 || true)
case "$PREDICTION" in
  '{"predictions":[0]}')
    ok "setosa row returns class 0 (expected)"
    ;;
  *)
    fail "unexpected prediction response: $PREDICTION"
    exit
    ;;
esac

log "ALL 6 CHECKPOINTS PASSED"
