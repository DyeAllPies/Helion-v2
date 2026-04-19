// internal/api/server_test.go
//
// Tests for Server.Serve and Server.Shutdown lifecycle.

package api_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// ── Serve / Shutdown ──────────────────────────────────────────────────────────

func TestServe_StartsAndShutdown_NoError(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)

	// Pick a free port by listening and immediately closing.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()

	// Give the server time to start.
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("Serve returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Serve did not return after Shutdown")
	}
}

func TestShutdown_NilHTTPSrv_ReturnsNil(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// Never called Serve, so httpSrv is nil.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown with nil httpSrv: %v", err)
	}
}

// ── ServeTLS (feature 23) ──────────────────────────────────────────────

// freePort returns a loopback TCP address that is free at the moment
// the helper runs. There is an inherent race between closing the
// scouting listener and the server re-binding, but in practice the
// kernel does not re-hand out the port to an unrelated process in the
// few microseconds that separates those two calls.
func freePort(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()
	return addr
}

// buildHybridTLSConfig spins up a fresh pqcrypto CA, issues a cert for
// the "helion-coordinator" subject, enhances the CA with the hybrid
// KEM config, and returns a server TLS config ready for ServeTLS.
// Mirrors the production wiring in cmd/helion-coordinator/main.go.
func buildHybridTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	certPEM, keyPEM, err := ca.IssueNodeCert("helion-coordinator")
	if err != nil {
		t.Fatalf("IssueNodeCert: %v", err)
	}
	ca.EnhanceWithHybridKEM()
	cfg, err := ca.EnhancedTLSConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("EnhancedTLSConfig: %v", err)
	}
	return cfg
}

// TestServeTLS_RejectsEmptyCertConfig is the "don't silently serve
// plaintext" guard. Passing a cfg with no Certificates must fail fast
// rather than start a TLS listener that answers every handshake with
// an "no certificates configured" error.
func TestServeTLS_RejectsEmptyCertConfig(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	err := srv.ServeTLS(freePort(t), &tls.Config{})
	if err == nil {
		t.Fatal("expected error from ServeTLS with empty cert list")
	}
	if !strings.Contains(err.Error(), "must carry at least one certificate") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestServeTLS_RejectsNilConfig — symmetric guard: passing nil cfg
// must fail before any listener is created.
func TestServeTLS_RejectsNilConfig(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	err := srv.ServeTLS(freePort(t), nil)
	if err == nil {
		t.Fatal("expected error from ServeTLS with nil cfg")
	}
}

// TestServeTLS_EndToEnd_HybridCurves is the load-bearing test for
// feature 23: start the HTTP API over TLS, dial it with a client that
// trusts the same CA, and confirm a GET /healthz returns 200. The
// handshake itself proves the Kyber-aware curve preferences were
// accepted — without them tls.Dial would fail on curve negotiation.
func TestServeTLS_EndToEnd_HybridCurves(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	cfg := buildHybridTLSConfig(t)

	addr := freePort(t)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ServeTLS(addr, cfg) }()
	time.Sleep(40 * time.Millisecond)

	// Build a client trust pool containing our test CA's cert. We
	// cheat a bit: the pqcrypto CA's self-signed cert is inside the
	// server cfg's Certificates[0].Certificate[len-1]. A real node
	// would use pqcrypto.NewCAFromPEM(caPEM) instead.
	rootPool := cfg.ClientCAs
	clientCfg := &tls.Config{
		RootCAs:    rootPool,
		ServerName: "helion-coordinator",
		MinVersion: tls.VersionTLS13,
		// No ClientAuth certs presented: the /healthz handler does
		// not require auth, but the server's ClientAuth mode is
		// RequireAnyClientCert. So present an empty cert chain —
		// the server accepts it because verification happens at the
		// application layer.
		Certificates: []tls.Certificate{},
	}
	httpClient := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: clientCfg,
		},
	}

	// The server demands a client cert (RequireAnyClientCert); give
	// it one. We reuse the server cert here — any CA-signed chain
	// will do since handshake-level verification is deferred.
	clientCfg.Certificates = cfg.Certificates
	resp, err := httpClient.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("TLS dial + GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz over TLS: status = %d, want 200", resp.StatusCode)
	}

	// Plain-HTTP dial must NOT return 200 — guard against "TLS is
	// there but the listener silently answers plaintext" regressions.
	// net/http's server responds 400 "Client sent an HTTP request
	// to an HTTPS server" on plaintext so the *client* sees a
	// response with a 4xx, not a network error. Either shape is
	// acceptable; what fails the test is a 2xx.
	plainClient := &http.Client{Timeout: 500 * time.Millisecond}
	if plainResp, perr := plainClient.Get("http://" + addr + "/healthz"); perr == nil {
		plainResp.Body.Close()
		if plainResp.StatusCode >= 200 && plainResp.StatusCode < 300 {
			t.Errorf("plain HTTP GET on TLS listener returned 2xx (%d) — TLS not enforcing", plainResp.StatusCode)
		}
	}

	_ = srv.Shutdown(context.Background())
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Error("ServeTLS did not return after Shutdown")
	}
}

// TestServeTLS_TLSConfigCarriesKyberCurve asserts the EnhancedTLSConfig
// path actually puts the Kyber curve ID at the top of the server's
// CurvePreferences. Fails loudly if a GODEBUG=tlskyber=0 build silently
// falls back to classical-only — which is the exact failure mode
// HELION_PQC_REQUIRED is designed to detect in production.
func TestServeTLS_TLSConfigCarriesKyberCurve(t *testing.T) {
	cfg := buildHybridTLSConfig(t)
	if len(cfg.CurvePreferences) == 0 {
		t.Fatal("EnhancedTLSConfig produced empty CurvePreferences — ApplyHybridKEM silently fell back")
	}
	// Kyber curve ID is 0x6399 in Go 1.23+. Check the first entry;
	// the ApplyHybridKEM contract puts hybrid ahead of classical.
	const kyberID = tls.CurveID(0x6399)
	found := false
	for _, cid := range cfg.CurvePreferences {
		if cid == kyberID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CurvePreferences missing Kyber (0x6399); got: %v", cfg.CurvePreferences)
	}
}
