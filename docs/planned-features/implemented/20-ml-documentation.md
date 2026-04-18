# Feature: ML Documentation

**Priority:** P1
**Status:** Done. All five target docs updated; new user-facing
guide at `docs/ml-pipelines.md` with ten sections + runbook;
SECURITY.md threat table gained eight ML-specific rows.
**Affected files:** `docs/ARCHITECTURE.md` (ML section + REST
table + glossary + decisions + §13), `docs/COMPONENTS.md` (§5
ML subsystems), `docs/persistence.md` (three-tier storage +
`datasets/`/`models/` prefixes + in-memory ServiceRegistry
note), `docs/dashboard.md` (ML module section + API endpoints),
`docs/SECURITY.md` (8 new threat rows for ML surface), new
`docs/ml-pipelines.md`.
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## Documentation

- `docs/ARCHITECTURE.md` — added ML pipeline section (§13) with
  the three-tier storage diagram, component map,
  DispatchRequest proto additions, and trust-boundary summary.
  REST table extended with `/api/datasets`, `/api/models`,
  `/api/services`, `/workflows/{id}/lineage` endpoints.
  Glossary gained entries for Artifact store, Stager, `from:`
  reference, ResolvedOutputs, Dataset/model registry, Service
  job, and CUDA_VISIBLE_DEVICES. Decisions table gained rows
  for ML artifact store, artifact passing, and inference
  serving.
- `docs/COMPONENTS.md` — §5 "ML subsystems" with subsections
  for each of the five ML components (`internal/artifacts/`,
  `internal/staging/`, `internal/cluster/workflow_resolve.go`,
  `internal/registry/`, prober + service registry). Interface
  signatures + security posture per component.
- `docs/persistence.md` — key schema extended with
  `datasets/{name}/{version}` and `models/{name}/{version}`
  prefixes. "Dual-store" section replaced by "Three-tier
  storage" covering BadgerDB + object store + PostgreSQL with
  per-tier access patterns, consistency, and
  unavailability-effect table. Note added on
  `cluster.ServiceRegistry` being in-memory by design.
- `docs/dashboard.md` — shell component tree gained the `ml/`
  feature module; API endpoints table gained the seven ML
  routes; new "ML module" section covers the four routes + the
  security posture + the two iris test files
  (`ml-iris.spec.ts` for CI, `ml-iris-walkthrough.spec.ts`
  gated behind `E2E_RECORD_IRIS_WALKTHROUGH=1` for video
  regeneration).
- `docs/ml-pipelines.md` (new) — 10-section user-facing guide
  built around the iris example: what Helion gives you,
  walkthrough, writing your own workflow, `from:` references,
  registries, node labels + GPUs, inference services,
  dashboard views, security model, troubleshooting. Every
  section links back to the implementation doc in
  `planned-features/implemented/`.
- `docs/SECURITY.md` — threat table gained eight ML rows:
  cross-job URI reports (attestOutputs), undeclared output
  names (declared-name cross-check), tampered bytes
  (GetAndVerify SHA-256), token escalation (`job` role +
  `adminMiddleware`), token persistence (TTL + revoke), metadata
  DoS (MaxBytesReader + rate limit), GPU env-var escape
  (map-based CUDA_VISIBLE_DEVICES override), service-spec
  validator rules.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| None (docs only) | Published the ML threat rows in the main SECURITY.md table; cross-linked from ml-pipelines.md § 9 so operators see the model in one place. | §1 threat model extended; no new code paths. |

## What's NOT in scope

- Tutorial videos or click-through walkthroughs — covered by
  the existing `docs/e2e-full-run.mp4` + `docs/e2e-iris-run.mp4`.
- API reference generated from OpenAPI — the REST tables in
  `ARCHITECTURE.md` stay hand-curated for now; auto-generation
  would require a swagger/openapi annotation pass across the
  handlers, which is a separate slice.
- Per-cloud deployment guide — the Helm chart in
  `deploy/helm-chart/` already documents the per-cloud values
  files; ml-pipelines.md cross-links it rather than duplicating.
