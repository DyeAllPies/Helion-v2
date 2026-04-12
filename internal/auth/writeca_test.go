// internal/auth/writeca_test.go
//
// Tests for WriteCAFile and NewNodeBundleFromCAFile.

package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// ── WriteCAFile ──────────────────────────────────────────────────────────────

func TestWriteCAFile_EmptyPath_NoOp(t *testing.T) {
	if err := auth.WriteCAFile([]byte("pem"), ""); err != nil {
		t.Fatalf("expected nil for empty path, got %v", err)
	}
}

func TestWriteCAFile_WritesToDisk(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "ca.pem")

	if err := auth.WriteCAFile(ca.CertPEM, path); err != nil {
		t.Fatalf("WriteCAFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty CA file")
	}
}

func TestWriteCAFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "ca.pem")

	if err := auth.WriteCAFile([]byte("test-pem"), path); err != nil {
		t.Fatalf("WriteCAFile: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected file to exist")
	}
}

// ── NewNodeBundleFromCAFile ──────────────────────────────────────────────────

func TestNewNodeBundleFromCAFile_Success(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	// Write the coordinator CA to a temp file.
	dir := t.TempDir()
	caPath := filepath.Join(dir, "coord-ca.pem")
	if err := os.WriteFile(caPath, ca.CertPEM, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	bundle, err := auth.NewNodeBundleFromCAFile(caPath)
	if err != nil {
		t.Fatalf("NewNodeBundleFromCAFile: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
	if bundle.CA == nil {
		t.Error("expected bundle to have a CA")
	}
}

func TestNewNodeBundleFromCAFile_FileNotFound(t *testing.T) {
	_, err := auth.NewNodeBundleFromCAFile("/nonexistent/path/ca.pem")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestNewNodeBundleFromCAFile_InvalidPEM(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad-ca.pem")
	_ = os.WriteFile(badPath, []byte("not valid PEM"), 0o644)

	_, err := auth.NewNodeBundleFromCAFile(badPath)
	if err == nil {
		t.Error("expected error for invalid PEM")
	}
}
