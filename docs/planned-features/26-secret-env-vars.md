# Feature: Secret env-var support on jobs

**Priority:** P1
**Status:** Pending
**Affected files:**
`internal/proto/coordinatorpb/types.go` (new `Secret` flag on
the env-var representation),
`internal/api/handlers_jobs.go` (accept + redact),
`internal/api/handlers_workflows.go` (same path for workflow
jobs),
`internal/audit/logger.go` or equivalent (audit detail scrubbing),
`dashboard/src/app/core/services/api.service.ts` (type update),
`dashboard/src/app/features/jobs/job-detail.component.ts` (mask
display),
`docs/SECURITY.md` (new row).

## Problem

`HELION_TOKEN` is already passed through a job's env map today —
every MNIST job carries one, set by the submit.py submitter. Add
to that list any Hugging Face token (`HF_TOKEN`), cloud
credentials (`AWS_SECRET_ACCESS_KEY`), model-registry API keys,
etc. The current behaviour is unsafe at three levels:

1. **`GET /jobs/{id}`** returns the full env map in clear. Any
   dashboard user, any CI system with a read-only token, any
   attacker with audit-viewer permission can extract tokens by
   listing jobs.
2. **Audit event detail strings** stringify the whole request —
   including env values — so tokens land in the BadgerDB audit
   log in cleartext, where they stay forever.
3. **Operator-facing log lines** (`job dispatched … env=...`)
   and dashboard UI cards render env values verbatim. Anyone who
   walks past a terminal can read the token off the screen.

The feature 22 submission tab makes this worse: operators will
paste tokens directly into a form field, the UI will send them,
and then the UI will display the job they just created with the
token right there next to the commit button.

## Current state

- Request env comes in as `map[string]string`; server stores it
  unchanged in the job record.
- `GET /jobs/{id}` returns the whole job record (including env)
  straight from `JobStore`.
- Audit logger at
  [`internal/audit/logger.go`](../../internal/audit/) stringifies
  the event `data` map into the `detail` field — no scrubbing.
- Dashboard job-detail component renders
  `{{ job.env | json }}` — raw dump.

## Design

### New wire type: `SubmitEnvVar`

Replace the raw map with a typed slice on the submit path.

```go
// internal/proto/coordinatorpb/types.go
type SubmitEnvVar struct {
    Key    string `json:"key"`
    Value  string `json:"value"`
    Secret bool   `json:"secret,omitempty"`
}

// A job's internal representation keeps env as a map for
// runtime dispatch (os/exec.Command.Env is []string), but the
// map carries a parallel set of "secret" keys so GET-path
// redaction knows which values to suppress.
type Job struct {
    ...
    Env         map[string]string `json:"env,omitempty"`
    SecretKeys  []string          `json:"-"`  // internal — never serialised outside redacted-response paths
}
```

### Back-compat: accept both shapes

For one release, the submit path accepts either:
- `"env": { "PYTHONPATH": "/app" }` — legacy map, all keys treated
  non-secret (unchanged behaviour).
- `"env": [{"key": "PYTHONPATH", "value": "/app"},
           {"key": "HF_TOKEN", "value": "hf_...", "secret": true}]`
  — new typed form, per-key secret flag.

A discriminator function on decode picks the shape. The legacy
path logs a DEPRECATION warning once per process. Removed in
the following minor version.

### Redaction paths

Four places where a value must never leak:

1. **`GET /jobs/{id}`** — env values whose key is in
   `SecretKeys` render as `"[REDACTED]"` in the response body.
2. **Audit log detail string** — the audit logger's helper that
   stringifies event data (`eventToDetail` or similar) checks a
   `_secret_keys` list inside the event data and scrubs matching
   values.
3. **Server-side `slog` lines** — `slog.Info("job dispatched",
   slog.Any("env", env))` → filter env through
   `redactSecretEnv(env, secretKeys)` before logging.
4. **Dashboard** — the typed response carries `value:
   "[REDACTED]"` from the server; the dashboard displays it
   verbatim. As a second line of defence, the submission form's
   secret-flag toggle also switches the HTML input to
   `type="password"` so the value never appears in the DOM as a
   plain `value="..."` attribute.

The redaction happens **server-side**. The dashboard is never
trusted to "know" which values were secret — it only displays
what the server sends.

### Dry-run interaction

When feature 24 (dry-run preflight) lands, the dry-run response
is the would-be job object — which is subject to the same
redaction. A dry-run that sends `{key: X, value: Y, secret: true}`
gets a response carrying `{key: X, value: "[REDACTED]",
secret: true}` just like the real submit would.

## Security plan

| Threat | Control |
|---|---|
| Token leaks via `GET /jobs/{id}` to any user with read permission | Server-side redaction of values whose key is flagged secret. Redaction is the default for every GET path — no per-endpoint opt-in. |
| Token leaks via audit log / incident-response dump | Audit detail strings elide `value=`, keep `key=` and `<secret>` marker so reviewers can still see WHICH env var was set without reading its content. |
| Token leaks via server stderr | `slog.Any("env", ...)` filters before logging; audit `detail` field is the canonical "what happened" record and it's already scrubbed. |
| Token in browser DOM via the form | `type="password"` on the secret-flagged form input; the new field component never binds the value to a visible `<span>`. |
| Operator toggles "secret" off post-hoc to read their own token | Once a secret key is stored, the server has no way to un-redact — the scrubbing is applied at response time, but the original value is still in the stored job record (we need it to dispatch). A leaked admin token can read the underlying record directly via the storage layer. The only mitigation against that is short-lived tokens + restricted storage access, not this feature. Document explicitly. |

### Not attempting

- **Encrypting stored values.** The coordinator needs the
  plaintext value to forward to the node runtime. A proper
  secrets manager (Vault, KMS-per-job) is a much bigger slice.
  Worth doing eventually; out of scope here.
- **Detecting "leaked via stdout".** If a job prints its own
  `$HELION_TOKEN` to stdout, it ends up in `logstore`. We
  don't scrub job stdout — that would require content inspection
  on every log line, and fancy regexes get fooled. Operators
  must not `echo $HELION_TOKEN` from their workflow scripts;
  document.

## Implementation order

1. **Wire type** + dual-format decoder with deprecation log.
2. **Storage** — `SecretKeys` field on the internal `Job`
   struct. `JobStore.Put` + `JobStore.Get` preserve it; audit
   log carries the list via the event `data` map (internal field
   name `_secret_keys`, not rendered).
3. **Response redaction** for `GET /jobs/{id}` + list endpoints.
4. **Audit detail scrubbing** — unit test with a token in the
   env, assert the audit entry detail string says
   `HF_TOKEN=<secret>` not `HF_TOKEN=hf_abc...`.
5. **`slog` env filter** — one helper called from every call
   site that logs `env`.
6. **Dashboard TS types** + job-detail component mask display +
   (when feature 22 lands) the submission form's secret
   toggle.
7. **SECURITY.md + docs/ARCHITECTURE.md** updates.

## Tests

- `TestSubmitEnvVar_Decode_AcceptsLegacyMap` — `"env": {...}` →
  all non-secret, no rejection.
- `TestSubmitEnvVar_Decode_AcceptsTypedSlice` — `"env": [...]`
  with mixed `secret: true`/`false` → secret keys captured.
- `TestHandleSubmitJob_SecretEnv_RedactedOnGet` — submit with
  `{key: "HF_TOKEN", value: "hf_…", secret: true}` → 200.
  `GET /jobs/{id}` body shows `{"HF_TOKEN": "[REDACTED]"}` not
  the original value.
- `TestHandleSubmitJob_NonSecretEnv_NotRedacted` — regression
  guard: `PYTHONPATH: /app` (not secret) still visible on GET.
- `TestAuditEntry_SecretValue_ElidedFromDetail` — submit a
  secret env var, read back the last audit entry, detail string
  contains `HF_TOKEN=<secret>` not the plaintext.
- `TestSlogFilter_RedactsSecretEnv` — call the redactor directly
  on an env map + secret keys, assert the returned map has the
  matching values replaced with `[REDACTED]`.
- Dashboard: `submit-env-field.component.spec.ts` — secret
  toggle sets `type="password"` on the input AND does not
  render the value in any `{{ }}` binding.

## Acceptance criteria

1. `POST /jobs -d '{"command":"echo","args":["x"],
   "env":[{"key":"HF_TOKEN","value":"hf_sekret","secret":true}]}'`
   returns 200.
2. `GET /jobs/<that-id>` → response `.env.HF_TOKEN == "[REDACTED]"`.
3. `helion-coordinator` stderr does NOT contain `hf_sekret`
   anywhere in the run.
4. Audit entry detail for that submit says `HF_TOKEN=<secret>`
   not `HF_TOKEN=hf_sekret`.
5. Legacy map form still works: `"env":{"PYTHONPATH":"/app"}` on
   an existing script → 200; script runs; no deprecation failure
   (warning log only).

## Deferred

- **Operator-facing "show me the secret" action.** Deliberately
  not built. If an operator forgets their token, they regenerate
  it; they don't read it out of the coordinator. Matches Vault /
  AWS Secrets Manager ergonomics.
- **Key-level ACL on secrets** (who can SET a given key, who
  can DISPATCH a job that reads one). Requires a user-identity
  model the coordinator doesn't have. Noted under
  `deferred/`.
