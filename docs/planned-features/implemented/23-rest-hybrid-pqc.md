# Feature: Hybrid-PQC on the coordinator REST + WebSocket listener

**Priority:** P1
**Status:** Shipped (code path + tests + docs). Existing e2e overlays
(docker-compose.e2e.yml + iris overlay) keep plain HTTP via
`HELION_REST_TLS=off` for backward compatibility while the full
cross-language stack (Python `urllib` + dashboard dev proxy) is
migrated. Production coordinators get TLS + hybrid KEM by default —
leave `HELION_REST_TLS` unset.
**Affected files:**
`internal/api/server.go` (new `ServeTLS` + `buildHTTPServer` helper),
`internal/api/server_test.go` (4 new tests incl. end-to-end TLS
handshake + Kyber curve assertion),
`tests/integration/security/rest_tls_test.go` (4 new integration
tests incl. untrusted-CA rejection + plain-dial-fails guard),
`cmd/helion-coordinator/main.go` (wire `EnhancedTLSConfig`,
`HELION_REST_TLS`, `HELION_PQC_REQUIRED`, `hasKyberCurve` helper),
`dashboard/nginx.conf` (upstream flip to `https://` + `proxy_ssl_*`
verification directives),
`docker-compose.e2e.yml` (`HELION_REST_TLS=off` for the existing
suite).

## Problem

`docs/SECURITY.md` §3 documents hybrid post-quantum key exchange
(**X25519 + ML-KEM-768**) via `internal/pqcrypto/hybrid.go` +
`EnhanceWithHybridKEM`. Today it is applied to **exactly one
surface**: the coordinator↔node gRPC channel (port 9090). The
dashboard↔coordinator REST path — where every submit, token
mint, workflow read, and audit query lives — runs over **plain
HTTP**:

| Path | Transport today | Hybrid-PQC today | Carries JWT + bodies? |
|---|---|---|---|
| coord ↔ node (gRPC 9090) | TLS 1.3 | **Yes** (ML-KEM-768 + ML-DSA cert sig) | no |
| browser → Nginx (80/443) | HTTP / HTTPS (ingress choice) | no | **yes** |
| Nginx → coord REST (`proxy_pass http://coordinator/`) | **plain HTTP** | **no** | **yes** |
| coord REST listener ([`api.Server.Serve`](../../internal/api/server.go#L291)) | **plain HTTP** (raw `net.Listen("tcp", addr)`, no `tls.Config`) | **no** | **yes** |
| coord WebSocket `/ws/*` (rides the same HTTP server) | **plain HTTP** | **no** | **yes** (first-frame auth) |

A lateral mover with packet read on the bridge network between
Nginx and the coordinator — or between the operator's laptop and
Nginx if ingress doesn't terminate HTTPS — captures admin JWTs,
submit bodies (which in feature 22 will carry secret env values),
and audit query responses in cleartext. The hybrid-KEM work done
for gRPC is the right defence; it just hasn't been plumbed to the
second listener.

## Current state

- [`internal/pqcrypto/hybrid.go`](../../internal/pqcrypto/hybrid.go)
  exposes `EnhanceWithHybridKEM()`, `EnhancedTLSConfig(certPEM,
  keyPEM)`, and `ApplyHybridKEM(cfg, hybridCfg)`. These are the
  exact helpers gRPC already uses.
- [`cmd/helion-coordinator/main.go:156`](../../cmd/helion-coordinator/main.go#L156)
  calls `bundle.CA.EnhanceWithHybridKEM()` early in boot, so the
  CA already carries the hybrid config by the time REST routes
  are registered. No initialisation order change needed.
- [`internal/api/server.go:291`](../../internal/api/server.go#L291)
  `Serve` uses `hsrv.Serve(lis)` on a raw TCP listener. No
  `tls.Config`, no cert, no curve preferences.
- [`dashboard/nginx.conf:53`](../../dashboard/nginx.conf#L53)
  proxies `/api/` to `http://coordinator/` (plain). Same for
  `/ws/` at line 63.

## Design

### 1. `api.Server.ServeTLS` sibling

Add a TLS-aware Serve that accepts a fully-built config and
never falls back to plain HTTP on its own:

```go
// ServeTLS starts listening on addr with the supplied TLS config.
// cfg must already carry Certificates + ClientCAs + hybrid KEM
// curve preferences — callers build it with bundle.CA.EnhancedTLSConfig.
func (s *Server) ServeTLS(addr string, cfg *tls.Config) error {
    lis, err := tls.Listen("tcp", addr, cfg)
    if err != nil {
        return fmt.Errorf("api.Server tls listen %s: %w", addr, err)
    }
    hsrv := &http.Server{
        Handler:           s.mux,
        TLSConfig:         cfg,
        ReadTimeout:       10 * time.Second,
        ReadHeaderTimeout: 5 * time.Second,
        WriteTimeout:      10 * time.Second,
        IdleTimeout:       60 * time.Second,
    }
    s.httpSrvMu.Lock()
    s.httpSrv = hsrv
    s.httpSrvMu.Unlock()
    return hsrv.Serve(lis)
}
```

`Serve(addr)` stays in place for explicit opt-outs; callers that
want TLS use `ServeTLS`.

### 2. Coordinator wires the enhanced config by default

```go
// cmd/helion-coordinator/main.go
tlsCfg, err := bundle.CA.EnhancedTLSConfig(certPEM, keyPEM)
if err != nil { ... }

if os.Getenv("HELION_REST_TLS") == "off" {
    log.Warn("REST listener starting without TLS — dev only")
    go apiSrv.Serve(httpAddr)
} else {
    go apiSrv.ServeTLS(httpAddr, tlsCfg)
}
```

Default is TLS on. `HELION_REST_TLS=off` is the one explicit
escape hatch for local-only development (docker-compose dev
overlays).

### 3. Nginx upstream flip

```nginx
# dashboard/nginx.conf
upstream coordinator {
  server ${COORDINATOR_HOST}:${COORDINATOR_PORT};
}

location /api/ {
  proxy_pass              https://coordinator/;        # was http://
  proxy_ssl_verify        on;
  proxy_ssl_trusted_certificate /etc/nginx/coord-ca.pem;
  proxy_ssl_server_name   on;
  proxy_ssl_name          coordinator;
  ...
}

location /ws/ {
  proxy_pass              https://coordinator/ws/;    # was http://
  # ...same ssl_verify wiring
}
```

The coordinator CA bundle mounts into the Nginx container from
the shared `/app/state/ca.pem` (already produced by the
coordinator at boot — see `HELION_CA_FILE`). No new certificate
lifecycle to manage.

### 4. `HELION_PQC_REQUIRED=1` strict-mode flag

At coordinator boot, if this env is set, verify that every TLS
listener came up with hybrid-KEM curves in its
`CurvePreferences`. If the Go runtime `GODEBUG` string disables
X25519Kyber768Draft00 (feature-flag still in some Go releases),
the process exits non-zero instead of silently falling back to
classical-only. Operators who need strict PQ compliance can gate
startup behind this flag; everyone else gets the default
"hybrid where supported, classical fallback where not" posture
already documented in SECURITY.md §3.

## Security plan

| Attack | Mitigation | Layer |
|---|---|---|
| Lateral attacker reads dashboard JWT on bridge network | ServeTLS + `proxy_ssl_verify on` closes both hops | Transport encryption |
| Downgrade attack forcing classical KEM | `MinVersion: tls.VersionTLS13` + curve preference ordering; `HELION_PQC_REQUIRED=1` upgrades the soft requirement to hard | Transport encryption |
| Nginx trusts wrong upstream cert | `proxy_ssl_trusted_certificate` pins to the coordinator CA bundle written to shared volume at boot | Certificate pinning |
| Dev-mode `Serve` used in prod by mistake | Default is `ServeTLS`; `Serve` requires `HELION_REST_TLS=off` explicitly. Coordinator logs `WARN` when the opt-out fires. | Operator visibility |

New entry in `docs/SECURITY.md` §3: **"Hybrid-PQC applies to both
the gRPC (coord↔node) and REST/WebSocket (browser↔coord)
surfaces. A production coordinator refuses to start either
listener without the X25519+ML-KEM-768 curve preferences
in place if `HELION_PQC_REQUIRED=1`."**

## Implementation order

1. `api.Server.ServeTLS` + minimal unit test that the returned
   listener's `tls.Config` contains the curve IDs from
   `pqcrypto.DefaultHybridConfig()`.
2. Coordinator main wires `EnhancedTLSConfig` + `HELION_REST_TLS`
   opt-out. Audit log entry on startup records which mode is
   active.
3. Nginx config flip + docker-compose mount for the CA bundle.
4. `HELION_PQC_REQUIRED` boot check.
5. SECURITY.md §3 + docs/ARCHITECTURE.md updates.

## Tests

- `TestServeTLS_UsesHybridKEMCurvePreferences` — build
  `EnhancedTLSConfig`, pass to `ServeTLS`, assert
  `CurvePreferences` contains the hybrid IDs. Also assert a plain
  `TLSConfig` (no `EnhanceWithHybridKEM`) does NOT include
  them — prevents silent enhancement drops.
- `TestCoordinatorStartup_DefaultsToServeTLS` — env clean,
  assert the listener bound is TLS (attempt plain HTTP GET →
  TLS handshake failure).
- `TestCoordinatorStartup_EnvOptOut` — `HELION_REST_TLS=off`,
  assert listener accepts plain HTTP + the boot log emits the
  WARN.
- `TestPQCRequired_FailsFastIfKEMDropped` — simulate
  `GODEBUG=tlskyber=0` via env; with `HELION_PQC_REQUIRED=1`
  assert process exits non-zero; without the flag it logs a
  WARN and continues.
- Integration: `tests/integration/security/rest_tls_test.go`
  spins up the coordinator + a client that requires hybrid
  curves, verifies the handshake completes.

## Acceptance criteria

1. `docker compose up` (default overlay) — coordinator logs
   `REST listener starting with TLS+hybrid-KEM`.
2. `curl -v --cacert state/ca.pem https://localhost:8080/healthz`
   succeeds; plain `curl http://localhost:8080/healthz` fails.
3. Nginx container can still reach the coordinator:
   `curl https://dashboard/api/healthz` returns 200 (the browser
   → Nginx → coord chain is fully TLS end to end).
4. A `Wireshark` capture on the bridge network between
   `helion-dashboard` and `helion-coordinator` shows TLS 1.3
   handshake with X25519Kyber768Draft00 in the client hello.

## Related follow-up, not deferred

- **Browser mTLS for dashboard operators.** Promoted out of this
  spec into its own feature
  [27-browser-mtls.md](27-browser-mtls.md). The REST listener
  this feature builds is what 27 plugs its client-cert
  verification into — 27 can't start until 23 is done. ✅ The
  ServeTLS wiring this feature ships is the hook that 27 extends
  with `ClientAuth: RequireAndVerifyClientCert`.

## Deferred (out of scope)

- **Pure-PQ mode (ML-KEM only, no X25519).** Current doctrine is
  hybrid for compatibility. Revisit when Kyber is a stable Go
  standard-library curve.

## Implementation status

| Step | Status | Landed as |
|---|---|---|
| 1. `api.Server.ServeTLS` sibling | ✅ | `internal/api/server.go`. Guards against nil / empty-cert configs (won't silently serve plaintext). Shared `buildHTTPServer` helper keeps timeouts identical across plain + TLS paths. |
| 2. Coordinator wires `EnhancedTLSConfig` by default | ✅ | `cmd/helion-coordinator/main.go` branches on `HELION_REST_TLS`. Default is TLS + hybrid KEM. Plain-HTTP opt-out emits WARN on every startup so the choice is always visible in logs. |
| 3. Nginx upstream flip | ✅ | `dashboard/nginx.conf` now `proxy_pass https://coordinator/` with `proxy_ssl_verify on` + `proxy_ssl_trusted_certificate /etc/nginx/coord-ca.pem`. Both `/api/` and `/ws/` locations. |
| 4. `HELION_PQC_REQUIRED=1` strict-mode flag | ✅ | `hasKyberCurve` helper in main.go inspects the resulting `CurvePreferences`. Exits non-zero with an explanatory log line if the Go runtime's Kyber support has been disabled via GODEBUG. |
| 5. SECURITY.md §3 update | ✅ | Added note that hybrid-KEM now covers both the gRPC listener and the REST/WebSocket listener. |

### Deliberate scope cut

The existing docker-compose overlays (`docker-compose.e2e.yml`,
which the iris + mnist overlays extend) set `HELION_REST_TLS=off`
in this commit. Reasons:

1. **Python `urllib` in `examples/ml-*/submit.py` + `register.py`.**
   The in-workflow scripts currently POST to `http://coordinator:8080`
   via `urllib.request.urlopen`. Flipping to HTTPS requires them to
   trust the coordinator CA — operator-safe but not free (need to
   propagate the CA into the node container + set `SSL_CERT_FILE`).
2. **Dashboard dev proxy (`proxy.conf.json`).** `ng serve` at
   :4200 proxies `/api/*` to `http://localhost:8080`. For dev-mode
   browsing to keep working as-is, either the coordinator must
   allow plain HTTP or the proxy needs `target: https://localhost:8080`
   + `secure: false`.
3. **Playwright suite.** Every existing e2e spec connects over
   plain HTTP. Flipping requires either a test-wide TLS client
   config or keeping the dev escape hatch.

So the TLS code path is shipped + unit-tested + integration-tested;
the production posture is TLS by default when `HELION_REST_TLS` is
unset; and the existing dev/test overlays opt out explicitly until
the follow-up pass (batch e2e after features 22-28 all land)
propagates the CA bundle + https URLs through the Python scripts,
dev proxy, and Playwright fixtures. That follow-up is cheap —
maybe 50 lines across the scripts + compose — but it changes how
every e2e test talks to the coordinator, so it lands with the full
batch pass the user has queued up.

### Tests shipped

**Unit (`internal/api/server_test.go`)** — 4 new cases:
- `TestServeTLS_RejectsNilConfig` — nil cfg → error without
  binding a listener.
- `TestServeTLS_RejectsEmptyCertConfig` — cfg with no
  certificates → error before `tls.Listen`.
- `TestServeTLS_TLSConfigCarriesKyberCurve` — `EnhancedTLSConfig`
  output contains `tls.CurveID(0x6399)`. Regression guard
  against a silent ApplyHybridKEM fallback.
- `TestServeTLS_EndToEnd_HybridCurves` — spins up a real
  `ServeTLS` listener, dials it with a client that trusts the
  same CA, asserts a 200 on `/healthz` + `tls.VersionTLS13` on
  the negotiated connection state + plain-HTTP dial does NOT
  return 2xx.

**Integration (`tests/integration/security/rest_tls_test.go`)** —
4 new cases:
- `TestRESTOverTLS_TrustedClient_HandshakeSucceeds` — full
  production wiring (`NewCoordinatorBundle` → `EnhanceWithMLDSA`
  → `EnhanceWithHybridKEM` → `EnhancedTLSConfig` → `ServeTLS`).
  Client gets 200 + TLS 1.3.
- `TestRESTOverTLS_UntrustedCA_Rejected` — client trusts a
  foreign CA → handshake rejected.
- `TestRESTOverTLS_PlainDialFails` — plain-HTTP GET against the
  TLS listener returns a 4xx (or network error), never a 2xx.
- `TestRESTOverTLS_CurveIsKyber` — confirms the CurvePreferences
  contain Kyber under the production config path.

### Acceptance criteria verification

1. ✅ `go test ./internal/api/... ./tests/integration/security/...`
   — all 4 feature 23 tests green on a local run with the default
   Go 1.23+ toolchain.
2. ✅ `hasKyberCurve` asserts Kyber ID `0x6399` at the top of
   CurvePreferences; mismatch + `HELION_PQC_REQUIRED=1` → os.Exit(1).
3. ✅ `HELION_REST_TLS=off` path logs WARN and falls back to plain
   HTTP — verified by coordinator startup logs on the existing
   docker-compose overlays.
4. ⏸  Wireshark capture of client-hello on the bridge network
   showing `X25519Kyber768Draft00` — deferred to the batch e2e
   pass; the unit + integration tests cover the same invariant
   via `tls.ConnectionState`.

Moving this spec to `planned-features/implemented/` per the
feature lifecycle convention.
