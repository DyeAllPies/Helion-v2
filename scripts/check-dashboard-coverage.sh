#!/usr/bin/env bash
# scripts/check-dashboard-coverage.sh
#
# Enforce Angular dashboard coverage thresholds.
#
# Why this exists:
#   karma.conf.js declares `coverageReporter.check.global` thresholds, but
#   @angular-devkit/build-angular:karma overrides the reporter configuration
#   entirely — it keeps HTML output and drops everything else we requested
#   (json-summary, lcovonly, text-summary). It also ignores the `check:`
#   block, so karma-coverage never exits non-zero on threshold violations.
#
#   External enforcement by parsing the generated HTML is the only reliable
#   path today. The HTML schema has been stable across Angular 15–21.
#
# Thresholds are kept in sync with:
#   - karma.conf.js `coverageReporter.check.global`
#   - The Go CI internal/ coverage threshold (85%)
#   in .github/workflows/ci.yml
#
# Usage:
#   ./scripts/check-dashboard-coverage.sh
#   # exits 0 on pass, 1 on failure, 2 on missing coverage HTML

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
INDEX="$ROOT_DIR/dashboard/coverage/helion-dashboard/index.html"

# Keep in sync with karma.conf.js `coverageReporter.check.global`.
MIN_STATEMENTS=85
MIN_BRANCHES=60
MIN_FUNCTIONS=85
MIN_LINES=85

if [ ! -f "$INDEX" ]; then
  echo "FAIL: coverage index.html not found at $INDEX" >&2
  echo "Run: cd dashboard && npx ng test --watch=false --browsers=ChromeHeadless --code-coverage" >&2
  exit 2
fi

# Istanbul's index.html has four `<span class="strong">XX.XX%</span>` entries
# in header order: statements, branches, functions, lines. Extract them in
# document order so we know which is which without relying on labels.
mapfile -t PCTS < <(
  grep -oE '<span class="strong">[0-9.]+%' "$INDEX" \
    | sed -E 's#<span class="strong">##; s#%$##' \
    | head -4
)

if [ ${#PCTS[@]} -lt 4 ]; then
  echo "FAIL: could not parse coverage from index.html (got ${#PCTS[@]} values, need 4)" >&2
  exit 2
fi

STATEMENTS="${PCTS[0]}"
BRANCHES="${PCTS[1]}"
FUNCTIONS="${PCTS[2]}"
LINES="${PCTS[3]}"

echo "Dashboard coverage:"
printf "  statements: %s%% (min %d%%)\n" "$STATEMENTS" "$MIN_STATEMENTS"
printf "  branches:   %s%% (min %d%%)\n" "$BRANCHES"   "$MIN_BRANCHES"
printf "  functions:  %s%% (min %d%%)\n" "$FUNCTIONS"  "$MIN_FUNCTIONS"
printf "  lines:      %s%% (min %d%%)\n" "$LINES"      "$MIN_LINES"

STATUS=0
check_ge() {
  local name="$1" actual="$2" min="$3"
  if awk -v a="$actual" -v m="$min" 'BEGIN { exit !(a+0 < m+0) }'; then
    echo "FAIL: $name $actual% is below $min% threshold" >&2
    STATUS=1
  fi
}

check_ge statements "$STATEMENTS" "$MIN_STATEMENTS"
check_ge branches   "$BRANCHES"   "$MIN_BRANCHES"
check_ge functions  "$FUNCTIONS"  "$MIN_FUNCTIONS"
check_ge lines      "$LINES"      "$MIN_LINES"

if [ $STATUS -eq 0 ]; then
  echo "PASS: all dashboard coverage thresholds met."
fi
exit $STATUS
