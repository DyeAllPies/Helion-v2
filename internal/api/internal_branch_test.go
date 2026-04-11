// internal/api/internal_branch_test.go
//
// Package-internal tests that reach helpers and fields the external
// api_test package cannot see (logAuditErr, upgrader.CheckOrigin, etc.).

package api

import (
	"errors"
	"net/http/httptest"
	"testing"
)

// ── logAuditErr ───────────────────────────────────────────────────────────────
// logAuditErr is a leaf logging helper — there is no return value, so this
// test just documents that both branches execute without panicking.

func TestLogAuditErr_CriticalBranch(t *testing.T) {
	logAuditErr(true, "auth.missing_bearer", errors.New("store down"))
}

func TestLogAuditErr_NonCriticalBranch(t *testing.T) {
	logAuditErr(false, "job.submit", errors.New("store down"))
}

// ── upgrader.CheckOrigin ──────────────────────────────────────────────────────
// The websocket.Upgrader.CheckOrigin callback configured in NewServer is not
// reached via plain HTTP tests because httptest.NewRequest never sets an
// Origin header. Here we invoke it directly on a fresh Server instance to
// hit all three branches: empty, parsed host == request host, and mismatch.

func TestUpgraderCheckOrigin_EmptyOrigin_Allowed(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "http://coordinator.internal/ws/metrics", nil)
	req.Host = "coordinator.internal"
	// No Origin header.
	if !s.upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin should allow empty Origin")
	}
}

func TestUpgraderCheckOrigin_SameHost_Allowed(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "http://coordinator.internal/ws/metrics", nil)
	req.Host = "coordinator.internal"
	req.Header.Set("Origin", "https://coordinator.internal")
	if !s.upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin should allow same-host Origin")
	}
}

func TestUpgraderCheckOrigin_DifferentHost_Rejected(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "http://coordinator.internal/ws/metrics", nil)
	req.Host = "coordinator.internal"
	req.Header.Set("Origin", "https://evil.example.com")
	if s.upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin should reject cross-origin request")
	}
}

func TestUpgraderCheckOrigin_MalformedOrigin_Rejected(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "http://coordinator.internal/ws/metrics", nil)
	req.Host = "coordinator.internal"
	// url.Parse rejects whitespace in the scheme portion.
	req.Header.Set("Origin", "ht tp://\x7f")
	if s.upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin should reject unparseable Origin")
	}
}
