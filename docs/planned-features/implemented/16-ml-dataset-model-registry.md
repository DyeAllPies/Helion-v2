# Feature: ML Dataset and Model Registry

**Priority:** P1
**Status:** Done
**Affected files:** `internal/registry/` (new), `internal/api/handlers_registry.go` (new), `cmd/helion-coordinator/main.go`.
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## Dataset and model registries

New package: `internal/registry/`. Two parallel resources, both backed by
BadgerDB (metadata only — the bytes live in the artifact store).

```go
type Dataset struct {
    Name      string            // unique
    Version   string            // semver-ish, user-supplied
    URI       artifacts.URI
    SizeBytes int64
    SHA256    string
    Tags      map[string]string // free-form
    CreatedAt time.Time
    CreatedBy string            // JWT subject
}

type Model struct {
    Name        string
    Version     string
    URI         artifacts.URI
    Framework   string            // "pytorch" | "onnx" | "tensorflow" | "other"
    SourceJobID string            // training job that produced it
    SourceDataset struct {        // lineage pointer
        Name    string
        Version string
    }
    Metrics  map[string]float64   // user-reported eval metrics
    Tags     map[string]string
    CreatedAt time.Time
    CreatedBy string
}
```

REST API:

```
POST   /api/datasets                 — register a new dataset (multipart upload)
GET    /api/datasets                 — list with filter on tags
GET    /api/datasets/:name/:version  — fetch metadata + signed URI
DELETE /api/datasets/:name/:version  — delete (artifact + metadata)

POST   /api/models                   — register a new model
GET    /api/models                   — list
GET    /api/models/:name/:version    — fetch metadata + URI
GET    /api/models/:name/latest      — convenience: highest semver
DELETE /api/models/:name/:version
```

All endpoints JWT-authenticated, audited (`dataset.registered`,
`model.registered`, etc. — emitted on the event bus so the analytics
pipeline picks them up automatically).

Lineage is recorded but not enforced — a model's `SourceJobID` is whatever
the registering call says it is. We trust the training job to register its
own outputs and stamp the lineage; we do not attempt to *infer* lineage
from artifact URIs.

## Implementation notes — dataset + model registries (done)

Two parallel resources, metadata only — the bytes stay in the
artifact store ([step 1](11-ml-artifact-store.md), sibling in this folder). The registry answers "what does this
named model look like, by whom and when?"; the artifact store
answers "how do I fetch its bytes?" Same split you'd find in
MLflow / W&B / CometML, minimal cut.

Data + persistence
([`internal/registry/`](../../../internal/registry/)):

- `Dataset` and `Model` structs carry URI + size + SHA256 (copied
  from the artifact store's Stat so downstream callers can verify
  without a second round-trip) + tags + CreatedAt + CreatedBy
  (JWT subject).
- `Model` adds lineage — `SourceJobID` + `SourceDataset{Name,
  Version}` + `Metrics map[string]float64`. Lineage is recorded
  but not *enforced*; the registrar is trusted to stamp the right
  pointers, matching the broader trust model where node-reported
  outputs are gated by `attestOutputs` but user-supplied metadata
  at the REST boundary rides on the JWT subject.
- Immutable once registered — version bumps create a new entry.
  Keeps the audit story simple and matches how every registry
  in the space behaves.
- `BadgerStore` shares the coordinator's existing BadgerDB under
  `datasets/<name>/<version>` and `models/<name>/<version>` key
  prefixes. No separate DB file — metadata is small and low-
  traffic; isolation isn't worth the operational overhead.
- `LatestModel(name)` walks the `models/<name>/` prefix and picks
  the entry with the newest `CreatedAt`. Chronological, not
  semantic — if a registrar backfills an older version after a
  newer one, the newer one wins.

REST surface
([`internal/api/handlers_registry.go`](../../../internal/api/handlers_registry.go)):

```
POST   /api/datasets
GET    /api/datasets                      (paginated)
GET    /api/datasets/{name}/{version}
DELETE /api/datasets/{name}/{version}

POST   /api/models
GET    /api/models                        (paginated)
GET    /api/models/{name}/{version}
GET    /api/models/{name}/latest
DELETE /api/models/{name}/{version}
```

Every endpoint rides the shared `authMiddleware` (JWT required);
mutating endpoints run the per-subject `registryQueryAllow`
limiter (2 rps, burst 30 — same shape as the analytics limiter
for consistency); success emits an audit record + event-bus
event. Registry routes are registered only when
`SetRegistryStore` is called at coordinator startup, so a
deployment that didn't opt in returns 404 from the mux rather
than exposing phantom endpoints.

Validation
([`internal/registry/validate.go`](../../../internal/registry/validate.go)):

- **Name:** lowercase alnum + `-._` (k8s DNS label charset). Shell-
  / URL- / BadgerDB-key-safe without escaping at any layer.
- **Version:** broader charset (accepts `v1.0.0`, `2026-04-14`,
  `git-abc1234`, SemVer `+build` metadata) but still caps length
  and rejects control bytes / spaces.
- **URI:** must start with `file://` or `s3://` — same allowlist
  the rest of the ML pipeline uses. NUL / control rejection and
  length cap mirror `validateArtifactBindings`.
- **Tags:** k8s-label-shaped bounds (≤32 entries, 63-byte keys,
  253-byte values, no `=` or NUL in keys). Same rule as node
  labels so a user's mental model is "Helion tags behave like
  k8s labels."
- **Metrics:** ≤64 entries, 63-byte keys, NaN / ±Inf rejected
  (won't round-trip through `encoding/json`).
- **Lineage:** `Model.SourceDataset` is all-or-nothing — partial
  pointers (name without version) are rejected so the audit
  record is never half-formed.

Security posture (matches step 2-5 patterns — no new crypto,
inherits mTLS + hybrid PQ channel):

- JWT subject → CreatedBy → audit actor → rate-limit key. One
  identity thread through the whole request.
- `maxSubmitBodyBytes = 1 MiB` cap on the POST body (shared with
  the job-submit handler) so a malicious caller can't stream MB
  of free-form tags.
- Delete of an active model's registry entry does *not* delete
  the underlying artifact bytes — the registry holds pointers,
  not the data. Artifact GC is a step-6-adjacent deferred item
  (see the [deferred backlog](../deferred/README.md)).

Deliberately deferred in this slice (recorded for clarity):

- **URI existence check at register time.** Would require wiring
  the artifact store into the coordinator (today only nodes open
  one). A caller can register a bogus URI; downstream consumers
  (workflow resolver, signed-URL fetch) detect it when they
  actually try to read. Low-value enforcement for the minimal
  cut — the JWT+audit trail already names who registered the
  bad pointer.
- **Artifact GC on registry delete.** A deleted `Model` record
  leaves its bytes in the artifact store. Pinning (artifacts
  referenced by a live registry entry survive; others get GC'd
  after a TTL) is the intended end state — filed as an open
  question in this doc, will land alongside the upload-via-REST
  handler that doesn't exist yet.
- **`GET /api/datasets?tag=foo` filter.** Tag-based search is
  a list-endpoint convenience. The current list is paginated
  newest-first; a client can filter Go-side. Nice to have, not
  on the critical path.

Tests (33 new, all CI-safe):

- [`registry/validate_test.go`](../../../internal/registry/validate_test.go)
  — name / version / URI / tags / metrics / dataset / model
  validators, including partial-lineage rejection + NaN/±Inf +
  oversize + control-byte rejection.
- [`registry/badger_test.go`](../../../internal/registry/badger_test.go)
  — Dataset and Model roundtrips, `ErrAlreadyExists` on dup
  version, `ErrNotFound` on missing, list newest-first +
  pagination, delete-is-version-specific, `LatestModel`
  chronological (not semantic), cross-type isolation
  (dataset "x" doesn't collide with model "x").
- [`api/handlers_registry_test.go`](../../../internal/api/handlers_registry_test.go)
  — HTTP surface: register/get/list/delete for both
  resources, 409 on dup, 404 on missing, 400 on bad scheme /
  partial lineage / NaN metric, pagination, `/latest`
  returning most-recent, event bus publishing on register +
  delete, registry-gated 404 when `SetRegistryStore` wasn't
  called.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| New write endpoints, artifact uploads | JWT required; per-subject rate limit (mirror `analyticsQueryAllow` shape: 2 rps burst 30 → 429); audit events `dataset.registered`, `dataset.deleted`, `model.registered`, `model.deleted` with actor; `http.MaxBytesReader` cap at 5 GiB (open-question: multipart path); URL-ownership check binds a registered URI to the caller's subject or an owning job-ID prefix | — |

Threat additions handled here:

| Threat | Mitigation |
|---|---|
| Artifact upload API DoS (unauthenticated, flood) | JWT + per-subject token bucket (mirror analytics limiter) + `http.MaxBytesReader` |
| Registry write without audit | `dataset.registered` / `model.registered` audit events on success |
| Registered model references an artifact the registrar never owned | Server-side check: registered URI must be under a key prefix keyed off the caller's JWT subject or the originating job's ID |

Audit event taxonomy:

| Event | Actor | Target | Details |
|---|---|---|---|
| `artifact.registered` | JWT subject | `dataset:<name>@<version>` or `model:<name>@<version>` | `{uri, size, sha256, source_job_id}` |
| `artifact.deleted` | JWT subject | same | `{uri}` |
| `artifact.get` | JWT subject | same | `{endpoint}` — at read API, rate-limited before audit (same as analytics) |
