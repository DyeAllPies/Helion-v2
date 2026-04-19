# Feature: Dangerous-env denylist on job submission

**Priority:** P1
**Status:** Pending
**Affected files:**
`internal/api/handlers_jobs.go` (validator),
`internal/api/handlers_jobs_test.go`,
`docs/SECURITY.md` (new threat row).

## Problem

A submitter — via the CLI today, the dashboard submission tab
when feature 22 lands, or anyone with a valid JWT — can include
an env var named `LD_PRELOAD=/tmp/evil.so` on any job. The Go
runtime ([`internal/runtime/go_runtime.go`](../../internal/runtime/))
passes the request env map directly into
`exec.Command{...}.Env` without filtering. On a non-alpine node
image (most of the Python-capable images in
`Dockerfile.node-python` + `Dockerfile.node-python-rust`), the
dynamic loader honours `LD_PRELOAD` and loads the attacker's
shared object into every job subprocess — executing arbitrary
code inside the node's sandbox.

The **Rust** runtime dodges this by accident: its executor calls
`env_clear()` before spawn (see
[`runtime-rust/src/executor.rs:97`](../../runtime-rust/src/executor.rs#L97)).
So the hole is Go-runtime specific, but since all existing
production nodes use the Go runtime it's the common case.

Related dangerous-loader env vars that do equivalent things on
various Unix flavours:

| Variable(s) | Effect |
|---|---|
| `LD_PRELOAD` | Linux glibc — load user SO into every exec |
| `LD_LIBRARY_PATH` | Linux — alter search order for libs |
| `LD_AUDIT` | Linux glibc — run auditor SO on every symbol lookup |
| `DYLD_INSERT_LIBRARIES` | macOS — equivalent to LD_PRELOAD |
| `DYLD_LIBRARY_PATH` | macOS — equivalent to LD_LIBRARY_PATH |
| `DYLD_FRAMEWORK_PATH` | macOS — loader search path for frameworks |
| `GCONV_PATH` | glibc iconv — load attacker modules at charset conv |
| `GIO_EXTRA_MODULES` | glib — load attacker modules at `g_type_init` |

No Helion job has a legitimate reason to set any of these. The
denylist is the right shape — a small allow-by-default list with
a few explicitly forbidden prefixes / exact names.

## Current state

- [`handleSubmitJob`](../../internal/api/handlers_jobs.go#L449)
  validates env at ~line 70-80: `len(env) ≤ 128`,
  `key ≠ ""`, `key ≤ maxEnvKeyLen`, `value ≤ maxEnvValLen`.
  No content filtering on keys.
- The same validator is shared by
  [`handleSubmitWorkflow`](../../internal/api/handlers_workflows.go#L94)
  through its per-job validation pass, so fixing it once covers
  both endpoints.
- The denylist is easy to centralise next to the other
  validators in the same file.

## Design

### Reject by exact key or prefix match

```go
// handlers_jobs.go

// envKeyBlocked reports whether an env var key is one a Helion
// job has no legitimate reason to set. The dynamic loader and
// glibc/glib module-loading env vars below would let a job with
// write access to /tmp hijack every subprocess exec on the node.
//
// Blocked on every submit path (single job, workflow job,
// dry-run). Denylist rather than allowlist because application
// jobs may legitimately set arbitrary app-specific vars —
// PYTHONPATH, HELION_TOKEN, HF_HOME, CUDA_VISIBLE_DEVICES, etc.
// The list is short and covers the well-known loader injection
// vectors.
var envKeyBlockedPrefixes = []string{"LD_", "DYLD_"}
var envKeyBlockedExact    = map[string]struct{}{
    "GCONV_PATH":        {},
    "GIO_EXTRA_MODULES": {},
    "HOSTALIASES":       {},          // glibc — alt hosts file, redirects name resolution
    "NLSPATH":           {},          // glibc — catalogue path, tangential attack surface
    "RES_OPTIONS":       {},          // glibc resolver override
}

func envKeyBlocked(key string) (bool, string) {
    for _, p := range envKeyBlockedPrefixes {
        if strings.HasPrefix(key, p) {
            return true, "dynamic-loader injection vector (" + p + "* prefix)"
        }
    }
    if _, ok := envKeyBlockedExact[key]; ok {
        return true, "known module-loading or resolver env var"
    }
    return false, ""
}
```

Error body carries the rejection reason so operators can
understand why their submit failed without running grep on the
source.

### Case sensitivity

Unix env vars are case-sensitive. The denylist matches
verbatim — a clever submitter typing `ld_preload` lowercase
wouldn't be blocked. That's the correct behaviour: the Linux
loader also only honours uppercase, so a lowercase var is inert.
The denylist protects against what the loader actually reads.

## Security plan

New `SECURITY.md` threat row:

| Threat | Control |
|---|---|
| Attacker with submit permission hijacks subprocess execution on the node via a dynamic-loader env var (`LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.) | Centralised denylist in `handlers_jobs.go` rejects submits whose env keys match the blocked prefixes / exact names, with a clear error message. Applied on every submit path (single job, workflow job, dry-run). Rust runtime's `env_clear()` provides belt-and-braces defence; the denylist is the load-bearing mitigation on the Go runtime. |

### Why denylist, not allowlist

Helion jobs set arbitrary application-specific env routinely —
`PYTHONPATH`, `HELION_TOKEN`, `HELION_API_URL`, `CUDA_VISIBLE_DEVICES`,
`HF_HOME`, `SKLEARN_DATA_HOME`, user vars in ML workflows. An
allowlist would be infeasibly long and would break every new
user on day one. The denylist is short (two prefixes + five
exact names) and covers every loader-injection vector I found
in the glibc + macOS documentation. If a new dangerous prefix
appears (e.g. `SOMETHING_AUDIT_LIBS`), adding it is a one-line
change.

### Admin role blocked too

Even admin role cannot bypass the denylist. There's no
legitimate admin workflow that needs `LD_PRELOAD`; if an
operator wants to run `strace` or `perf` they go to the node
directly, not through the job submit path.

## Implementation order

1. `envKeyBlocked(key)` helper + unit test table (one case per
   prefix + exact name).
2. Wire into the existing env-validation loop in
   `handlers_jobs.go`.
3. Integration test: `POST /jobs` with each blocked key → 400.
4. Integration test: `POST /workflows` with a blocked key in
   any child job → 400.
5. SECURITY.md update.

## Tests

- `TestEnvKeyBlocked_TablePrefix` — `LD_PRELOAD`, `LD_AUDIT`,
  `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES`,
  `DYLD_FRAMEWORK_PATH` all return blocked=true.
- `TestEnvKeyBlocked_TableExact` — `GCONV_PATH`,
  `GIO_EXTRA_MODULES`, `HOSTALIASES`, `NLSPATH`, `RES_OPTIONS`
  all return blocked=true.
- `TestEnvKeyBlocked_Allowed` — `PYTHONPATH`, `HELION_TOKEN`,
  `path` (lowercase), `HELIO_LD_FOO` (ld in the middle, not
  prefix) return blocked=false.
- `TestHandleSubmitJob_LDPRELOAD_Rejected` — POST a job with
  `env: {LD_PRELOAD: /tmp/evil.so}` → 400, error body contains
  "dynamic-loader injection vector".
- `TestHandleSubmitWorkflow_LDPRELOAD_In_Child_Rejected` — POST
  a workflow where job 2 of 3 has `LD_PRELOAD` in env → 400,
  error body names `job 2`. No workflow persisted.

## Acceptance criteria

1. `curl -XPOST .../jobs -d '{"command":"echo","args":["x"],
   "env":{"LD_PRELOAD":"/tmp/evil.so"}}'` returns 400.
2. Error body includes `dynamic-loader injection vector` so the
   operator can diagnose without reading the source.
3. Same payload without `LD_PRELOAD` → 200.
4. The existing MNIST + iris workflows still submit cleanly —
   none of their env keys matches the denylist.
5. Audit log carries a `job.submit_reject` event with the
   blocked-key reason in its detail field (new event type or
   a reused existing reject type — TBD during implementation).

## Deferred

- **Block-list symlinks pointing at dangerous libraries.** The
  denylist stops key names; a sufficiently motivated attacker
  could stage an exec that sets `LD_PRELOAD` inside the
  subprocess once it starts. That's out of scope for submit-
  time validation and in scope for seccomp / user-namespace
  work (feature 15 and follow-ups).
- **Per-node policy overrides.** An operator might want
  `LD_LIBRARY_PATH` set for a specific job on a specific node.
  Not supported; if you need it, run outside Helion.
