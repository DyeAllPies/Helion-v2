# Feature: ML Documentation

**Priority:** P1
**Status:** Pending
**Affected files:** `docs/ARCHITECTURE.md`, `docs/COMPONENTS.md`, `docs/persistence.md`, `docs/dashboard.md`, `docs/ml-pipelines.md` (new).
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## Documentation

- `docs/ARCHITECTURE.md` — add ML pipeline section + dual-store note
  (artifacts on object store, metadata in Badger, analytics in PG).
- `docs/COMPONENTS.md` — artifact store, registry, service mode.
- `docs/persistence.md` — clarify three-tier storage:
  Badger (operational metadata) / object store (artifact bytes) /
  PostgreSQL (analytics).
- `docs/dashboard.md` — ML module pages.
- New `docs/ml-pipelines.md` — user-facing "how to write an ML workflow"
  guide built around the iris example.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| None | Updates the SECURITY.md threat table to include the ML rows described in the parent slice | — |
