// internal/pqcrypto/hybrid_test.go
//
// Tests for hybrid-KEM TLS configuration (X25519 + ML-KEM-768).

package pqcrypto_test

import (
	"crypto/tls"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

func TestDefaultHybridConfig_EnabledByDefault(t *testing.T) {
	cfg := pqcrypto.DefaultHybridConfig()
	if cfg == nil {
		t.Fatal("expected non-nil HybridConfig")
	}
	if !cfg.EnableHybridKEM {
		t.Error("hybrid KEM should be enabled by default")
	}
	if cfg.CurvePreference == 0 {
		t.Error("curve preference should be set")
	}
}

func TestApplyHybridKEM_NilConfig_CreatesNew(t *testing.T) {
	hybridCfg := pqcrypto.DefaultHybridConfig()
	result := pqcrypto.ApplyHybridKEM(nil, hybridCfg)
	if result == nil {
		t.Fatal("expected non-nil tls.Config")
	}
}

func TestApplyHybridKEM_SetsCurvePreferences(t *testing.T) {
	hybridCfg := pqcrypto.DefaultHybridConfig()
	result := pqcrypto.ApplyHybridKEM(&tls.Config{}, hybridCfg)
	if len(result.CurvePreferences) == 0 {
		t.Error("expected curve preferences to be set")
	}
}

func TestApplyHybridKEM_DisabledHybrid_NoChange(t *testing.T) {
	hybridCfg := &pqcrypto.HybridConfig{EnableHybridKEM: false}
	original := &tls.Config{}
	result := pqcrypto.ApplyHybridKEM(original, hybridCfg)
	if len(result.CurvePreferences) != 0 {
		t.Error("disabled hybrid KEM should not set curve preferences")
	}
}

func TestEnhanceCAWithHybridKEM_NoError(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	// Should not panic or produce errors — no return value.
	ca.EnhanceWithHybridKEM()
}

func TestEnhancedTLSConfig_ReturnsConfig(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	ca.EnhanceWithHybridKEM()
	certPEM, keyPEM, _ := ca.IssueNodeCert("coord")
	cfg, err := ca.EnhancedTLSConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("EnhancedTLSConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
}

func TestEnhancedNodeTLSConfig_ReturnsConfig(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	ca.EnhanceWithHybridKEM()
	certPEM, keyPEM, _ := ca.IssueNodeCert("node-1")
	cfg, err := ca.EnhancedNodeTLSConfig(certPEM, keyPEM, "coordinator")
	if err != nil {
		t.Fatalf("EnhancedNodeTLSConfig: %v", err)
	}
	if cfg.ServerName != "coordinator" {
		t.Errorf("want ServerName=coordinator, got %q", cfg.ServerName)
	}
}

func TestEnhancedTLSConfig_InvalidPEM_ReturnsError(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, err = ca.EnhancedTLSConfig(badPEM, badPEM)
	if err == nil {
		t.Error("expected error for invalid cert/key PEM, got nil")
	}
}

func TestEnhancedNodeTLSConfig_InvalidPEM_ReturnsError(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, err = ca.EnhancedNodeTLSConfig(badPEM, badPEM, "coordinator")
	if err == nil {
		t.Error("expected error for invalid cert/key PEM, got nil")
	}
}

// ── hybridKEMEnabled GODEBUG branches ────────────────────────────────────────
// These three tests drive ApplyHybridKEM with varying GODEBUG values to cover
// the hybridKEMEnabled() decision paths. ApplyHybridKEM is the only external
// caller of hybridKEMEnabled, so exercising it via the env var is enough.

func TestApplyHybridKEM_GoDebugDisablesKyber_NoCurvePref(t *testing.T) {
	t.Setenv("GODEBUG", "tlskyber=0")
	cfg := pqcrypto.ApplyHybridKEM(nil, pqcrypto.DefaultHybridConfig())
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	// With tlskyber=0 the function short-circuits and does NOT add any
	// curve preferences.
	if len(cfg.CurvePreferences) != 0 {
		t.Errorf("tlskyber=0: want no curve preferences, got %v", cfg.CurvePreferences)
	}
}

func TestApplyHybridKEM_GoDebugEnablesKyber_SetsCurvePref(t *testing.T) {
	t.Setenv("GODEBUG", "tlskyber=1")
	cfg := pqcrypto.ApplyHybridKEM(nil, pqcrypto.DefaultHybridConfig())
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if len(cfg.CurvePreferences) == 0 {
		t.Error("tlskyber=1: want curve preferences set")
	}
}

func TestApplyHybridKEM_GoDebugUnrelatedKey_IgnoredAndDefaultApplies(t *testing.T) {
	t.Setenv("GODEBUG", "somethingelse=1,another=2")
	cfg := pqcrypto.ApplyHybridKEM(nil, pqcrypto.DefaultHybridConfig())
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	// Unrelated GODEBUG entries don't affect the decision — the default
	// (enabled) path applies and curve preferences are set.
	if len(cfg.CurvePreferences) == 0 {
		t.Error("default path: want curve preferences set")
	}
}
