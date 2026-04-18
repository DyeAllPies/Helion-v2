# Audits

Security & code-quality audits, one file per run.

## Files here

- [`TEMPLATE.md`](TEMPLATE.md) — copy this when starting a new audit.
  Instructions at the top.
- `YYYY-MM-DD-NN.md` — in-flight or partially-open audits (at least
  one Open finding still outstanding).
- [`done/`](done/) — audits whose Open column is all zeroes. Moved
  here once the last finding closes so the main list stays focused
  on what still needs attention.

## Active

| Audit ID | Headline |
|----------|----------|
| [2026-04-11-01](2026-04-11-01.md) | Initial full-system audit — closed all 10 findings |
| [2026-04-12-01](2026-04-12-01.md) | Second-pass audit after dispatch hardening — closed all 10 findings |
| [2026-04-14-01](2026-04-14-01.md) | ML registry slice audit — M1/M2 fixed, M3/M4/L1/L3 deferred |
| [2026-04-14-02](2026-04-14-02.md) | Inference-jobs slice audit — M2/M3/L2/L3/T2 fixed, M1/L1/T1 deferred |

> Historical note: the prior four `YYYY-MM-DD-NN.md` rows are all
> technically closed (every Open column is zero) but remain in the
> active list because some of their deferred items still have open
> entries under `planned-features/deferred/`. A fully-closed audit
> with no deferrals outstanding — like
> [`done/2026-04-15-01.md`](done/2026-04-15-01.md) — moves out.

## Done

| Audit ID | Headline |
|----------|----------|
| [done/2026-04-15-01](done/2026-04-15-01.md) | Feature-11 exhaustive coverage audit — 6 test gaps + 2 Medium + 2 Low all fixed; no production-code defects |
| [done/2026-04-15-02](done/2026-04-15-02.md) | Feature-11 fourth-pass coverage audit — cross-backend contract lock added; declares coverage saturation |
| [done/2026-04-15-03](done/2026-04-15-03.md) | Feature-12 coverage audit — 2 test gaps fixed (ReportResult attestation wiring + live-MinIO Stager upload); no production-code defects |
| [done/2026-04-15-04](done/2026-04-15-04.md) | Feature-12 second-pass audit — 1 Medium + 1 Low + 1 Test Gap fixed (output-size cap, stager-less refusal, Cleanup idempotency); declares feature 12 saturation |
| [done/2026-04-15-05](done/2026-04-15-05.md) | Feature-12 third-pass audit — production fix: `attestOutputs` now cross-checks reported output Names against `Job.Outputs` declaration; 1 Low + 2 Test Gaps; recants the prior pass's saturation claim |
| [done/2026-04-15-06](done/2026-04-15-06.md) | Feature-12 fourth-pass audit — 1 Test Gap fixed (empty-`workRoot` fallback); six items considered and dismissed; calibration-based recommendation to move on to feature 13 |
| [done/2026-04-15-07](done/2026-04-15-07.md) | Feature-13 first-pass audit — 1 Test Gap fixed (`ml.resolve_failed` event observer); 11 items dismissed; recommends feature 14 or 15 next |
| [done/2026-04-15-08](done/2026-04-15-08.md) | Feature-13 second-pass audit — production fix: two sibling `<upstream>.<output>` splitters converted from first-dot to last-dot to match the canonical contract; 1 Low + 2 Test Gaps; recants 07's saturation claim |
| [done/2026-04-15-09](done/2026-04-15-09.md) | Feature-14 first-pass audit — 1 Test Gap fixed (`job.unschedulable` event `reason`-field observer wiring); 8 items dismissed; generalises the "pin every load-bearing event payload field" pattern |

## Workflow

1. Copy `TEMPLATE.md` to `docs/audits/<date>-<NN>.md`. `NN` is a
   two-digit counter starting at `01` for the first audit that day;
   use `02`, `03` … for additional audits on the same date.
2. Fill in the Date, Auditor, and Overall Risk Level fields.
3. Work each finding to close (`✅ Fixed`) or **defer** (move into
   `docs/planned-features/deferred/` as a new numbered file — see
   `deferred/TEMPLATE.md`).
4. Update `docs/audits/README.md` (this file) with the new audit ID
   and a one-line headline under the Active block.
5. Commit. Audit findings referenced outside this folder always use
   the full `AUDIT <audit-id>/<severity-letter><n>` form, e.g.
   `AUDIT 2026-04-11-01/H2`.
6. When the last deferred item gets closed or dismissed (so nothing
   about the audit requires attention anymore), `git mv` the file
   into `done/` and move its row from the Active to the Done table
   here. See [`done/README.md`](done/README.md) for the pattern.

See the top-level [`docs/DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md) for
how audits connect to the planned-features and deferred folders.
