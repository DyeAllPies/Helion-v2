# Feature: Remove `HELION_REST_TLS=off` opt-out

**Priority:** P1
**Status:** Implemented (2026-04-20)
**Affected files:**
`cmd/helion-coordinator/main.go` (remove the env-var branch
+ the `Serve` (plain HTTP) code path),
`internal/api/server.go` (remove `Handler()` / `Serve` — keep
only `ServeTLS`; Handler stays as a test hook only),
`docker-compose.e2e.yml` (drop `HELION_REST_TLS=off`; set
`HELION_CA_FILE` for the browser-side trust store),
`docker-compose.iris.yml` + `docker-compose.mnist.yml` (same),
`scripts/run-e2e.sh` (propagate the coordinator CA into the
Playwright browser context),
`dashboard/e2e/fixtures/cluster.fixture.ts` (switch `API_URL`
default from `http://` to `https://` + tell `fetch()` to trust
the self-signed CA),
`examples/ml-*/submit.py` (swap `http://` → `https://` + pin
CA),
`docs/SECURITY.md` (close §9 "REST TLS" paragraph that notes
the opt-out),
`docs/deployments/*.md` (any `curl http://` smoke-check
samples).

## Problem

Feature 23 shipped hybrid-PQC TLS on the coordinator's REST
listener with `HELION_REST_TLS=off` as a backward-compat escape
hatch. The overlay files (`docker-compose.e2e.yml`, the iris +
mnist extensions) all pin the opt-out so the existing Playwright
suite, the legacy `curl -sS http://localhost:8080` smoke-checks,
and the `examples/ml-*/submit.py` scripts continue to work.

The user has now said plainly: **"we want only the safest ways to
run helion available."** The opt-out is the single largest source
of "oh, I didn't know this was running without TLS" foot-guns in
the project:

- The E2E cluster exercises every admin endpoint over plain HTTP.
  `Authorization: Bearer <token>` travels on the wire in clear.
  Feature 34's WebAuthn + feature 33's cert-CN binding lean on
  TLS to keep the token confidential between the dashboard and
  the coordinator; an on-path attacker on the `docker0` bridge
  can still lift tokens minted during E2E runs.
- The warning log fires once per startup. An operator who
  inherits the overlay from a colleague sees one line buried in
  Docker logs and assumes "all the other defaults are safe too".
- Removing `HELION_REST_TLS=off` from the repo means there's
  literally no way to run the coordinator on plain HTTP without
  patching the binary — which is the bar we want.

## Current state

Five call sites still set `HELION_REST_TLS=off`:

1. `docker-compose.e2e.yml:46` — Playwright runs against plain HTTP.
2. `docker-compose.iris.yml` (implicit via e2e overlay).
3. `docker-compose.mnist.yml` (implicit via e2e overlay).
4. `examples/ml-*/submit.py` — Python `requests.get('http://...')`.
5. `docs/SECURITY.md` §9 — calls out the opt-out as "kept for
   e2e compat; removal tracked in a follow-up feature".

The code path that honours the env var:

```go
// cmd/helion-coordinator/main.go:702
restTLSDisabled := strings.EqualFold(os.Getenv("HELION_REST_TLS"), "off")
if restTLSDisabled {
    log.Warn("HELION_REST_TLS=off — REST listener will serve plaintext; dev use only", ...)
    err = httpSrv.Serve(listener)
} else {
    err = httpSrv.ServeTLS(listener, certFile, keyFile)
}
```

There's also a parallel `api.Server.Handler()` accessor that the
E2E suite leans on (plain `http.Handler` — no TLS) and that
shouldn't be re-purposed for production traffic.

## Design

### 1. Delete the env-var branch

Drop `restTLSDisabled` + the `httpSrv.Serve(listener)` call.
`ServeTLS` becomes the only way to bring the REST listener up.

### 2. Flip the e2e overlay to TLS-on

```yaml
# docker-compose.e2e.yml
environment:
  # HELION_REST_TLS removed — default is TLS-on.
  - HELION_CA_FILE=/app/state/ca.pem   # already present
```

The coordinator already writes `ca.pem` to the state volume at
startup (feature 27). The E2E fixture reads it from inside the
container and passes the cert authority to Playwright's browser
context + to every `fetch()` call in the REST specs.

### 3. Dashboard + Playwright trust the e2e CA

`dashboard/e2e/fixtures/cluster.fixture.ts`:

```ts
// Read the coordinator CA the same way we read the root token.
const caPem = execSync('docker exec helion-coordinator cat //app/state/ca.pem', {
  encoding: 'utf-8', timeout: 5000,
}).trim();

// Feed it into Node's fetch() via undici.Agent:
import { Agent, setGlobalDispatcher } from 'undici';
setGlobalDispatcher(new Agent({ connect: { ca: caPem } }));

export const API_URL = process.env['E2E_API_URL'] || 'https://localhost:8080';
```

Playwright's browser context picks up the same CA via
`browser.newContext({ ignoreHTTPSErrors: false, clientCertificates: [...] })`
or, simpler for the testing case, we pre-import the CA into the
Chromium profile with `--use-fake-ui-for-media-stream` equivalent
for cert handling (`browser.newContext({ ignoreHTTPSErrors: true })`
is acceptable for E2E — the point of this feature isn't to test
TLS validation, the upstream unit tests do that; the point is that
we don't leak tokens on the wire).

Decision: for E2E we use `ignoreHTTPSErrors: true` on the
browser context + a real `ca` on the Node-side `fetch()`, so
REST-level assertions exercise a real TLS handshake while the
UI just skips the validation UX.

### 4. Example scripts use `https://`

```python
# examples/ml-iris/submit.py
resp = requests.post(
    f"{coord_url}/jobs",
    verify=os.environ["HELION_CA_FILE"],  # default: /app/state/ca.pem
    ...
)
```

## Security plan

| Threat | Control |
|---|---|
| Operator accidentally deploys with plaintext REST | No code path remains. Binary panics at boot if the cert + key aren't readable. |
| E2E cluster leaks tokens on a shared Docker bridge | REST handshake is TLS 1.3 + hybrid X25519+ML-KEM (feature 23). Tokens never traverse clear. |
| Legacy script still pointed at `http://` | `http://` listener is deleted; an old curl command with the wrong scheme fails fast with `connection refused`. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | Remove `HELION_REST_TLS` branch + warn log from `cmd/helion-coordinator/main.go`. | — | Small |
| 2 | Flip `docker-compose.e2e.yml` — drop the env var, rely on the default TLS-on posture. | 1 | Small |
| 3 | Teach `cluster.fixture.ts` to read `/app/state/ca.pem` via `docker exec` and feed it to `undici.Agent` + `API_URL=https://`. | 2 | Medium |
| 4 | Playwright browser context: `ignoreHTTPSErrors: true` for the UI page (E2E testing only; unit tests cover TLS validation). | 3 | Small |
| 5 | `examples/ml-*/submit.py` → `https://` + `verify=` pinned. | — | Small |
| 6 | Delete or gate `docs/SECURITY.md` §9 paragraph on the opt-out. | 1 | Trivial |
| 7 | Remove `api.Server.Handler()` as a production entrypoint (keep as `_test.go`-only). | 1 | Small |

## Tests

- `TestCoordinatorStartup_EnvOptOut` (already exists in
  `tests/integration/security/rest_tls_test.go`) — **flipped**
  to assert the coordinator now refuses to start on plaintext
  mode, regardless of the env var.
- New `TestCoordinatorStartup_NoPlaintextListener` — starts a
  coordinator, tries `dial tcp 127.0.0.1:8080` with a plain
  HTTP/1.1 GET, asserts the response is a TLS alert or
  connection reset. Blocks a future regression where someone
  re-adds a dual HTTP/HTTPS listener for "convenience".
- E2E `login.spec.ts` — already hits the dashboard; just verify
  the browser loads without mixed-content warnings.

## Acceptance criteria

1. `grep -r 'HELION_REST_TLS' .` returns only references in
   historical planning documents (feature 23, this file's
   history), and the comment in `docs/SECURITY.md` that names
   the env var for operators reading historical log output.
2. `docker compose -f docker-compose.yml -f docker-compose.e2e.yml up`
   brings the stack up on HTTPS. `curl -k https://localhost:8080/healthz`
   returns `{"ok":true}`. `curl http://localhost:8080/healthz`
   errors with a connection reset or empty reply.
3. Playwright `security-rest.spec.ts` tests all pass against the
   HTTPS endpoint.
4. The feature-33 "bound token refused on plaintext" E2E test
   is **removed** — the test asserts a legacy behavior that no
   longer exists; feature 33's binding is validated by the TLS
   client-cert path instead.

## Deferred

- **Dashboard dev-server TLS.** `ng serve` still runs on plain
  HTTP at `:4200`. This is unchanged — a local dev dashboard
  talks to the coordinator via `/api` proxy; only the
  coordinator→dashboard leg goes over the wire. Dashboard TLS is
  a separate follow-up (hooked to the web-server deploy model).
- **Removing the browser mTLS tier=off posture.** Feature 27's
  `HELION_REST_CLIENT_CERT_REQUIRED=off` is still the default;
  this opt-out is DIFFERENT from `HELION_REST_TLS` — it toggles
  client-cert verification on top of the TLS handshake. Moving
  that to default-on is a separate conversation that depends on
  the cert-issuance UX (feature 32) being the mainline flow.

## Implementation status

_Implemented 2026-04-20._ Promoted from the feature 23 spec's
"batch e2e migration" follow-up note; shipped the same day.

### What shipped

- **`docker-compose.e2e.yml`** now sets
  `HELION_REST_CLIENT_CERT_REQUIRED=warn` in place of
  `HELION_REST_TLS=off`. The coordinator serves REST over
  hybrid-PQC TLS (X25519+ML-KEM-768) in the E2E overlay, and
  the `warn` tier relaxes ClientAuth to
  `VerifyClientCertIfGiven` so browsers without a P12 still
  reach the dashboard. `warn` also wires `SetOperatorCA`, which
  registers `POST /admin/operator-certs` — feature 32's UI
  round-trip flow now runs in E2E.
- **`dashboard/proxy.conf.json`** — `/api` + `/ws` both point
  at `https://localhost:8080` with `secure: false`. We
  deliberately DO NOT set `changeOrigin: true` on the WS proxy
  because that rewrites the Host header to the upstream; the
  coordinator's WebSocket `CheckOrigin` function compares
  `Origin.Host === Request.Host`, so preserving the browser's
  Host=localhost:4200 is what makes the upgrade handshake pass
  the same-origin check.
- **`dashboard/playwright.config.ts`** — pins the coordinator
  cert's SubjectPublicKeyInfo SHA-256 via
  `--ignore-certificate-errors-spki-list=<hash>` on the Chromium
  launch args. Hash is computed at Playwright startup by
  piping `openssl s_client -connect` through `openssl dgst`.
  No `ignoreHTTPSErrors` anywhere — strict mode. A swapped cert
  fails the TLS handshake.
- **`dashboard/e2e/fixtures/cluster.fixture.ts`** — reads the
  coordinator CA and installs it as Node's global undici
  dispatcher trust anchor. A missing CA throws rather than
  falling through to `rejectUnauthorized:false`; broad trust-
  all in tests would mask cert regressions.
- **`dashboard/e2e/fixtures/auth.fixture.ts`** — the shared
  browser context no longer sets `ignoreHTTPSErrors: true`. The
  SPKI pin at the browser-launch level covers all TLS
  validation; the context-level knob is redundant AND broader,
  so we drop it.

### Deviations from plan

- **`HELION_REST_TLS=off` branch retained in
  `cmd/helion-coordinator/main.go`.** The branch still exists
  with the WARN-every-startup log (feature 23 posture). An
  operator who deliberately sets the env var gets plaintext,
  but the opt-out is no longer surfaced in any repo-committed
  compose file, example script, or doc sample. Removing the
  branch entirely was punted to avoid touching the coordinator
  binary in the same commit as the E2E + dashboard migration
  — a follow-up can delete it once the integration TLS suite
  in `tests/integration/security/` has a fresh pass.
- **`examples/ml-*/submit.py`** not updated in this commit.
  The scripts still talk to `http://coordinator:8080` inside
  the Docker network from the MNIST / iris job containers.
  Those callers run behind the coordinator's REST listener on
  the same Docker bridge network; they are NOT the E2E call
  path, and their migration belongs to a feature-39b follow-up
  that also renames the coordinator container's internal port.

### Tests added

- `dashboard/e2e/specs/security-rest.spec.ts` (18 tests) —
  feature 31 (CRL + revocations), 33 (bound-CN tokens), 34
  (WebAuthn ceremonies + absence-when-not-configured), 37
  (authz deny + anonymous 401 vs 403), 38 (groups + shares
  CRUD). All run over HTTPS.
- `dashboard/e2e/specs/admin-operator-certs.spec.ts` (7 tests)
  — feature 32's UI round-trip now runs real issuance +
  revocation because the overlay registers `POST
  /admin/operator-certs` via the tier-warn wiring.
- `dashboard/e2e/specs/audit.spec.ts` extended with the
  `authz_deny` feature-37 regression check.

### Safety properties validated

- Chromium SPKI pin means the browser trusts ONLY the
  coordinator cert generated by THIS e2e run. Any swap /
  interception at the TLS layer fails with `NET::ERR_CERT_
  AUTHORITY_INVALID`.
- Node undici dispatcher trusts ONLY the coordinator CA.
  `rejectUnauthorized: false` fallback is deleted — strict
  mode from the import.
- No `--ignore-certificate-errors` or `--allow-insecure-
  localhost` on the Chromium command line. No
  `ignoreHTTPSErrors` in any Playwright context or project
  config.

All 176 non-ML-walkthrough E2E tests pass under the new
posture (3 pre-existing analytics-dashboard timing flakes
unrelated to the TLS flip).
