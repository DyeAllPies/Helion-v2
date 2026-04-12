// cmd/helion-node/main_test.go
//
// Tests for the pure-logic helpers in the helion-node entry point.
// The side-effectful parts of main() (grpc server, signal wait, heartbeat
// loop) are exercised end-to-end by tests/integration.

package main

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/runtime"
)

// discardLogger is a silent slog.Logger used by tests that only care about
// whether the code path runs — not what it writes to stderr.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── envOr ─────────────────────────────────────────────────────────────────────

func TestEnvOr_EnvSet_ReturnsEnvValue(t *testing.T) {
	t.Setenv("HELION_TEST_VAR", "from-env")
	if got := envOr("HELION_TEST_VAR", "fallback"); got != "from-env" {
		t.Errorf("want 'from-env', got %q", got)
	}
}

func TestEnvOr_EnvUnset_ReturnsFallback(t *testing.T) {
	// t.Setenv with "" still sets an empty string; os.Unsetenv is the
	// right way to simulate "unset".
	_ = os.Unsetenv("HELION_TEST_VAR")
	if got := envOr("HELION_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("want 'fallback', got %q", got)
	}
}

func TestEnvOr_EnvEmpty_ReturnsFallback(t *testing.T) {
	t.Setenv("HELION_TEST_VAR", "")
	if got := envOr("HELION_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("empty env should use fallback, got %q", got)
	}
}

// ── loadNodeConfig ────────────────────────────────────────────────────────────

func TestLoadNodeConfig_AllDefaults(t *testing.T) {
	// Clear every env var loadNodeConfig reads so we see the pure defaults.
	for _, k := range []string{
		"PORT", "HELION_COORDINATOR", "HELION_RUNTIME",
		"HELION_RUNTIME_SOCKET", "HELION_NODE_ID", "HELION_NODE_ADDR",
	} {
		_ = os.Unsetenv(k)
	}

	cfg := loadNodeConfig("testhost")

	if cfg.Port != "8080" {
		t.Errorf("Port: want 8080, got %q", cfg.Port)
	}
	if cfg.CoordinatorAddr != "coordinator:9090" {
		t.Errorf("CoordinatorAddr: want coordinator:9090, got %q", cfg.CoordinatorAddr)
	}
	if cfg.RuntimeBackend != "go" {
		t.Errorf("RuntimeBackend: want go, got %q", cfg.RuntimeBackend)
	}
	if cfg.RuntimeSocket != "/run/helion/runtime.sock" {
		t.Errorf("RuntimeSocket: want /run/helion/runtime.sock, got %q", cfg.RuntimeSocket)
	}
	if cfg.NodeID != "testhost:8080" {
		t.Errorf("NodeID: want testhost:8080, got %q", cfg.NodeID)
	}
	if cfg.NodeAddr != "testhost:8080" {
		t.Errorf("NodeAddr: want testhost:8080, got %q", cfg.NodeAddr)
	}
}

func TestLoadNodeConfig_OverridesFromEnv(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("HELION_COORDINATOR", "coord.local:9191")
	t.Setenv("HELION_RUNTIME", "rust")
	t.Setenv("HELION_RUNTIME_SOCKET", "/tmp/rt.sock")
	t.Setenv("HELION_NODE_ID", "node-abc")
	t.Setenv("HELION_NODE_ADDR", "10.0.0.42:9000")

	cfg := loadNodeConfig("ignored-hostname")

	if cfg.Port != "9000" {
		t.Errorf("Port: want 9000, got %q", cfg.Port)
	}
	if cfg.CoordinatorAddr != "coord.local:9191" {
		t.Errorf("CoordinatorAddr: got %q", cfg.CoordinatorAddr)
	}
	if cfg.RuntimeBackend != "rust" {
		t.Errorf("RuntimeBackend: got %q", cfg.RuntimeBackend)
	}
	if cfg.RuntimeSocket != "/tmp/rt.sock" {
		t.Errorf("RuntimeSocket: got %q", cfg.RuntimeSocket)
	}
	if cfg.NodeID != "node-abc" {
		t.Errorf("NodeID: got %q", cfg.NodeID)
	}
	if cfg.NodeAddr != "10.0.0.42:9000" {
		t.Errorf("NodeAddr: got %q", cfg.NodeAddr)
	}
}

func TestLoadNodeConfig_IDDerivedFromHostnameAndPort(t *testing.T) {
	// Set a non-default PORT but leave NODE_ID unset to prove the fallback
	// uses the *current* port, not the literal default.
	_ = os.Unsetenv("HELION_NODE_ID")
	_ = os.Unsetenv("HELION_NODE_ADDR")
	t.Setenv("PORT", "5000")

	cfg := loadNodeConfig("worker-01")
	if cfg.NodeID != "worker-01:5000" {
		t.Errorf("NodeID derived: want worker-01:5000, got %q", cfg.NodeID)
	}
	if cfg.NodeAddr != "worker-01:5000" {
		t.Errorf("NodeAddr derived: want worker-01:5000, got %q", cfg.NodeAddr)
	}
}

// ── splitCertKeyPEM ──────────────────────────────────────────────────────────

func TestSplitCertKeyPEM_ValidPayload(t *testing.T) {
	certBlock := "-----BEGIN CERTIFICATE-----\nMIIBxjCCAW2g\n-----END CERTIFICATE-----\n"
	keyBlock := "-----BEGIN EC PRIVATE KEY-----\nMHQCAQEE\n-----END EC PRIVATE KEY-----\n"
	payload := []byte(certBlock + keyBlock)

	cert, key := splitCertKeyPEM(payload)
	if len(cert) == 0 {
		t.Error("expected non-empty cert PEM")
	}
	if len(key) == 0 {
		t.Error("expected non-empty key PEM")
	}
}

func TestSplitCertKeyPEM_EmptyPayload(t *testing.T) {
	cert, key := splitCertKeyPEM(nil)
	if len(cert) != 0 || len(key) != 0 {
		t.Errorf("empty payload should return empty cert/key, got cert=%d key=%d", len(cert), len(key))
	}
}

func TestSplitCertKeyPEM_CertOnly(t *testing.T) {
	certBlock := "-----BEGIN CERTIFICATE-----\nMIIBxjCCAW2g\n-----END CERTIFICATE-----\n"
	cert, key := splitCertKeyPEM([]byte(certBlock))
	if len(cert) == 0 {
		t.Error("expected cert PEM")
	}
	if len(key) != 0 {
		t.Error("expected empty key PEM for cert-only payload")
	}
}

func TestSplitCertKeyPEM_PrivateKeyType(t *testing.T) {
	// PRIVATE KEY (PKCS#8) should also be recognized.
	keyBlock := "-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----\n"
	cert, key := splitCertKeyPEM([]byte(keyBlock))
	if len(cert) != 0 {
		t.Error("expected empty cert for key-only payload")
	}
	if len(key) == 0 {
		t.Error("expected PRIVATE KEY to be recognized")
	}
}

// ── selectRuntime ─────────────────────────────────────────────────────────────

func TestSelectRuntime_DefaultReturnsGoRuntime(t *testing.T) {
	rt := selectRuntime("go", "/ignored", discardLogger())
	defer rt.Close()
	if _, ok := rt.(*runtime.GoRuntime); !ok {
		t.Errorf("want *runtime.GoRuntime for 'go', got %T", rt)
	}
}

func TestSelectRuntime_UnknownBackendFallsBackToGo(t *testing.T) {
	// Any unrecognised value must default to the Go runtime rather than
	// returning nil — a typo in HELION_RUNTIME should never crash the agent.
	rt := selectRuntime("typo", "/ignored", discardLogger())
	defer rt.Close()
	if _, ok := rt.(*runtime.GoRuntime); !ok {
		t.Errorf("unknown backend: want *runtime.GoRuntime, got %T", rt)
	}
}

func TestSelectRuntime_RustBackendReturnsRustClient(t *testing.T) {
	rt := selectRuntime("rust", "/tmp/nonexistent.sock", discardLogger())
	defer rt.Close()
	// RustClient doesn't need to connect at construction time; the socket
	// is only dialed on first Run/Cancel call.
	if _, ok := rt.(*runtime.RustClient); !ok {
		t.Errorf("rust backend: want *runtime.RustClient, got %T", rt)
	}
}
