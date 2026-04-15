# Done Audits

Audits whose Open column is all zeroes — every finding has been
fixed or explicitly deferred to a tracked backlog file. They move
here (keeping their original `YYYY-MM-DD-NN.md` ID) so the parent
`docs/audits/` directory stays focused on in-flight / partially-open
audits. This mirrors the `planned-features/` → `planned-features/implemented/`
and `deferred/` → `deferred/implemented/` patterns.

## Workflow

When the last Open finding in an audit closes:

1. Update the audit file's summary table so every Open column shows `0`.
2. `git mv docs/audits/<audit-id>.md docs/audits/done/<audit-id>.md`.
3. Update `docs/audits/README.md` to move the archive row from the
   active block into the done block (or note "Moved to done/" inline).
4. Any inbound references
   (`docs/audits/<audit-id>.md` in code comments, commit messages,
   feature docs) keep working via relative path updates.

"Deferred" counts as closed for this purpose — the finding is tracked
in `planned-features/deferred/` and no longer needs attention in the
audit itself.

## Files here

- `YYYY-MM-DD-NN.md` — one file per closed audit.
- `README.md` — this index.

## Archive

| Audit ID | Headline |
|----------|----------|
| [2026-04-15-01](2026-04-15-01.md) | Feature-11 exhaustive coverage audit — 6 test gaps + 2 Medium + 2 Low all fixed; no production-code defects |
| [2026-04-15-02](2026-04-15-02.md) | Feature-11 fourth-pass coverage audit — cross-backend contract lock added; declares coverage saturation |
| [2026-04-15-03](2026-04-15-03.md) | Feature-12 coverage audit — 2 test gaps fixed (ReportResult attestation wiring + live-MinIO Stager upload) |
| [2026-04-15-04](2026-04-15-04.md) | Feature-12 second-pass audit — output-size cap + stager-less refusal + Cleanup idempotency; declares feature 12 saturation |
