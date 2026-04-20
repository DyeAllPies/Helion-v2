// cmd/helion-issue-op-cert/main_test.go
//
// Table-driven tests for the operator-cert CLI. Each test injects
// a fake env/readFile/writeFile + an httptest.Server and asserts
// the resulting exit code + written P12.

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// mkSelfSignedPEM returns a minimal self-signed cert in PEM form.
// Used to exercise defaultNewClient's RootCAs path without needing
// a fixture file.
func mkSelfSignedPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca-1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newFakeDeps returns a deps wired to in-memory env/readFile/writeFile
// plus an httptest.Server URL. The test can tweak fields before use.
func newFakeDeps(envMap map[string]string, written map[string][]byte) deps {
	return deps{
		env: func(k string) string { return envMap[k] },
		readFile: func(path string) ([]byte, error) {
			if b, ok := written[path]; ok {
				return b, nil
			}
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
		},
		writeFile: func(path string, data []byte, _ os.FileMode) error {
			written[path] = data
			return nil
		},
		stderr: io.Discard,
		newClient: func(_ bool, _ []byte) (*http.Client, error) {
			return http.DefaultClient, nil
		},
	}
}

func goodServer(t *testing.T, wantCN, wantPW string, p12 []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/operator-certs" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer admin-tok" {
			http.Error(w, "missing/invalid auth", http.StatusUnauthorized)
			return
		}
		var req issueReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.CommonName != wantCN {
			t.Errorf("cn: got %q, want %q", req.CommonName, wantCN)
		}
		if req.P12Password != wantPW {
			t.Errorf("pw: got %q, want %q", req.P12Password, wantPW)
		}
		resp := issueResp{
			CommonName:     req.CommonName,
			SerialHex:      "deadbeef",
			FingerprintHex: "abc123",
			NotBefore:      "2026-04-20T00:00:00Z",
			NotAfter:       "2026-07-19T00:00:00Z",
			P12Base64:      base64.StdEncoding.EncodeToString(p12),
			AuditNotice:    "(audit: operator_cert_issued)",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// ── Success path ─────────────────────────────────────────────

func TestRun_HappyPath_P12Written(t *testing.T) {
	p12 := []byte("PKCS12-BYTES")
	srv := goodServer(t, "alice@ops", "topsecret", p12)
	defer srv.Close()

	written := map[string][]byte{}
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, written)

	code := run([]string{
		"--operator-cn", "alice@ops",
		"--p12-password", "topsecret",
		"--out", "/tmp/alice.p12",
	}, d)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if got := written["/tmp/alice.p12"]; !bytes.Equal(got, p12) {
		t.Fatalf("P12 bytes mismatch: got %q, want %q", got, p12)
	}
}

func TestRun_PasswordFromFile_Preferred(t *testing.T) {
	srv := goodServer(t, "bob@ops", "from-file", nil)
	defer srv.Close()

	written := map[string][]byte{
		"/secrets/alice.pw": []byte("from-file\n"), // trailing LF gets trimmed
	}
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, written)

	code := run([]string{
		"--operator-cn", "bob@ops",
		"--p12-password", "IGNORED",
		"--p12-password-file", "/secrets/alice.pw",
		"--out", "/tmp/bob.p12",
	}, d)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
}

func TestRun_CAFileSupplied_ReadIntoNewClient(t *testing.T) {
	srv := goodServer(t, "eve@ops", "pw", nil)
	defer srv.Close()

	written := map[string][]byte{
		"/etc/ca.pem": []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
	}
	saw := struct {
		insecure bool
		pem      []byte
	}{}
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
		"HELION_CA_FILE":     "/etc/ca.pem",
	}, written)
	d.newClient = func(insecure bool, pem []byte) (*http.Client, error) {
		saw.insecure = insecure
		saw.pem = pem
		return http.DefaultClient, nil
	}

	code := run([]string{
		"--operator-cn", "eve@ops",
		"--p12-password", "pw",
		"--out", "/tmp/eve.p12",
	}, d)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if saw.insecure {
		t.Error("newClient called with insecure=true when --insecure-skip-verify not set")
	}
	if !bytes.Contains(saw.pem, []byte("BEGIN CERTIFICATE")) {
		t.Errorf("CA PEM not forwarded to newClient: %q", saw.pem)
	}
}

func TestRun_InsecureSkipVerify_Wired(t *testing.T) {
	srv := goodServer(t, "eve@ops", "pw", nil)
	defer srv.Close()

	written := map[string][]byte{}
	var sawInsecure bool
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, written)
	d.newClient = func(insecure bool, _ []byte) (*http.Client, error) {
		sawInsecure = insecure
		return http.DefaultClient, nil
	}
	code := run([]string{
		"--operator-cn", "eve@ops",
		"--p12-password", "pw",
		"--out", "/tmp/eve.p12",
		"--insecure-skip-verify",
	}, d)
	if code != 0 || !sawInsecure {
		t.Fatalf("insecure flag: code=%d insecure=%v (want 0, true)", code, sawInsecure)
	}
}

// ── Usage errors (exit 1) ────────────────────────────────────

func TestRun_MissingCoordinator_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_TOKEN": "admin-tok",
	}, map[string][]byte{})
	if code := run([]string{"--operator-cn", "x", "--p12-password", "pw", "--out", "/x"}, d); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

func TestRun_MissingToken_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "https://c",
	}, map[string][]byte{})
	if code := run([]string{"--operator-cn", "x", "--p12-password", "pw", "--out", "/x"}, d); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

func TestRun_MissingCN_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "https://c",
		"HELION_TOKEN":       "tok",
	}, map[string][]byte{})
	if code := run([]string{"--p12-password", "pw", "--out", "/x"}, d); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

func TestRun_MissingOut_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "https://c",
		"HELION_TOKEN":       "tok",
	}, map[string][]byte{})
	if code := run([]string{"--operator-cn", "x", "--p12-password", "pw"}, d); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

func TestRun_MissingPassword_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "https://c",
		"HELION_TOKEN":       "tok",
	}, map[string][]byte{})
	if code := run([]string{"--operator-cn", "x", "--out", "/x"}, d); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

func TestRun_BadPasswordFile_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "https://c",
		"HELION_TOKEN":       "tok",
	}, map[string][]byte{})
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password-file", "/does/not/exist",
		"--out", "/x",
	}, d)
	if code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

func TestRun_BadCAFile_Exit1(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "https://c",
		"HELION_TOKEN":       "tok",
		"HELION_CA_FILE":     "/nope.pem",
	}, map[string][]byte{})
	if code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/x",
	}, d); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
}

// ── Coordinator errors (exit 2) ──────────────────────────────

func TestRun_CoordinatorReturns401_Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bearer token invalid"}`))
	}))
	defer srv.Close()

	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, map[string][]byte{})
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/tmp/x.p12",
	}, d)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestRun_CoordinatorReturns500_NoJSONError_Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("catastrophic failure — not JSON"))
	}))
	defer srv.Close()

	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, map[string][]byte{})
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/tmp/x.p12",
	}, d)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestRun_CoordinatorUnreachable_Exit2(t *testing.T) {
	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": "http://127.0.0.1:1/unreachable",
		"HELION_TOKEN":       "admin-tok",
	}, map[string][]byte{})
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/tmp/x.p12",
	}, d)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestRun_MalformedBase64P12_Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"p12_base64":"!!not-base64!!"}`))
	}))
	defer srv.Close()

	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, map[string][]byte{})
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/tmp/x.p12",
	}, d)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestRun_MalformedJSON_Exit2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, map[string][]byte{})
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/tmp/x.p12",
	}, d)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

// ── Filesystem error (exit 3) ────────────────────────────────

func TestRun_WriteFileFails_Exit3(t *testing.T) {
	srv := goodServer(t, "x", "pw", []byte("bytes"))
	defer srv.Close()

	d := newFakeDeps(map[string]string{
		"HELION_COORDINATOR": srv.URL,
		"HELION_TOKEN":       "admin-tok",
	}, map[string][]byte{})
	d.writeFile = func(_ string, _ []byte, _ os.FileMode) error {
		return os.ErrPermission
	}
	code := run([]string{
		"--operator-cn", "x",
		"--p12-password", "pw",
		"--out", "/forbidden.p12",
	}, d)
	if code != 3 {
		t.Fatalf("want exit 3, got %d", code)
	}
}

// ── Default newClient ────────────────────────────────────────

func TestDefaultNewClient_AcceptsValidPEM(t *testing.T) {
	c, err := defaultNewClient(false, mkSelfSignedPEM(t))
	if err != nil {
		t.Fatalf("AppendCertsFromPEM: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}

func TestDefaultNewClient_InsecureSkipVerify(t *testing.T) {
	c, err := defaultNewClient(true, nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}

func TestDefaultNewClient_EmptyPEM_NoPool(t *testing.T) {
	c, err := defaultNewClient(false, nil)
	if err != nil {
		t.Fatalf("empty PEM must not error: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}

func TestDefaultNewClient_NoCertsInPEM_Errors(t *testing.T) {
	// PEM-shaped header without a real BEGIN CERTIFICATE block —
	// AppendCertsFromPEM parses 0 certs and defaultNewClient errors.
	_, err := defaultNewClient(false, []byte("garbage nonsense"))
	if err == nil {
		t.Fatal("want error for PEM with zero certs, got nil")
	}
	if !strings.Contains(err.Error(), "no certs") {
		t.Errorf("unexpected error text: %v", err)
	}
}
