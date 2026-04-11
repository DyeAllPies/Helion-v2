# helion-v2 Security & Code Quality Audit — Template

**This file is a template, not an audit record.** When starting a new
audit, copy this file to `docs/audits/<YYYY-MM-DD>.md` (use today's
calendar date as the audit ID) and work in the copy. Prior audits live
under `docs/audits/` as a historical archive — never overwrite them.

> **Instructions to an AI assistant running an audit:**
>
> 1. Pick today's date in `YYYY-MM-DD` form. This is the **audit ID**.
> 2. Create a new file at `docs/audits/<audit-id>.md` with this template.
>    If that filename already exists (two audits in one day), append
>    `-a`, `-b`, … to disambiguate: e.g. `2026-04-11-b.md`.
> 3. Fill in the Date, Auditor, and Overall Risk Level fields at the top
>    of the new file.
> 4. When referencing a finding from code comments, commit messages,
>    tests, or other audit docs, **always include the audit ID**. Use
>    the format `AUDIT <audit-id>/<severity-letter><n>`. Examples:
>      - `AUDIT 2026-04-11/M1`
>      - `AUDIT 2026-05-02-b/H3`
>      - `AUDIT 2026-06-01/L4 (fixed)`
>    This disambiguates findings across audits — `M1` alone collides
>    with every previous audit's first Medium.
> 5. Do not delete or rename files under `docs/audits/`. Closed audits
>    are a historical record of both problems and fixes.
> 6. Leave this template (`docs/AUDIT.md`) untouched — it stays empty
>    so the next auditor has a clean starting point.

---

**Audit ID:** _YYYY-MM-DD (matches the filename under `docs/audits/`)_
**Date:** _YYYY-MM-DD_
**Auditor:** _name or tool_
**Overall Risk Level:** _CRITICAL | HIGH | MEDIUM | LOW_

_One-paragraph executive summary: the shape of the system, the headline
finding, and whether this state is fit for production. Keep it short — the
severity table below carries the details._

---

## Summary

| Severity  | Open | Fixed |
|-----------|-----:|------:|
| Critical  |    0 |     0 |
| High      |    0 |     0 |
| Medium    |    0 |     0 |
| Low       |    0 |     0 |
| Test Gaps |    0 |     0 |

_Update these counts as findings are closed. When the table is all zeroes in
the Open column, the audit is "closed" — capture that fact in one line
immediately below the table._

---

## How to use this template

1. **One finding per subsection.** Use the ID conventions `C1..Cn`, `H1..Hn`,
   `M1..Mn`, `L1..Ln` for Critical / High / Medium / Low. Test gaps use
   `T1..Tn`. IDs are stable for the life of this audit — never reuse a
   retired ID within the same audit file.
2. **External references use the audit ID.** When you mention a finding
   outside this file (code comment, commit message, another audit doc),
   always prefix with the audit ID: `AUDIT 2026-04-11/M1`, not bare `M1`.
   This prevents collision with the same severity letter in other audits.
3. **Mark fixed findings with a ✅.** Prepend `✅ ` to the heading and
   append `*(fixed YYYY-MM-DD)*`. Leave the full text in place so the
   audit is a historical record of both the problem and the fix.
4. **Every finding has a File line.** Point at a specific path + symbol,
   not a whole package. Reviewers should be able to jump straight to
   the code.
5. **Every fix cites its tests.** A finding is not closed until at
   least one test exercises the fixed branch. List the test names in
   the fix block.
6. **Deployment / configuration notes** at the bottom are a separate
   bucket: items that are known gaps but are explicitly future work,
   not audit findings. Don't mix them with the severity-tracked
   findings above.
7. **Remediation Plan** is for pre-implementation design. Once a
   finding lands, remove its spec from that section and rely on the
   ✅-marked finding block as the source of truth.

---

## Critical

### C1 — _short title_
**File:** `path/to/file.go` — _function or section_

_Description: what the bug is, why it is Critical, what an attacker (or a
runtime failure) could do with it. Include a minimal code excerpt if that
makes the problem obvious faster than prose._

**Fix:** _one-sentence plan. Detail lives in the Remediation Plan until the
fix lands, then in this block once the ✅ marker is applied._

---

## High

### H1 — _short title_
**File:** `path/to/file.go`

_Same shape as a Critical finding, one severity level down. Typical High
items: auth bypasses guarded by an unlikely flag, unbounded resource
consumption, information disclosure with real exploit value._

---

## Medium

### M1 — _short title_
**File:** `path/to/file.go`

_Correctness and hardening issues that are not directly exploitable but
accumulate into real risk: missing timeouts, silent error swallowing,
unbounded maps, rate-limit gaps on non-critical endpoints, goroutine leaks._

---

## Low

### L1 — _short title_
**File:** `path/to/file.go`

_Cosmetic, dead-code, or information-leak items. Still worth writing down
so the next auditor doesn't re-discover them. If something is truly not
worth tracking, don't write it down at all._

---

## Test Gaps

| #  | Status      | Test                                   |
|----|-------------|----------------------------------------|
| T1 | 🔲 Open     | `TestX_Y_Z` — _what branch it covers_  |

_Use this table for coverage holes that are not tied to a specific bug —
branches that should be tested to prevent regressions of future fixes.
Once a test lands, mark it ✅ Fixed and keep the row as a history entry._

---

## Remediation Plan

_Pre-implementation design for the open findings above. One subsection per
finding, grouped into batches that can land independently and keep the
tree green. Delete a subsection once the finding is marked ✅ — the fix
block under the finding becomes the source of truth._

### Execution batches (YYYY-MM-DD)

| # | Batch | Findings | Risk | Primary files |
|---|-------|----------|------|----------------|
| 1 | _name_ | _IDs_ | low / medium / high | _paths_ |

**Why this order:** _one sentence per batch explaining the dependency
relationship or risk rationale._

### _F# — short title_

_Exact code to write. Structured enough that someone other than the original
author can land the fix without re-deriving the design._

---

## Deployment & Configuration Notes

_Known-gaps bucket. Items here are NOT security findings — they are either
infrastructure decisions with acknowledged tradeoffs or features explicitly
deferred to a later phase. Examples:_

- **No config file support** — all configuration is via environment variables.
- **CA certificate TTL** — default and override env var.
- **`/readyz` scope** — what the readiness probe does and does not verify.
- **Graceful shutdown** — which signals are handled, timeout, caveats.

---

## File-Size Hygiene — Large Files to Split

_Optional section for non-security housekeeping: files over ~500 lines that
are candidates for splitting by concern. Production-code splits must
preserve public API; test-file splits are lowest-risk._

| File | Lines | Status | Target split |
|------|------:|--------|--------------|
| `path/to/big_file.go` | 0 | pending | _proposed split by concern_ |

**Guiding principles:**
1. Test-file splits are lowest risk — same `_test` package, helpers shared.
2. Production-code splits must preserve public API — no symbol renames.
3. Each split file should be cohesive and under ~400 lines.
4. Run full package tests after each split to catch duplicate declarations.
