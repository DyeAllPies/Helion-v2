// internal/api/operator_cert.go
//
// Feature 27 — browser mTLS for dashboard operators.
//
// Three concerns live in this file:
//
//   1. Types + helpers for the three-tier enforcement gate
//      (`off` / `warn` / `on`) and the Server's CA handle.
//   2. clientCertMiddleware — extracts the verified operator CN
//      from the TLS handshake OR from loopback-only proxy headers
//      and stores it in the request context. Emits
//      `operator_cert_missing` audit events in `warn` mode and
//      rejects cert-less requests in `on` mode.
//   3. handleIssueOperatorCert — POST /admin/operator-certs. Admin-
//      only HTTP path to mint a new P12 bundle for an operator. The
//      CA is in-memory (generated per coordinator boot) so a file-
//      based CLI cannot read it; issuance is mediated through this
//      audited admin endpoint instead.
//
// Design note on why issuance is an HTTP admin endpoint rather
// than the file-based CLI in the original spec:
//
//   The coordinator's CA private key lives in memory only (see
//   auth.NewCoordinatorBundle). A CLI that reads the CA from
//   /app/state/ca.key would require a whole new CA persistence
//   slice (related to feature 30's envelope encryption plan).
//   An HTTP admin endpoint fits the existing
//   adminMiddleware/rate-limit/audit pattern and covers the same
//   use case — an admin issues certs for operators they trust.

package api

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// ── Types ────────────────────────────────────────────────────────────────────

// clientCertIssuer is the subset of pqcrypto.CA the feature-27
// handler needs. Keeping the dependency narrow lets tests plug in a
// mock without pulling the full CA struct.
type clientCertIssuer interface {
	IssueOperatorCert(cn string, ttl time.Duration) (certPEM, keyPEM []byte, err error)
	ClientCertPool() *x509.CertPool
}

// ClientCertTier is the exported alias of clientCertTier so the
// coordinator (cmd/helion-coordinator/main.go) can parse the env
// var and install the tier via SetClientCertTier without exposing
// the internal enum shape.
type ClientCertTier = clientCertTier

// Exported tier values for the coordinator to compare against when
// deciding whether to mutate the TLS config (ClientAuth + ClientCAs).
const (
	ClientCertOff  = clientCertOff
	ClientCertWarn = clientCertWarn
	ClientCertOn   = clientCertOn
)

// ParseClientCertTierFromEnv parses the HELION_REST_CLIENT_CERT_REQUIRED
// env-var value. Exported so cmd/helion-coordinator/main.go can
// validate the config at boot time. Malformed values return an
// error; the coordinator treats that as fatal so a typo never
// silently weakens security.
func ParseClientCertTierFromEnv(raw string) (ClientCertTier, error) {
	return parseClientCertTier(raw)
}

// clientCertTier controls whether the REST listener requires a
// verified client certificate from the peer.
//
//   - clientCertOff:  no effect. Default; existing behaviour.
//   - clientCertWarn: every request is served, but requests that
//     arrived without a verified client cert emit an
//     `operator_cert_missing` audit event. Used for staged
//     rollouts (admin turns on `warn`, watches the log for who's
//     still bearer-only, tells them to install certs, then flips
//     to `on`).
//   - clientCertOn:   cert-less requests are refused at the TLS
//     handshake (coordinator-terminates-TLS) or at the handler
//     (nginx-terminates-TLS via loopback-only proxy headers).
type clientCertTier int

const (
	clientCertOff clientCertTier = iota
	clientCertWarn
	clientCertOn
)

// parseClientCertTier maps the HELION_REST_CLIENT_CERT_REQUIRED
// value to the internal tier. Unknown values fall back to `off`
// with a WARN so a typo cannot silently weaken security further
// than the default.
func parseClientCertTier(raw string) (clientCertTier, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off", "false", "0", "no":
		return clientCertOff, nil
	case "warn":
		return clientCertWarn, nil
	case "on", "true", "1", "yes", "required":
		return clientCertOn, nil
	default:
		return clientCertOff, fmt.Errorf("HELION_REST_CLIENT_CERT_REQUIRED: unrecognised value %q (want off/warn/on)", raw)
	}
}

// String returns the canonical token for logs + audit.
func (t clientCertTier) String() string {
	switch t {
	case clientCertOff:
		return "off"
	case clientCertWarn:
		return "warn"
	case clientCertOn:
		return "on"
	default:
		return "unknown"
	}
}

// operatorCNKey is the request-context key that carries the
// verified operator CN extracted by clientCertMiddleware. Handlers
// consult this to stamp the CN into audit events alongside the JWT
// subject; empty string means "no verified client cert on this
// request" (which is legal in `off` and `warn` tiers).
type opCNKey struct{}

var operatorCNKey = opCNKey{}

// OperatorCNFromContext returns the verified operator CN stored in
// ctx by clientCertMiddleware, or "" if no cert was presented or
// verified. Exported so external test packages can exercise the
// audit-stamping behaviour.
func OperatorCNFromContext(ctx context.Context) string {
	cn, _ := ctx.Value(operatorCNKey).(string)
	return cn
}

// stampOperatorCN copies details and adds `operator_cn` when the
// context carries a verified operator CN. No-op when tier=off or
// when the request arrived without a cert.
//
// Take-a-copy semantics so the caller's map isn't mutated (the
// same details map sometimes feeds multiple audit calls in a row).
// Returns the input unchanged when there's no CN to stamp — saves
// an allocation on the common case.
func stampOperatorCN(ctx context.Context, details map[string]interface{}) map[string]interface{} {
	cn := OperatorCNFromContext(ctx)
	if cn == "" {
		return details
	}
	out := make(map[string]interface{}, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	out["operator_cn"] = cn
	return out
}

// ── SetClientCertTier / SetOperatorCA ───────────────────────────────────────

// SetClientCertTier installs the feature-27 enforcement tier on the
// Server. Must be called before Serve. Pass the tier parsed from
// HELION_REST_CLIENT_CERT_REQUIRED via parseClientCertTier.
func (s *Server) SetClientCertTier(tier clientCertTier) {
	s.clientCertTier = tier
}

// SetOperatorCA installs the CA handle used by
// POST /admin/operator-certs. Must be called before Serve. Pass the
// same CA the coordinator uses for node certs (nodes + operators
// share one CA; operator certs carry ClientAuth-only EKU to keep
// the trust direction one-way). Registers the handler route.
func (s *Server) SetOperatorCA(ca clientCertIssuer) {
	s.operatorCA = ca
	s.mux.HandleFunc("POST /admin/operator-certs", s.authMiddleware(s.adminMiddleware(s.handleIssueOperatorCert)))
}

// ── clientCertMiddleware ────────────────────────────────────────────────────

// clientCertMiddleware extracts the verified operator CN from the
// request and stamps it into the context so downstream handlers can
// include it in audit events.
//
// Two ways a verified operator CN reaches this handler:
//
//   (a) Direct TLS to the coordinator. When the REST listener has
//       ClientAuth=RequireAndVerifyClientCert (tier=on) or
//       VerifyClientCertIfGiven (tier=warn), the crypto/tls layer
//       populates r.TLS.PeerCertificates with the verified chain.
//
//   (b) Nginx terminates TLS. Nginx's `ssl_verify_client on` +
//       `proxy_set_header X-SSL-Client-Verify / -S-DN / -Fingerprint`
//       forwards the verification result to the coordinator. The
//       coordinator trusts those headers ONLY when the request
//       arrived from loopback (127.0.0.1 / ::1) — a direct-from-
//       internet request carrying forged X-SSL-Client-* headers
//       would otherwise be a header-smuggling bypass.
//
// In both cases, the middleware extracts the CN, stamps the
// context, and forwards. `warn` mode additionally emits an audit
// event when no verified cert was found; `on` mode rejects such
// requests at 401.
//
// Precedence: this middleware runs BEFORE authMiddleware, so a
// cert-less request in `on` mode never reaches JWT validation.
func (s *Server) clientCertMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.clientCertTier == clientCertOff {
			next(w, r)
			return
		}
		// Health + readiness endpoints stay cert-free so k8s-style
		// probes (which can't present a browser-issued client cert)
		// keep working. These endpoints are unauthenticated anyway
		// and reveal no operational detail worth protecting.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next(w, r)
			return
		}
		cn, verified := s.extractVerifiedCN(r)
		if verified {
			ctx := context.WithValue(r.Context(), operatorCNKey, cn)
			next(w, r.WithContext(ctx))
			return
		}
		// No verified cert.
		switch s.clientCertTier {
		case clientCertWarn:
			if s.audit != nil {
				if err := s.audit.Log(r.Context(), audit.EventOperatorCertMissing, actorFromContext(r.Context()), map[string]interface{}{
					"path":        r.URL.Path,
					"remote_addr": r.RemoteAddr,
				}); err != nil {
					logAuditErr(false, "operator_cert_missing", err)
				}
			}
			next(w, r)
		case clientCertOn:
			if s.audit != nil {
				if err := s.audit.Log(r.Context(), audit.EventOperatorCertMissing, actorFromContext(r.Context()), map[string]interface{}{
					"path":        r.URL.Path,
					"remote_addr": r.RemoteAddr,
					"enforced":    true,
				}); err != nil {
					logAuditErr(false, "operator_cert_missing", err)
				}
			}
			writeError(w, http.StatusUnauthorized, "client certificate required (HELION_REST_CLIENT_CERT_REQUIRED=on)")
		default:
			next(w, r)
		}
	}
}

// extractVerifiedCN returns (cn, true) when the caller presented a
// verified client cert via either direct TLS (r.TLS) or
// loopback-only Nginx proxy headers. Returns ("", false) otherwise.
func (s *Server) extractVerifiedCN(r *http.Request) (string, bool) {
	// Direct TLS path. crypto/tls populates PeerCertificates only
	// when ClientAuth was VerifyClientCertIfGiven /
	// RequireAndVerifyClientCert AND a cert actually verified.
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		leaf := r.TLS.PeerCertificates[0]
		if leaf.Subject.CommonName != "" {
			return leaf.Subject.CommonName, true
		}
	}
	// Proxy path. Only trust X-SSL-Client-* when the immediate peer
	// is loopback — otherwise the attacker is the one setting the
	// header.
	if isLoopbackRemote(r.RemoteAddr) {
		if strings.EqualFold(r.Header.Get("X-SSL-Client-Verify"), "SUCCESS") {
			// Nginx emits the Subject DN as "CN=alice@ops,O=Helion"
			// (Nginx's own formatting). Extract the CN portion.
			dn := r.Header.Get("X-SSL-Client-S-DN")
			if cn := cnFromDN(dn); cn != "" {
				return cn, true
			}
		}
	}
	return "", false
}

// isLoopbackRemote reports whether the request.RemoteAddr parses to
// a loopback address. Any non-loopback peer sending X-SSL-Client-*
// headers is treated as if those headers weren't present.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// cnFromDN extracts the CommonName value from an RFC 2253 /
// OpenSSL-style Distinguished Name string like "CN=alice@ops,O=Helion".
// Returns "" if no CN attribute is present. Defensive: trims
// whitespace, honours quoted values minimally (rejects DNs with
// unescaped commas inside CN — Nginx doesn't emit those for valid
// CNs).
func cnFromDN(dn string) string {
	for _, part := range strings.Split(dn, ",") {
		part = strings.TrimSpace(part)
		if eq := strings.IndexByte(part, '='); eq > 0 {
			key := strings.ToUpper(strings.TrimSpace(part[:eq]))
			if key == "CN" {
				return strings.TrimSpace(part[eq+1:])
			}
		}
	}
	return ""
}

// ── POST /admin/operator-certs — issuance handler ────────────────────────────

// maxIssueOpCertBodyBytes caps the request body. The request is
// tiny (CN + TTL + password) so anything bigger is either a mistake
// or an exhaustion attempt.
const maxIssueOpCertBodyBytes = 4 * 1024

// Minimum acceptable P12 password length. PKCS#12 uses it to derive
// the encryption key; short passwords are trivially brute-forceable
// against an exported P12 file.
const minP12PasswordLen = 8

// revealSecret-style reveal audit failure was critical; the same
// applies here — a cert issuance without a durable audit record is
// an access grant with no accountability.
func (s *Server) handleIssueOperatorCert(w http.ResponseWriter, r *http.Request) {
	if s.operatorCA == nil {
		writeError(w, http.StatusNotImplemented, "operator CA not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxIssueOpCertBodyBytes)

	actor := "unknown"
	if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
		actor = claims.Subject
	}

	// Rate limit per admin subject. Issuance is expensive and
	// produces long-lived credentials; the limiter bounds the
	// damage of a leaked admin token.
	if !s.issueOpCertAllow(actor) {
		writeError(w, http.StatusTooManyRequests, "operator-cert issuance rate limit exceeded")
		return
	}

	var req IssueOperatorCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditOpCertReject(r, actor, "", "invalid request body")
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}
	if msg := validateIssueOperatorCertRequest(&req); msg != "" {
		s.auditOpCertReject(r, actor, req.CommonName, msg)
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	ttl := 90 * 24 * time.Hour
	if req.TTLDays > 0 {
		ttl = time.Duration(req.TTLDays) * 24 * time.Hour
	}

	certPEM, keyPEM, err := s.operatorCA.IssueOperatorCert(req.CommonName, ttl)
	if err != nil {
		slog.Error("IssueOperatorCert failed",
			slog.String("cn", req.CommonName), slog.Any("err", err))
		s.auditOpCertReject(r, actor, req.CommonName, "issuance failed")
		writeError(w, http.StatusInternalServerError, "operator-cert issuance failed")
		return
	}

	cert, key, err := parseCertKeyPEM(certPEM, keyPEM)
	if err != nil {
		slog.Error("parse issued cert", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "operator-cert post-parse failed")
		return
	}

	// PKCS#12 encoding. go-pkcs12's Modern2023 profile uses AES-256
	// + PBKDF2-SHA-256 (browser-compatible + strong). The legacy
	// RC2/3DES encoders are deliberately not used.
	p12Bytes, err := pkcs12.Modern2023.Encode(key, cert, nil, req.P12Password)
	if err != nil {
		slog.Error("pkcs12 encode", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "operator-cert PKCS#12 encode failed")
		return
	}

	fpRaw := sha256.Sum256(cert.Raw)
	fingerprint := hex.EncodeToString(fpRaw[:])
	serial := cert.SerialNumber.Text(16)

	// Audit FIRST, then respond. A downed audit sink fails the
	// issuance closed — no cert leaves the coordinator without a
	// durable accountability record.
	if s.audit != nil {
		if err := s.audit.Log(r.Context(), audit.EventOperatorCertIssued, actor, stampOperatorCN(r.Context(), map[string]interface{}{
			"common_name":     req.CommonName,
			"serial_hex":      serial,
			"fingerprint_hex": fingerprint,
			"not_before":      cert.NotBefore.Format(time.RFC3339Nano),
			"not_after":       cert.NotAfter.Format(time.RFC3339Nano),
		})); err != nil {
			logAuditErr(true, "operator_cert_issued", err)
			writeError(w, http.StatusInternalServerError, "audit log unavailable; issuance refused")
			return
		}
	}

	resp := IssueOperatorCertResponse{
		CommonName:     req.CommonName,
		SerialHex:      serial,
		FingerprintHex: fingerprint,
		NotBefore:      cert.NotBefore,
		NotAfter:       cert.NotAfter,
		CertPEM:        string(certPEM),
		KeyPEM:         string(keyPEM),
		P12Base64:      base64.StdEncoding.EncodeToString(p12Bytes),
		AuditNotice: "This issuance was recorded in the audit log " +
			"(event type: operator_cert_issued). Serial " + serial +
			" is revocable via a future CRL/OCSP endpoint (feature 31).",
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleIssueOperatorCert", resp)
}

// parseCertKeyPEM decodes the cert + key PEM blobs the CA returned
// into the objects go-pkcs12 needs for Encode. Defensive because
// the CA marshals EC keys as "EC PRIVATE KEY" today; if that ever
// changes to PKCS#8, we want a clear error here rather than a
// silent encode-time panic.
func parseCertKeyPEM(certPEM, keyPEM []byte) (*x509.Certificate, interface{}, error) {
	cBlock, _ := pem.Decode(certPEM)
	if cBlock == nil {
		return nil, nil, fmt.Errorf("operator cert: PEM decode returned nil block")
	}
	cert, err := x509.ParseCertificate(cBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("operator cert: parse: %w", err)
	}
	kBlock, _ := pem.Decode(keyPEM)
	if kBlock == nil {
		return nil, nil, fmt.Errorf("operator key: PEM decode returned nil block")
	}
	var key interface{}
	switch kBlock.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(kBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("operator key (EC): %w", err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(kBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("operator key (PKCS#8): %w", err)
		}
		key = k
	default:
		return nil, nil, fmt.Errorf("operator key: unsupported PEM type %q", kBlock.Type)
	}
	return cert, key, nil
}

// validateIssueOperatorCertRequest enforces the submit-time shape
// rules on POST /admin/operator-certs bodies.
func validateIssueOperatorCertRequest(r *IssueOperatorCertRequest) string {
	if r.CommonName == "" {
		return "common_name is required"
	}
	if len(r.CommonName) > 256 {
		return "common_name must not exceed 256 bytes"
	}
	if strings.ContainsAny(r.CommonName, "\x00=") {
		return "common_name must not contain NUL or '='"
	}
	if r.TTLDays < 0 {
		return "ttl_days must not be negative"
	}
	if r.TTLDays > 3650 {
		return "ttl_days must not exceed 3650 (10 years; CA cap will also apply)"
	}
	if len(r.P12Password) < minP12PasswordLen {
		return fmt.Sprintf("p12_password must be at least %d bytes", minP12PasswordLen)
	}
	if strings.ContainsRune(r.P12Password, '\x00') {
		return "p12_password must not contain NUL"
	}
	return ""
}

// auditOpCertReject records a failed issuance attempt. Mirrors
// handleRevealSecret's auditRevealReject: every reject is audited
// so enumeration probes show up in the audit stream.
func (s *Server) auditOpCertReject(r *http.Request, actor, cn, reason string) {
	if s.audit == nil {
		return
	}
	details := map[string]interface{}{"reason": reason}
	if cn != "" {
		details["common_name"] = cn
	}
	if err := s.audit.Log(r.Context(), audit.EventOperatorCertReject, actor, details); err != nil {
		logAuditErr(false, "operator_cert_reject", err)
	}
}
