# Feature: Encrypted storage for secret env values

**Priority:** P2
**Status:** Pending
**Affected files:**
`internal/cluster/persistence_jobs.go` (envelope encryption on write),
`internal/proto/coordinatorpb/types.go` (new `EncryptedEnv` blob),
`internal/api/handlers_jobs.go` (decrypt at dispatch),
`cmd/helion-coordinator/main.go` (key material config),
`docs/SECURITY.md` (new threat row + key management section),
new `internal/secretstore/` package for the crypto + key rotation.

## Problem

Feature 26 redacts secret env values on every response path and
mediates admin read-back via an audited endpoint. But the
underlying storage still holds plaintext:

- `cpb.Job.Env` is a `map[string]string` that BadgerDB persists via
  `json.Marshal`. Every secret lives on disk in the clear under
  the coordinator's data dir.
- An attacker with filesystem access to the coordinator (a disk
  snapshot pull, a stolen node image, a privileged ops user) reads
  every HF token, AWS key, etc. by grepping the Badger store.
- A backup of the coordinator's Badger dir is a snapshot of every
  secret the coordinator has ever seen.

Feature 26's spec acknowledged this ("Not attempting") because the
coordinator needs plaintext to dispatch — but the acknowledgement
is not the same as a plan. Promoting to a planned item so the gap
closes with the right crypto hygiene rather than an ad-hoc patch.

## Current state

- Secret values are persisted in `cpb.Job.Env` plaintext, JSON-
  marshaled.
- `feature 23` ships hybrid-PQC on the REST + WebSocket listener,
  so secrets are protected in transit but not at rest.
- No KMS / Vault / key-management integration exists yet.

## Design

**Envelope encryption with a coordinator-held root KEK (key
encryption key), one DEK (data encryption key) per job.**

1. Coordinator boot reads a root KEK from a single env var (simple)
   OR a configured KMS (production). Minimum viable config is an
   env-var-held 32-byte key for dev/testing; production expects
   one of: AWS KMS, GCP KMS, or HashiCorp Vault.
2. On submit: for each key in `SecretKeys`, generate a fresh
   per-job DEK (32 bytes). Encrypt the value with the DEK
   (AES-256-GCM, nonce is 12 random bytes). Encrypt the DEK
   with the root KEK the same way. Store the resulting
   `{ciphertext, nonce, wrapped_dek, wrapped_dek_nonce}` blob on
   the Job record in a new `EncryptedEnv` field; REMOVE the value
   from `Env` so nothing sees plaintext after the submit handler
   returns.
3. On dispatch: unwrap the DEK with the root KEK, decrypt the
   value, populate `RunRequest.Env` in memory, send to the node.
   Never persist the decrypted value.
4. On reveal-secret (feature 26): same dispatch decrypt path,
   but the plaintext goes into the response instead of a
   `RunRequest`.

### Wire additions

```go
// internal/proto/coordinatorpb/types.go
type EncryptedEnvValue struct {
    Ciphertext      []byte `json:"ciphertext"`       // AES-256-GCM of the value
    Nonce           []byte `json:"nonce"`            // 12 bytes, random per-encrypt
    WrappedDEK      []byte `json:"wrapped_dek"`      // AES-256-GCM of the DEK
    WrappedDEKNonce []byte `json:"wrapped_dek_nonce"`// 12 bytes
    KEKVersion      uint32 `json:"kek_version"`      // for rotation
}

type Job struct {
    ...
    // Secret values live here; Env keeps only non-secret entries.
    EncryptedEnv map[string]*EncryptedEnvValue `json:"encrypted_env,omitempty"`
}
```

### Rotation

`KEKVersion` stamps each wrapped DEK with which KEK wrapped it.
Rotation rewraps each DEK under the new KEK during a background
sweep; running jobs remain dispatchable under the old version
until rewrap completes.

## Security plan

| Threat | Control |
|---|---|
| Attacker with filesystem / backup access reads plaintext secrets | Envelope-encrypted at rest with a root KEK that never lives on the same disk (env var at boot or KMS-held). Badger blob carries only the encrypted form. |
| Leaked root KEK decrypts all past secrets | KEK rotation with per-wrap version tag; rewrap sweep rotates the blob set. Operators who suspect KEK compromise rotate + rewrap immediately. |
| Dispatch path logs plaintext to stderr | Existing slog filter (feature 26) already redacts. Decrypt happens in memory, value passed directly to `RunRequest.Env`, never formatted into a log line. |
| Node compromise reveals the DEK | DEK is sent over the wire only wrapped in the hybrid-PQC TLS from feature 23 + only the already-decrypted VALUE reaches the node, not the DEK. A compromised node sees the plaintext it was going to run anyway — no uplift. |
| Coordinator keeps the root KEK in memory; core dump leaks it | Use OS keyring on Linux (`memfd_secret` where available) for the KEK. `mlock`-style best-effort for older kernels. Document that a coordinator core dump is considered a key-compromise event. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `internal/secretstore/` — pure AES-GCM envelope ops + unit tests (happy path, tamper-detect, wrong-KEK decrypt, rotation). | — | Medium |
| 2 | `cpb.Job.EncryptedEnv` field + JSON tag; backward-compat with existing plaintext-Env records (decrypt only when the field is non-empty). | 1 | Small |
| 3 | Submit handler encrypts secret values + strips them from `Env`. | 2 | Small |
| 4 | Dispatch path decrypts + populates the node RunRequest. | 2 | Small |
| 5 | Reveal-secret handler (feature 26) updated to decrypt via the same path. | 2 | Small |
| 6 | KEK config: env-var source for dev + `HELION_SECRETSTORE_KMS=<provider>` for prod. | 1 | Medium |
| 7 | Rotation sweep + admin endpoint `POST /admin/secretstore/rotate`. | 2 | Medium |
| 8 | Docs + SECURITY.md + operational guide for KEK setup and rotation. | 1-7 | Small |

## Tests

- `TestEnvelope_EncryptDecrypt_RoundTrip` — wrap + unwrap + decrypt
  produces the original plaintext.
- `TestEnvelope_Tamper_DecryptFails` — any bit flip in ciphertext /
  nonce / wrapped-DEK fails with a non-nil error and no plaintext
  byte output.
- `TestEnvelope_WrongKEK_DecryptFails` — a DEK wrapped under KEK v1
  cannot be unwrapped with KEK v2.
- `TestRotation_RewrapsAllBlobs` — synthetic workload of 100 jobs,
  rotate KEK, every blob is now stamped with the new version; each
  still decrypts correctly.
- Integration: submit job with `secret_keys: [HF_TOKEN]`, inspect
  the Badger record (out-of-band), assert the ciphertext does not
  contain the plaintext bytes.
- Integration: feature 26 reveal after encryption — the revealed
  value matches the original submitted plaintext.

## Deferred

- **Per-user KEKs.** A multi-tenant coordinator could have one KEK
  per tenant, so a compromised KEK limits blast radius. Requires
  an identity/tenant model; out of scope for this slice.

## Implementation status

_Not started. Promoted from feature 26's "Not attempting" section
on 2026-04-19 so the gap has a planning target._
