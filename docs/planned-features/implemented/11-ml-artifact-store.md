# Feature: ML Artifact Store

**Priority:** P1
**Status:** Done
**Affected files:** `internal/artifacts/` (new package), `docker-compose.yml` (MinIO service).
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## Artifact store abstraction

New package: `internal/artifacts/`.

```go
// internal/artifacts/store.go

// URI is an opaque artifact reference, e.g. "s3://helion/run-42/model.pt"
// or "file:///var/lib/helion/artifacts/run-42/model.pt".
type URI string

type Store interface {
    Put(ctx context.Context, key string, r io.Reader, size int64) (URI, error)
    Get(ctx context.Context, uri URI) (io.ReadCloser, error)
    Stat(ctx context.Context, uri URI) (Metadata, error)
    Delete(ctx context.Context, uri URI) error
}

type Metadata struct {
    Size      int64
    SHA256    string
    UpdatedAt time.Time
}
```

Two implementations:

- `LocalStore` — backed by a directory on the coordinator's host. Default for
  dev / single-node deployments. No new dependencies.
- `S3Store` — backed by any S3-compatible service (MinIO, AWS S3, GCS S3
  gateway). Uses `aws-sdk-go-v2`. The `docker-compose.yml` ships a MinIO
  service so the dev path matches prod.

Selection is by env var: `HELION_ARTIFACTS_BACKEND={local|s3}`,
`HELION_ARTIFACTS_PATH=/var/lib/helion/artifacts` (local) or
`HELION_ARTIFACTS_S3_ENDPOINT=...` + bucket + creds (S3).

The store is **not** in the coordinator's hot path — it is called by the API
when a user uploads an artifact, by nodes when staging inputs/outputs, and by
the registry when computing checksums.

## Implementation notes — artifact store (done)

Landed in [`internal/artifacts/`](../../../internal/artifacts/) with two
backends behind a single `Store` interface:

- `LocalStore` — filesystem root, `file://` URIs, atomic writes
  (tempfile → fsync → rename), streaming SHA-256 in `Stat`, context
  cancellation on every I/O chunk.
- `S3Store` — S3-compatible, `s3://<bucket>/<key>` URIs, via
  `github.com/minio/minio-go/v7`. Interface-level `s3Client` abstraction
  so unit tests run against an in-memory fake; a live integration test
  gates on `MINIO_TEST_ENDPOINT` for real-MinIO round-trips.
- `Open(Config)` factory driven by `HELION_ARTIFACTS_BACKEND` +
  backend-specific env vars.

Store-layer hardening (API-layer hardening deferred to the handler
step): key length cap (`MaxKeyLength = 1024`, matches S3 ceiling),
rejection of NUL + ASCII control bytes, rejection of absolute paths,
backslashes, Windows drive letters, traversal via `..`, URIs that
escape the store root, URIs that name a different bucket, and wrong
schemes.

Follow-ups landed on top of the initial step 1:

- **Root directory mode `0o700`** (files inside already `0o600`).
- **`S3Store` logs WARN on startup when `UseSSL=false`**, pointing
  operators at `HELION_ARTIFACTS_S3_USE_SSL=1` — harmless in the
  MinIO dev loop, loud in production.
- **`VerifyStore(ctx, store)`** — end-to-end Put→Get→Delete probe
  called from the node agent at startup (opt-in, gated on
  `HELION_ARTIFACTS_BACKEND`). A misconfigured deployment (typo'd
  bucket, bad creds, unreachable endpoint, missing write permission)
  fails loud here rather than silently at the first job.
- **`GetAndVerify(ctx, store, uri, expectedSHA256, maxBytes)`** +
  **`GetAndVerifyTo(ctx, store, uri, expectedSHA256, maxBytes, io.Writer)`** —
  paired readers that return (or stream) bytes only if their
  SHA-256 matches the caller-supplied digest. `GetAndVerifyTo` is
  the primary reader for multi-GB ML artifacts: every chunk flows
  through a TeeReader so memory use is O(64 KiB) regardless of
  object size, avoiding the OOM that the older all-in-memory
  helper would cause on large models. `GetAndVerify` is now a thin
  wrapper for small digest-known callers.
- **Docker Compose `minio` + `minio-bootstrap` services** under
  the `ml` profile. `docker compose --profile ml up` now ships a
  ready-to-use S3-compatible endpoint with the `helion` bucket
  pre-created.

Tests: **47 pass + 1 skipped live integration**
([`local_test.go`](../../../internal/artifacts/local_test.go),
[`s3_test.go`](../../../internal/artifacts/s3_test.go),
[`config_test.go`](../../../internal/artifacts/config_test.go),
[`verify_test.go`](../../../internal/artifacts/verify_test.go)).

**E2E / live-MinIO coverage** — the unit tests above all use the
in-memory `fakeS3` client; the single
`internal/artifacts/s3_test.go:TestS3_LiveIntegration` case that
talks to a real S3 endpoint is gated on `MINIO_TEST_ENDPOINT` and
was previously never wired into a CI path.
[`scripts/run-e2e.sh`](../../../scripts/run-e2e.sh) now activates
the `ml` compose profile, points
[`docker-compose.e2e.yml`](../../../docker-compose.e2e.yml)'s
coordinator + both nodes at the provisioned MinIO, and runs both
the unit-level live test and the new
[`tests/integration/artifacts_live_s3_test.go`](../../../tests/integration/artifacts_live_s3_test.go)
cases (round-trip, VerifyStore probe happy + bad-bucket, streaming
GetAndVerifyTo on a 1 MiB payload). The full node-agent → Stager →
MinIO flow (upload-on-finalize, resolve-and-download-on-next-job)
lives in features 12/13's surface, not feature 11's — reviewed
separately when those features get their coverage pass.

**Second-pass coverage additions** — follow-up audit found three
remaining gaps the original suite didn't explicitly assert against.
Each is now covered:

- [`local_test.go:TestConcurrentPuts_SameKey_NoOrphanTempfile`](../../../internal/artifacts/local_test.go)
  — 16 goroutines race to Put the same key; asserts (a) no error,
  (b) exactly one resolved file remains, (c) no orphan
  `.helion-artifact-*.tmp` survives, (d) the final bytes equal one
  of the submitted payloads verbatim. Locks in the invariant
  `local.go:82-108` claims and the "Deliberately not fixed" #2
  entry below depends on.
- [`verify_stream_test.go:TestGetAndVerifyTo_ContextCancelledMidStream`](../../../internal/artifacts/verify_stream_test.go)
  — 256 KiB payload via a slow reader that signals after the first
  4 KiB chunk; test cancels ctx mid-copy, asserts the helper
  returns a `context.Canceled`-wrapped error and **not** an
  `ErrChecksumMismatch` (so an operator cancel never looks like
  artifact-store tampering on the audit trail).
- [`dashboard/e2e/specs/ml-artifacts.spec.ts`](../../../dashboard/e2e/specs/ml-artifacts.spec.ts)
  — Playwright pass that exercises the dashboard → REST → registry
  chain with `HELION_ARTIFACTS_BACKEND=s3` in place. Registers a
  dataset with an `s3://helion/...` URI, verifies list + tag-filter
  + delete, and asserts the server-side URI-scheme validator
  surfaces clearly when an `http://` URI is attempted. Indirect
  coverage of the artifact store's URI contract through the same
  handler path a real user would hit.

**Third-pass coverage additions** — final exhaustive sweep,
documented in audit [`2026-04-15-01`](../../audits/done/2026-04-15-01.md).
A fourth follow-on pass
[`2026-04-15-02`](../../audits/2026-04-15-02.md) added the
cross-backend contract lock and declared coverage saturation.
Three more alarms the prior passes hadn't set, each closed:

- [`local_test.go:TestLocalStore_Permissions`](../../../internal/artifacts/local_test.go)
  — asserts root directory is 0o700, freshly-Put files are 0o600,
  and nested intermediate dirs are 0o700. Security-relevant
  regression surface (a silent drop to `os.MkdirAll`'s default
  0o755 would expose model checkpoints + training data to any
  local user); Linux-only, skipped on Windows.
- [`s3_test.go:TestNewS3Store_WarnsWhenUseSSLDisabled`](../../../internal/artifacts/s3_test.go)
  — slog-captures `NewS3Store` output, asserts the documented
  WARN line fires when `UseSSL=false` and is absent when
  `UseSSL=true`. Locks in the operator-visible alarm that an
  unencrypted S3 config trips on every node startup.
- [`artifacts_live_s3_test.go:TestLiveS3LargeObjectRoundtrip`](../../../tests/integration/artifacts_live_s3_test.go)
  — 10 MiB payload through live MinIO. Fails on any regression
  in Content-Length handling, reader-position drift, or the
  multipart-adjacent code path the fakeS3 doesn't model.

**Fourth-pass coverage addition + saturation.** Audit
[`2026-04-15-02`](../../audits/2026-04-15-02.md) landed a single
test —
[`contract_test.go:TestStoreContract_IdenticalAcrossBackends`](../../../internal/artifacts/contract_test.go),
which parametrises over `LocalStore` + `S3Store` (fakeS3-backed)
and runs an identical Put / Get / Stat / Delete / double-Delete /
empty-payload sequence against both, locking in that they agree
on every observable (error sentinels, empty-bytes handling, the
well-known empty-SHA constant). Ran to check whether a
speculative future refactor of one backend could drift against
the other without any test noticing; no such drift exists today,
and the test's purpose is to fail loudly if one is ever
introduced. The audit also explicitly **declares coverage
saturation** after this addition — nothing else I looked at
cleared the bar of "would catch a real regression, not a
contrived one." Future audits on this feature should focus on
downstream consumers (Stager, registry, workflow resolver), not
on the Store itself.

### Deliberately not fixed, with rationale

A second-pass audit flagged three concerns in the artifact-store
surface that were *not* addressed. Each is recorded here so a future
auditor doesn't re-raise it as an oversight:

1. **`S3Store.Delete` TOCTOU race.** The current implementation
   ([s3.go:213-216](../../../internal/artifacts/s3.go#L213-L216)) calls
   `StatObject` to probe existence, then `RemoveObject`. Between the
   two, another caller (or an operator using `mc`) could remove the
   object — our RemoveObject still succeeds silently (minio-go
   swallows NoSuchKey for idempotency), so we return `nil` for a
   delete we didn't perform.

   **Why not fixed:** The race window is milliseconds wide, and both
   observable outcomes are "object is gone." No caller in this repo
   distinguishes "I deleted it" from "it was already gone"; the Stager
   uses Delete only for opportunistic cleanup. Under the primary
   threat model (compromised node) a node cannot exploit the race to
   retain stale state — it already controls its own job's outputs.
   The alternative fix (drop the `ErrNotFound` contract and let
   `Delete` be idempotent by design) is a breaking API change for no
   observable win. Accept the documented race.

2. **`LocalStore.Put` concurrent-same-key tempfile orphans.** The
   audit asserted that racing Puts on the same key leave orphaned
   tempfiles. **This is not actually true.** Every error path in
   [local.go:82-108](../../../internal/artifacts/local.go#L82-L108)
   cleans up the tempfile explicitly, and a successful
   `os.Rename(tmpPath, full)` *consumes* the tempfile (the tempfile
   no longer exists post-rename). Two racing Puts each produce their
   own tempfile; both rename onto the same destination in order;
   last-writer-wins on `full`; neither tempfile survives. No dust
   actually accumulates.

3. **Library-mode callers bypassing the API validators.** A library
   caller (internal Go code, not going through POST /jobs) could
   construct a `cpb.Job` with `Inputs[i].LocalPath = "../escape"` and
   call `JobStore.Submit` directly, bypassing
   `validateArtifactBindings`. This is a real bypass at the API
   boundary, but the Stager's
   [`safeJoin`](../../../internal/staging/staging.go) re-validates
   every `local_path` before touching disk — a belt-and-braces guard
   that refuses traversal at the point it would matter. Any library
   caller would still fail at `Prepare` with "local_path escapes
   working directory." Accept the two-layer defense; don't duplicate
   the shape rules into `JobStore.Submit`.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| Bulk storage; cross-tenant key collisions | Key sanitisation (NUL, control, `..`, drive letters, length≤1024); URI bucket + scheme pin in `S3Store`; `O_EXCL` on LocalStore; `Lstat` rejections on uploads | §8 REST API security (bounded input) pattern applied at the Store boundary |
