// internal/authz/extras_test.go
//
// Coverage for the lesser-exercised helpers: ModelResource,
// ServiceResource, DenyError.Error, shareInvalidError.Error.
// Resource constructors are used by handlers but the tests
// in authz_test.go use the DatasetResource / JobResource
// / WorkflowResource constructors more heavily.

package authz_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
)

func TestModelResource_StampsKindAndShares(t *testing.T) {
	shares := []authz.Share{
		{Grantee: "user:bob", Actions: []authz.Action{authz.ActionRead}},
	}
	r := authz.ModelResource("m/v1", "user:alice", shares)
	if r == nil {
		t.Fatal("nil resource")
	}
	if r.Kind != authz.ResourceKindModel {
		t.Errorf("kind: got %q, want %q", r.Kind, authz.ResourceKindModel)
	}
	if r.OwnerPrincipal != "user:alice" {
		t.Errorf("owner: got %q", r.OwnerPrincipal)
	}
	if len(r.Shares) != 1 {
		t.Errorf("shares: got %d", len(r.Shares))
	}
}

func TestModelResource_NoShares_NilSlice(t *testing.T) {
	r := authz.ModelResource("m/v1", "user:alice")
	if len(r.Shares) != 0 {
		t.Errorf("no-shares variant: got %d entries", len(r.Shares))
	}
}

func TestServiceResource_StampsKind(t *testing.T) {
	r := authz.ServiceResource("svc-1", "user:alice")
	if r.Kind != authz.ResourceKindService {
		t.Errorf("kind: got %q, want %q", r.Kind, authz.ResourceKindService)
	}
}

func TestServiceResource_WithShares_Stamped(t *testing.T) {
	shares := []authz.Share{
		{Grantee: "group:ops", Actions: []authz.Action{authz.ActionRead}},
	}
	r := authz.ServiceResource("svc-1", "user:alice", shares)
	if len(r.Shares) != 1 {
		t.Errorf("shares: got %d", len(r.Shares))
	}
}

// ── DenyError.Error ─────────────────────────────────────────

func TestDenyError_ErrorFormat(t *testing.T) {
	// Produced by Allow on e.g. an anonymous denial.
	e := &authz.DenyError{
		Code:   authz.DenyCodeAnonymous,
		Reason: "anonymous cannot access job",
		Action: authz.ActionRead,
	}
	msg := e.Error()
	if !strings.Contains(msg, "authz:") {
		t.Errorf("missing authz prefix: %q", msg)
	}
	if !strings.Contains(msg, authz.DenyCodeAnonymous) {
		t.Errorf("missing code: %q", msg)
	}
	if !strings.Contains(msg, "anonymous cannot access") {
		t.Errorf("missing reason: %q", msg)
	}
}

// ── shareInvalidError.Error ─────────────────────────────────

func TestValidateShare_InvalidError_ErrorFormat(t *testing.T) {
	// ValidateShare returns a shareInvalidError that wraps
	// ErrShareInvalid. Calling err.Error() on the returned
	// value exercises the Error() method (currently 0% —
	// Unwrap() is covered via errors.Is but Error() was not).
	cases := []authz.Share{
		{Grantee: "", Actions: []authz.Action{authz.ActionRead}},
		{Grantee: "anonymous:x", Actions: []authz.Action{authz.ActionRead}},
		{Grantee: "user:", Actions: []authz.Action{authz.ActionRead}},
		{Grantee: "weirdkind:x", Actions: []authz.Action{authz.ActionRead}},
		{Grantee: "user:bob", Actions: nil},
		{Grantee: "user:bob", Actions: []authz.Action{"unknown_action"}},
	}
	for _, sh := range cases {
		err := authz.ValidateShare(sh)
		if err == nil {
			t.Errorf("ValidateShare(%+v): want error", sh)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "authz: invalid share:") {
			t.Errorf("Error() format: got %q, want 'authz: invalid share:' prefix", msg)
		}
		if !errors.Is(err, authz.ErrShareInvalid) {
			t.Errorf("errors.Is(ErrShareInvalid): want true, got false for %+v", sh)
		}
	}
}
