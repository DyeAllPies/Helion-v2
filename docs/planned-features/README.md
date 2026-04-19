# Planned Features

Feature specs, one file per slice. See
[`../DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md) for how this folder
relates to `audits/` and `deferred/`.

- [`TEMPLATE.md`](TEMPLATE.md) — copy this when starting a new feature.
- `NN-kebab-slug.md` — active feature specs (next unused two-digit number).
- [`deferred/`](deferred/) — items consciously pushed past the current
  scope. Template: [`deferred/TEMPLATE.md`](deferred/TEMPLATE.md).
- [`implemented/`](implemented/) — features that have fully shipped
  and passed an audit reconciling spec vs reality. Moving them out of
  the active list keeps the index focused on what's still in flight.

## Active features

| #  | Feature | Status | Priority | Doc |
|---:|---------|--------|----------|-----|
| 01 | Workflow / DAG support | **Done** | P0 | [01-workflow-dag.md](01-workflow-dag.md) |
| 02 | Retry + failure policies | **Done** | P0 | [02-retry-failure-policies.md](02-retry-failure-policies.md) |
| 03 | Resource-aware scheduling | **Done** | P1 | [03-resource-aware-scheduling.md](03-resource-aware-scheduling.md) |
| 04 | Job state machine improvements | **Done** | P1 | [04-job-state-machine.md](04-job-state-machine.md) |
| 05 | Priority queues | **Done** | P1 | [05-priority-queues.md](05-priority-queues.md) |
| 06 | Event system | **Done** | P2 | [06-event-system.md](06-event-system.md) |
| 07 | Observability improvements | **Done** | P2 | [07-observability.md](07-observability.md) |
| 08 | Deferred-enhancements index (legacy) | Archived | — | [08-deferred-enhancements.md](08-deferred-enhancements.md) |
| 09 | Analytics pipeline (BadgerDB → PostgreSQL) | **Done** | P1 | [09-analytics-pipeline.md](09-analytics-pipeline.md) |
| 10 | Minimal ML pipeline (umbrella) | **Done** (all 10 slices landed + audited + acceptance-green) | P1 | [10-minimal-ml-pipeline.md](10-minimal-ml-pipeline.md) |
| ~~11~~ | ~~ML — Artifact store abstraction~~ | **Implemented**; moved to [`implemented/11-ml-artifact-store.md`](implemented/11-ml-artifact-store.md) | P1 | — |
| ~~12~~ | ~~ML — Job spec: inputs/outputs/working_dir~~ | **Implemented**; moved to [`implemented/12-ml-job-io-staging.md`](implemented/12-ml-job-io-staging.md) | P1 | — |
| ~~13~~ | ~~ML — Inter-job artifact passing in workflows~~ | **Implemented**; moved to [`implemented/13-ml-workflow-artifact-passing.md`](implemented/13-ml-workflow-artifact-passing.md) | P1 | — |
| ~~14~~ | ~~ML — Node labels and selectors~~ | **Implemented**; moved to [`implemented/14-ml-node-labels-and-selectors.md`](implemented/14-ml-node-labels-and-selectors.md) | P1 | — |
| ~~15~~ | ~~ML — GPU as a first-class resource~~ | **Implemented**; moved to [`implemented/15-ml-gpu-first-class-resource.md`](implemented/15-ml-gpu-first-class-resource.md) | P1 | — |
| ~~16~~ | ~~ML — Dataset and model registry~~ | **Implemented** (Go runtime; audit 2026-04-14-01 M3/M4/L1/L3 deferred); moved to [`implemented/16-ml-dataset-model-registry.md`](implemented/16-ml-dataset-model-registry.md) | P1 | — |
| ~~17~~ | ~~ML — Inference jobs~~ | **Implemented** (Go runtime; Rust parity deferred/20); moved to [`implemented/17-ml-inference-jobs.md`](implemented/17-ml-inference-jobs.md) | P1 | — |
| ~~18~~ | ~~ML — Dashboard module~~ | **Implemented + audited**; moved to [`implemented/18-ml-dashboard-module.md`](implemented/18-ml-dashboard-module.md) | P2 | — |
| ~~19~~ | ~~ML — End-to-end iris demo~~ | **Implemented + acceptance-green (2026-04-18)**; moved to [`implemented/19-ml-end-to-end-demo.md`](implemented/19-ml-end-to-end-demo.md) | P1 | — |
| ~~20~~ | ~~ML — Documentation~~ | **Implemented**; moved to [`implemented/20-ml-documentation.md`](implemented/20-ml-documentation.md) | P2 | — |
| ~~21~~ | ~~ML — MNIST local E2E (progression-observing)~~ | **Implemented** (six tests green locally; also fixed a hidden 10 s dispatch RPC ceiling); moved to [`implemented/21-ml-mnist-e2e-local.md`](implemented/21-ml-mnist-e2e-local.md) | P2 | — |
| 22 | Dashboard submission tab (jobs / workflows / ML workflows + DAG builder) | **Shipped** (UI + DAG builder; server hardening 24-26 pending) | P1 | [22-ui-submission-tab.md](22-ui-submission-tab.md) |
| ~~23~~ | ~~Hybrid-PQC on coordinator REST + WebSocket listener~~ | **Implemented** (code + 8 tests shipped; existing e2e overlays opt out via `HELION_REST_TLS=off` pending the batch e2e migration); moved to [`implemented/23-rest-hybrid-pqc.md`](implemented/23-rest-hybrid-pqc.md) | P1 | — |
| ~~24~~ | ~~Dry-run preflight (`?dry_run=true` on submit endpoints)~~ | **Implemented** (jobs + workflows + datasets + models — deferred item rolled in); moved to [`implemented/24-dry-run-preflight.md`](implemented/24-dry-run-preflight.md) | P1 | — |
| ~~25~~ | ~~Dangerous-env denylist (`LD_*`, `DYLD_*`, `GCONV_PATH`, …)~~ | **Implemented** (jobs + workflows + artifact staging guards + per-node overrides — both deferred items rolled in); moved to [`implemented/25-env-var-denylist.md`](implemented/25-env-var-denylist.md) | P1 | — |
| ~~26~~ | ~~Secret env-var support (redact on GET + scrub audit)~~ | **Implemented** (redaction + admin reveal-secret endpoint; two Not-attempting items promoted to features 29 + 30); moved to [`implemented/26-secret-env-vars.md`](implemented/26-secret-env-vars.md) | P1 | — |
| ~~27~~ | ~~Browser mTLS for dashboard operators (opt-in; depends on 23)~~ | **Implemented** (3-tier gating + admin `POST /admin/operator-certs` + CLI; four deferred/out-of-scope items promoted to features 31–34); moved to [`implemented/27-browser-mtls.md`](implemented/27-browser-mtls.md) | P2 | — |
| ~~28~~ | ~~Unified analytics sink — capture every interesting event~~ | **Implemented** (7 new sink families + 7 REST endpoints + retention cron + PII hashing + per-job log ingestion + Analytics panels; 4 deferred items filed under deferred/29–32); moved to [`implemented/28-analytics-unified-sink.md`](implemented/28-analytics-unified-sink.md) | P2 | — |
| 29 | Job stdout/stderr secret scrubbing (closes feature 26's "echo $HELION_TOKEN" gap) | Pending | P2 | [29-stdout-secret-scrubbing.md](29-stdout-secret-scrubbing.md) |
| 30 | Encrypted env-value storage (envelope encryption + KEK rotation) | Pending | P2 | [30-encrypted-env-storage.md](30-encrypted-env-storage.md) |
| 31 | Operator-cert revocation via CRL / OCSP | Pending | P2 | [31-cert-revocation-crl-ocsp.md](31-cert-revocation-crl-ocsp.md) |
| 32 | Web-based operator-cert issuance UI (admin dashboard action) | Pending | P3 | [32-web-cert-issuance-ui.md](32-web-cert-issuance-ui.md) |
| 33 | Per-operator accountability — JWT `required_cn` bound to cert CN | Pending | P2 | [33-per-operator-accountability.md](33-per-operator-accountability.md) |
| 34 | WebAuthn / FIDO2 — hardware-bound keys mitigate compromised-browser risk | Pending | P2 | [34-webauthn-fido2.md](34-webauthn-fido2.md) |
| ~~35~~ | ~~IAM foundation — Principal model & auth-to-principal resolution~~ | **Implemented** (principal package + middleware + audit schema + dashboard badge); moved to [`implemented/35-principal-model.md`](implemented/35-principal-model.md) | P1 | — |
| ~~36~~ | ~~IAM — Resource ownership on every stateful type (jobs, workflows, datasets, models, services)~~ | **Implemented** (OwnerPrincipal on Job/Workflow/Dataset/Model/ServiceEndpoint + legacy backfill on load + preserve-owner tests + audit resource_owner detail); moved to [`implemented/36-resource-ownership.md`](implemented/36-resource-ownership.md) | P1 | — |
| ~~37~~ | ~~IAM — Authorization policy engine + middleware (replaces ad-hoc RBAC)~~ | **Implemented** (internal/authz package + per-resource RBAC on jobs/workflows/datasets/models/services + authz_deny audit + deny code response + legacy claims.Subject/SubmittedBy check removed + node-role JWTs denied on REST + DisableAuth stamps dev-admin); moved to [`implemented/37-authorization-policy.md`](implemented/37-authorization-policy.md) | P1 | — |
| 38 | IAM — Groups and resource shares (delegation beyond owner-or-admin) | Pending | P2 | [38-groups-and-shares.md](38-groups-and-shares.md) |

### Priority definitions

- **P0** — Required for minimal orchestrator.
- **P1** — Required for production use.
- **P2** — High-impact improvements but not blockers.
- **P3** — Backlog. Used on deferred items; see [`deferred/`](deferred/).
