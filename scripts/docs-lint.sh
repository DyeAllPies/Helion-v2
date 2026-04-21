#!/usr/bin/env bash
#
# scripts/docs-lint.sh — feature 44 gate on the docs/ tree shape.
#
# Checks every .md under docs/ (with the exemptions below):
#   1. Starts with three blockquote frontmatter lines carrying
#      Audience, Scope, and Depth keys so a cold reader can tell at
#      a glance who the file is for and how deep it goes.
#   2. Stays within its folder's line budget (see LINE CAPS below).
#      Files that grow past the cap are a signal to split, not a
#      reason to raise the cap.
#
# Exemptions — files in docs/ that are NOT gated because they use
# a different template (feature spec / audit) or are immutable history:
#   - docs/audits/**                    (immutable post-mortems)
#   - docs/planned-features/**          (feature-spec + audit templates)
#   - docs/DOCS-WORKFLOW.md             (canonical contributor doc)
#
# Run: ./scripts/docs-lint.sh
# CI:  .github/workflows/ci.yml — docs-lint job

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

FAILED=0

is_exempt() {
    local p="$1"
    case "$p" in
        docs/audits/*)                               return 0 ;;
        docs/planned-features/*)                     return 0 ;;
        docs/planned-features/deferred/*)            return 0 ;;
        docs/planned-features/implemented/*)         return 0 ;;
        docs/DOCS-WORKFLOW.md)                       return 0 ;;
    esac
    return 1
}

is_breadcrumb() {
    local f="$1"
    local lines
    lines="$(wc -l < "$f")"
    [ "$lines" -le 10 ] || return 1
    head -1 "$f" | grep -q '^<!-- MOVED' || return 1
    return 0
}

line_cap() {
    local p="$1"
    case "$p" in
        docs/README.md)                              echo 180 ;;
        docs/*/README.md)                            echo 300 ;;
        # Open question carried forward from feature 44 spec: ml-pipelines
        # is 685 lines pending the iris-walkthrough extraction. Tighten to
        # 500 once that split lands.
        docs/guides/ml-pipelines.md)                 echo 700 ;;
        *)                                           echo 500 ;;
    esac
}

check_frontmatter() {
    local f="$1"
    local head
    head="$(head -5 "$f")"
    local missing=()
    grep -q '^> \*\*Audience:\*\*'  <<<"$head" || missing+=("Audience")
    grep -q '^> \*\*Scope:\*\*'     <<<"$head" || missing+=("Scope")
    grep -q '^> \*\*Depth:\*\*'     <<<"$head" || missing+=("Depth")
    if [ "${#missing[@]}" -gt 0 ]; then
        printf 'FAIL %s: missing frontmatter key(s): %s\n' \
            "$f" "$(IFS=, ; echo "${missing[*]}")"
        return 1
    fi
    return 0
}

check_line_cap() {
    local f="$1"
    local cap lines
    cap="$(line_cap "$f")"
    lines="$(wc -l < "$f")"
    if [ "$lines" -gt "$cap" ]; then
        printf 'FAIL %s: %d lines > cap %d\n' "$f" "$lines" "$cap"
        return 1
    fi
    return 0
}

# Collect the file list deterministically so the script is reproducible in
# CI. `find -print0` + readarray keeps paths with spaces safe, though we
# don't expect any in docs/.
mapfile -d '' -t FILES < <(find docs -type f -name '*.md' -print0 | sort -z)

for f in "${FILES[@]}"; do
    if is_exempt "$f"; then
        continue
    fi
    if is_breadcrumb "$f"; then
        check_line_cap "$f" || FAILED=1
        continue
    fi
    check_frontmatter "$f" || FAILED=1
    check_line_cap   "$f" || FAILED=1
done

if [ "$FAILED" -eq 1 ]; then
    echo
    echo 'docs-lint: FAILED — see messages above.'
    echo 'Frontmatter template:'
    echo '  > **Audience:** <engineers | operators | users | contributors>'
    echo '  > **Scope:** <one sentence — what this file answers>'
    echo '  > **Depth:** <reference | guide | runbook>'
    exit 1
fi

echo "docs-lint: OK ($(printf '%s\n' "${FILES[@]}" | wc -l | tr -d ' ') .md files under docs/)"
