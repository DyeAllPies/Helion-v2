# Feature: Hybrid-PQC on the coordinator REST + WebSocket listener

**Priority:** P1
**Status:** Pending
**Affected files:**
`internal/api/server.go` (new `ServeTLS`),
`cmd/helion-coordinator/main.go` (wire `EnhancedTLSConfig`),
`dashboard/nginx.conf` (upstream flip to `https://`),
`docs/SECURITY.md` §3 (add REST surface to the covered list).

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
  verification into — 27 can't start until 23 is done.

## Deferred (out of scope)

- **Pure-PQ mode (ML-KEM only, no X25519).** Current doctrine is
  hybrid for compatibility. Revisit when Kyber is a stable Go
  standard-library curve.
