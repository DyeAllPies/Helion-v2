// cmd/helion-issue-op-cert/main.go
//
// helion-issue-op-cert asks the coordinator to mint a dashboard
// operator client certificate (feature 27 — browser mTLS).
//
// Design note: the original spec proposed a file-system CLI that
// read the CA private key from a shared state volume. The
// coordinator's current architecture generates the CA in memory on
// every boot (see internal/auth/NewCoordinatorBundle), so a file-
// based CLI has nothing to read. This binary is a thin HTTP client
// that hits POST /admin/operator-certs instead — same audited
// admin path the (future) web UI will use.
//
// Usage
// ─────
//   helion-issue-op-cert --operator-cn alice@ops \
//                        --ttl-days 90 \
//                        --p12-password "<password>" \
//                        --out alice.p12
//
// Environment
// ───────────
//   HELION_COORDINATOR   HTTPS address of the coordinator API (required).
//                        Example: https://127.0.0.1:8080
//   HELION_TOKEN         Admin JWT bearer token (required).
//   HELION_CA_FILE       Path to the coordinator's CA cert PEM; used to
//                        verify the coordinator's server cert. Optional —
//                        default is to rely on the system trust store (not
//                        helpful for a self-signed dev CA).
//
// Exit codes
// ──────────
//   0   cert issued successfully
//   1   usage error
//   2   HTTP error from the coordinator (body printed to stderr)
//   3   local filesystem error writing the P12
//
// Security notes
// ──────────────
//   - The --p12-password flag appears in shell history + `ps` output;
//     prefer piping the password via --p12-password-file for production
//     use. When --p12-password-file is set, --p12-password is ignored.
//   - The returned PEM + private key are in the JSON response body.
//     This binary writes the P12 (binary) to --out and discards the
//     PEM; the full JSON response is NOT retained on disk.
//   - Every issuance is audited server-side as `operator_cert_issued`.

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// issueReq + issueResp mirror the api.IssueOperatorCert{Request,Response}
// shapes. Duplicated here so cmd/ doesn't import internal/api (there
// is no public API surface today; this keeps the CLI compilation
// independent of the api package's internal churn).
type issueReq struct {
	CommonName  string `json:"common_name"`
	TTLDays     int    `json:"ttl_days,omitempty"`
	P12Password string `json:"p12_password"`
}

type issueResp struct {
	CommonName     string `json:"common_name"`
	SerialHex      string `json:"serial_hex"`
	FingerprintHex string `json:"fingerprint_hex"`
	NotBefore      string `json:"not_before"`
	NotAfter       string `json:"not_after"`
	P12Base64      string `json:"p12_base64"`
	AuditNotice    string `json:"audit_notice"`
}

type errResp struct {
	Error string `json:"error"`
}

// deps groups the process-global side-effects run() depends on.
// Tests inject fakes; main() wires os.* implementations.
type deps struct {
	env       func(string) string
	readFile  func(string) ([]byte, error)
	writeFile func(string, []byte, os.FileMode) error
	stderr    io.Writer
	// newClient returns the HTTP client run() uses. Tests return a
	// client pointed at an httptest.Server; main() returns a real
	// client configured from --insecure-skip-verify / HELION_CA_FILE.
	newClient func(insecureSkipVerify bool, caPEM []byte) (*http.Client, error)
}

func main() {
	os.Exit(run(os.Args[1:], deps{
		env:       os.Getenv,
		readFile:  os.ReadFile,
		writeFile: os.WriteFile,
		stderr:    os.Stderr,
		newClient: defaultNewClient,
	}))
}

func defaultNewClient(insecureSkipVerify bool, caPEM []byte) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if insecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 — dev-only opt-in
	} else if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("no certs parsed from CA PEM")
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}, nil
}

// run is the full CLI lifecycle. Returns the process exit code.
// Split out from main() so tests can drive it without calling
// os.Exit or touching the real process env.
func run(args []string, d deps) int {
	fs := flag.NewFlagSet("helion-issue-op-cert", flag.ContinueOnError)
	fs.SetOutput(d.stderr)
	var (
		cn              = fs.String("operator-cn", "", "Operator CommonName. Required.")
		ttlDays         = fs.Int("ttl-days", 0, "Certificate lifetime in days. Default 90 (coordinator-side).")
		p12PW           = fs.String("p12-password", "", "Password protecting the PKCS#12 bundle. Required.")
		p12PWFile       = fs.String("p12-password-file", "", "Read P12 password from this file (newline-trimmed). Overrides --p12-password.")
		outPath         = fs.String("out", "", "Path to write the PKCS#12 file. Required.")
		skipServerVerif = fs.Bool("insecure-skip-verify", false, "Do NOT verify the coordinator's server cert. DEV ONLY.")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}

	coord := strings.TrimRight(d.env("HELION_COORDINATOR"), "/")
	if coord == "" {
		return failf(d.stderr, 1, "HELION_COORDINATOR is required (e.g. https://127.0.0.1:8080)")
	}
	tok := d.env("HELION_TOKEN")
	if tok == "" {
		return failf(d.stderr, 1, "HELION_TOKEN is required (admin JWT)")
	}
	if *cn == "" {
		return failf(d.stderr, 1, "--operator-cn is required")
	}
	if *outPath == "" {
		return failf(d.stderr, 1, "--out is required (path to write .p12 file)")
	}

	password := *p12PW
	if *p12PWFile != "" {
		raw, err := d.readFile(*p12PWFile)
		if err != nil {
			return failf(d.stderr, 1, "read p12 password file: %v", err)
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	if password == "" {
		return failf(d.stderr, 1, "either --p12-password or --p12-password-file is required")
	}

	body, err := json.Marshal(issueReq{
		CommonName:  *cn,
		TTLDays:     *ttlDays,
		P12Password: password,
	})
	if err != nil {
		return failf(d.stderr, 1, "marshal request: %v", err)
	}

	var caPEM []byte
	if !*skipServerVerif {
		if caPath := d.env("HELION_CA_FILE"); caPath != "" {
			caPEM, err = d.readFile(caPath)
			if err != nil {
				return failf(d.stderr, 1, "read HELION_CA_FILE: %v", err)
			}
		}
	} else {
		fmt.Fprintln(d.stderr, "[warn] --insecure-skip-verify: coordinator server cert NOT verified")
	}

	httpClient, err := d.newClient(*skipServerVerif, caPEM)
	if err != nil {
		return failf(d.stderr, 1, "HELION_CA_FILE: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, coord+"/admin/operator-certs", bytes.NewReader(body))
	if err != nil {
		return failf(d.stderr, 1, "build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := httpClient.Do(req)
	if err != nil {
		return failf(d.stderr, 2, "POST /admin/operator-certs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e errResp
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &e)
		if e.Error != "" {
			return failf(d.stderr, 2, "coordinator %d: %s", resp.StatusCode, e.Error)
		}
		return failf(d.stderr, 2, "coordinator %d: %s", resp.StatusCode, string(raw))
	}

	var ir issueResp
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return failf(d.stderr, 2, "decode response: %v", err)
	}

	p12, err := base64.StdEncoding.DecodeString(ir.P12Base64)
	if err != nil {
		return failf(d.stderr, 2, "decode p12_base64: %v", err)
	}
	// 0600: the P12 is encrypted but the password strength is the
	// only thing between the file and the private key. World-
	// readable defeats the point.
	if err := d.writeFile(*outPath, p12, 0o600); err != nil {
		return failf(d.stderr, 3, "write %s: %v", *outPath, err)
	}

	fmt.Fprintf(d.stderr, "issued operator cert:\n")
	fmt.Fprintf(d.stderr, "  common_name      %s\n", ir.CommonName)
	fmt.Fprintf(d.stderr, "  serial_hex       %s\n", ir.SerialHex)
	fmt.Fprintf(d.stderr, "  fingerprint_hex  %s\n", ir.FingerprintHex)
	fmt.Fprintf(d.stderr, "  not_before       %s\n", ir.NotBefore)
	fmt.Fprintf(d.stderr, "  not_after        %s\n", ir.NotAfter)
	fmt.Fprintf(d.stderr, "  p12              %s (%d bytes)\n", *outPath, len(p12))
	fmt.Fprintf(d.stderr, "\n%s\n", ir.AuditNotice)
	return 0
}

func failf(stderr io.Writer, code int, format string, args ...interface{}) int {
	fmt.Fprintf(stderr, "helion-issue-op-cert: "+format+"\n", args...)
	return code
}
