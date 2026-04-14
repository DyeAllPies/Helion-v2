#!/usr/bin/env bash
# scripts/verify-repo-hygiene.sh
#
# Catches repo-level mistakes that CI would otherwise find for us.
# Runs as part of `make check` — cheap, fast, no network.
#
# Guards:
#
#   1. `go mod verify` — every module in go.sum matches its cached hash.
#      Catches corrupted / tampered caches.
#
#   2. Shell script executable bit — every *.sh tracked under scripts/
#      must be mode 100755 in the git index. A Linux CI runner can't
#      invoke a 100644 script directly: `./scripts/foo.sh` gives
#      "Permission denied" exit 126. This caught the most recent CI
#      failure in the coverage-check step.
#
# NOTE: this script does NOT re-run `go mod tidy` — go.mod carries an
# explicit genproto override that tidy would strip (see comment in
# go.mod). For the specific class of "missing go.sum entry" that a
# pristine Docker builder would hit, use `make docker-smoke` which
# actually builds the coordinator image. It's slow (~60s) so it's not
# part of the default `make check`.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# See Makefile GO_ENV comment: workspace mode is off everywhere to
# match CI (which has no go.work) and avoid the pre-split/split
# genproto ambiguity.
export GOWORK=off

echo "==> go mod verify"
go mod verify

echo "==> shell script executable bits"
status=0
while read -r mode _ path; do
  if [ "$mode" != "100755" ]; then
    echo "FAIL: $path is mode $mode in git index, should be 100755" >&2
    status=1
  fi
done < <(git ls-files --stage -- 'scripts/*.sh')
if [ $status -ne 0 ]; then
  echo "Fix with: git update-index --chmod=+x <path>" >&2
  exit $status
fi

echo "PASS: repo hygiene checks passed."
