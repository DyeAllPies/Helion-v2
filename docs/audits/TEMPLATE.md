# helion-v2 Security & Code Quality Audit — Template

**This file is a template, not an audit record.** Copy it to
`docs/audits/<YYYY-MM-DD>-<NN>.md` when starting a new audit and work
in the copy. Prior audits live under `docs/audits/` as a historical
archive — never overwrite them.

> **Instructions for an AI assistant (or human) running an audit:**
>
> 1. Pick today's date in `YYYY-MM-DD` form. The **audit ID** is
>    `<date>-<NN>` where `NN` is a two-digit counter starting at `01`
>    for the first audit of the day. Examples:
>       - First audit on 2026-05-02 → ID `2026-05-02-01`, file
>         `docs/audits/2026-05-02-01.md`.
>       - Second audit the same day → ID `2026-05-02-02`, file
>         `docs/audits/2026-05-02-02.md`.
>    The `NN` suffix is mandatory even when there is only one audit
>    that day — it keeps the scheme uniform and avoids a
>    conditional-rename later if a second audit does land.
> 2. Copy this template to the new path, fill in the Date, Auditor,
>    and Overall Risk Level fields, and list the audit in
>    `docs/audits/README.md` under its year section.
> 3. When referencing a finding from code comments, commit messages,
>    tests, or other audit docs, **always use the full audit ID**:
>    `AUDIT <audit-id>/<severity-letter><n>`. Examples:
>       - `AUDIT 2026-04-11-01/M1`
>       - `AUDIT 2026-05-02-02/H3`
>       - `AUDIT 2026-06-01-01/L4 (fixed)`
>    The severity+number alone (e.g. bare `M1`) collides with every
>    other audit's first Medium.
> 4. Do not delete or rename closed audit files — they are a
>    historical record of both the problem and the fix.
> 5. Leave this template (`docs/audits/TEMPLATE.md`) untouched so the
>    next auditor has a clean starting point.
> 6. After the audit lands, if any finding is deferred rather than
>    fixed, add an entry to `docs/planned-features/deferred/` (one
>    file per deferred item — see `deferred/TEMPLATE.md`) and link
>    it from the finding's block.

---

**Audit ID:** _YYYY-MM-DD-NN (matches the filename under `docs/audits/`)_
**Date:** _YYYY-MM-DD_
**Auditor:** _name or tool_
**Overall Risk Level:** _CRITICAL | HIGH | MEDIUM | LOW_

_One-paragraph executive summary: the shape of the system, the headline
finding, and whether this state is fit for production. Keep it short — the
severity table below carries the details._

---

## Summary

| Severity  | Open | Fixed | Deferred |
|-----------|-----:|------:|---------:|
| Critical  |    0 |     0 |        0 |
| High      |    0 |     0 |        0 |
| Medium    |    0 |     0 |        0 |
| Low       |    0 |     0 |        0 |
| Test Gaps |    0 |     0 |        0 |

_Update these counts as findings are closed. When the Open column is all
zeroes, the audit is "closed" — capture that fact in one line immediately
below the table._

---

## How to use this template

1. **One finding per subsection.** IDs are `C1..Cn`, `H1..Hn`, `M1..Mn`,
   `L1..Ln`, `T1..Tn`. IDs are stable for the life of this audit — never
   reuse a retired ID within the same audit file.
2. **External references use the full audit ID.** In code comments,
   commit messages, or other docs, always prefix with the audit ID:
   `AUDIT 2026-05-02-01/M1`, never bare `M1`.
3. **Mark fixed findings with a ✅.** Prepend `✅ ` to the heading and
   append `*(fixed YYYY-MM-DD)*`. Leave the full text in place so the
   audit is a historical record of both the problem and the fix.
4. **Mark deferred findings with → Deferred.** Append a pointer to the
   deferred-folder file that now owns the item, e.g.
   `→ Deferred (see [deferred/NN-slug.md](../planned-features/deferred/NN-slug.md))`.
5. **Every finding has a File line.** Point at a specific path + symbol,
   not a whole package.
6. **Every fix cites its tests.** A finding is not closed until at
   least one test exercises the fixed branch.

---

## Critical

### C1 — _short title_
**File:** `path/to/file.go` — _function or section_

_Description: what the bug is, why it is Critical, what an attacker (or a
runtime failure) could do with it._

**Fix:** _one-sentence plan._

---

## High

### H1 — _short title_
**File:** `path/to/file.go`

_Same shape as Critical, one severity down. Typical High items: auth
bypasses guarded by an unlikely flag, unbounded resource consumption,
information disclosure with real exploit value._

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

_Cosmetic, dead-code, or information-leak items worth writing down so the
next auditor doesn't re-discover them._

---

## Test Gaps

| #  | Status      | Test                                   |
|----|-------------|----------------------------------------|
| T1 | 🔲 Open     | `TestX_Y_Z` — _what branch it covers_  |

---

## Remediation Plan

_Pre-implementation design for the open findings above. Delete a
subsection once the finding is marked ✅ — the fix block under the
finding becomes the source of truth._

### Execution batches (YYYY-MM-DD)

| # | Batch | Findings | Risk | Primary files |
|---|-------|----------|------|----------------|
| 1 | _name_ | _IDs_ | low / medium / high | _paths_ |

---

## Deployment & Configuration Notes

_Known-gaps bucket. Items here are NOT security findings — they are
infrastructure decisions with acknowledged tradeoffs. Examples:_

- **No config file support** — all configuration via environment variables.
- **CA certificate TTL** — default and override env var.

---

## File-Size Hygiene — Large Files to Split (optional)

| File | Lines | Status | Target split |
|------|------:|--------|--------------|
| `path/to/big_file.go` | 0 | pending | _proposed split by concern_ |
