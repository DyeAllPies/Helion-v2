// internal/authz/authz_test.go
//
// Table-driven coverage of the Allow evaluator. Every row is
// one (principal, action, resource) triple with the expected
// outcome. Keep the table dense — a new policy rule should be a
// diff that adds rows, not a new test function.
//
// Negative cases come first (every deny code has at least one
// row), then the admin short-circuit, then the per-kind allow
// rows.

package authz_test

import (
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

type allowCase struct {
	name       string
	p          *principal.Principal
	action     authz.Action
	res        *authz.Resource
	wantAllow  bool
	wantCode   string // only meaningful when wantAllow is false
}

func TestAllow_Table(t *testing.T) {
	alice := principal.User("alice", "user")
	bob := principal.User("bob", "user")
	aliceAdmin := principal.User("root", "admin")
	opCarol := principal.Operator("carol@ops", "user")
	opAdmin := principal.Operator("rootop@ops", "admin")
	anon := principal.Anonymous()
	nodeGPU := principal.Node("gpu-01")
	svcRetry := principal.Service("retry_loop")
	svcDispatch := principal.Service("dispatcher")
	svcCoord := principal.Service("coordinator")
	svcUnknown := principal.Service("custom_unknown_service")
	jobTok := principal.Job("wf-42")

	aliceJob := authz.JobResource("j-1", "user:alice", "")
	aliceWf := authz.WorkflowResource("wf-1", "user:alice")
	aliceDataset := authz.DatasetResource("ds/v1", "user:alice")
	legacyJob := authz.JobResource("j-legacy", principal.LegacyOwnerID, "")
	jobInWf42 := authz.JobResource("wf-42/a", "user:alice", "wf-42")
	jobInWfOther := authz.JobResource("wf-99/a", "user:alice", "wf-99")

	cases := []allowCase{
		// ── Nil + defensive ──
		{"nil principal denied", nil, authz.ActionRead, aliceJob, false, authz.DenyCodeNilPrincipal},
		{"nil resource denied", alice, authz.ActionRead, nil, false, authz.DenyCodeNilResource},

		// ── Admin short-circuit (rule 2) ──
		{"user admin reads others' job", aliceAdmin, authz.ActionRead, aliceJob, true, ""},
		{"user admin deletes dataset", aliceAdmin, authz.ActionDelete, aliceDataset, true, ""},
		{"user admin reveals secret", aliceAdmin, authz.ActionReveal, aliceJob, true, ""},
		{"user admin on system", aliceAdmin, authz.ActionAdmin, authz.SystemResource(), true, ""},
		{"operator admin reads others' wf", opAdmin, authz.ActionRead, aliceWf, true, ""},
		{"user admin on legacy-owned job (break-glass)", aliceAdmin, authz.ActionRead, legacyJob, true, ""},

		// ── Anonymous (rule 7) ──
		{"anonymous read denied", anon, authz.ActionRead, aliceJob, false, authz.DenyCodeAnonymous},
		{"anonymous on system denied", anon, authz.ActionAdmin, authz.SystemResource(), false, authz.DenyCodeAnonymous},

		// ── System resource requires admin ──
		{"non-admin user on system", alice, authz.ActionAdmin, authz.SystemResource(), false, authz.DenyCodeAdminRequired},
		{"non-admin operator on system", opCarol, authz.ActionAdmin, authz.SystemResource(), false, authz.DenyCodeAdminRequired},

		// ── Legacy sentinel (admin-only) ──
		{"non-admin user on legacy-owned denied", alice, authz.ActionRead, legacyJob, false, authz.DenyCodeLegacyOwner},
		{"service on legacy-owned denied", svcRetry, authz.ActionRead, legacyJob, false, authz.DenyCodeLegacyOwner},
		{"node on legacy-owned denied", nodeGPU, authz.ActionRead, legacyJob, false, authz.DenyCodeLegacyOwner},

		// ── Rule 3: node principals denied on REST actions ──
		{"node read job denied", nodeGPU, authz.ActionRead, aliceJob, false, authz.DenyCodeNodeNotAllowed},
		{"node write job denied", nodeGPU, authz.ActionWrite, aliceJob, false, authz.DenyCodeNodeNotAllowed},
		{"node list jobs denied", nodeGPU, authz.ActionList, aliceJob, false, authz.DenyCodeNodeNotAllowed},
		{"node delete dataset denied", nodeGPU, authz.ActionDelete, aliceDataset, false, authz.DenyCodeNodeNotAllowed},
		{"node reveal denied", nodeGPU, authz.ActionReveal, aliceJob, false, authz.DenyCodeNodeNotAllowed},

		// ── Rule 4: service principals ──
		{"retry_loop cancels job", svcRetry, authz.ActionCancel, aliceJob, true, ""},
		{"retry_loop reveal denied", svcRetry, authz.ActionReveal, aliceJob, false, authz.DenyCodeServiceNotAllowed},
		{"dispatcher read job", svcDispatch, authz.ActionRead, aliceJob, true, ""},
		{"dispatcher delete dataset denied", svcDispatch, authz.ActionDelete, aliceDataset, false, authz.DenyCodeServiceNotAllowed},
		{"coordinator service has no allowlist", svcCoord, authz.ActionRead, aliceJob, false, authz.DenyCodeServiceNotAllowed},
		{"unknown service name denied", svcUnknown, authz.ActionRead, aliceJob, false, authz.DenyCodeServiceNotAllowed},

		// ── Rule 5: job-scoped tokens ──
		{"job token reads job in its workflow", jobTok, authz.ActionRead, jobInWf42, true, ""},
		{"job token reads job in other workflow denied", jobTok, authz.ActionRead, jobInWfOther, false, authz.DenyCodeJobScopeMismatch},
		{"job token on standalone job denied", jobTok, authz.ActionRead, aliceJob, false, authz.DenyCodeJobScopeMismatch},
		{"job token on workflow resource denied", jobTok, authz.ActionRead, aliceWf, false, authz.DenyCodeJobScopeMismatch},
		{"job token cancel denied", jobTok, authz.ActionCancel, jobInWf42, false, authz.DenyCodeJobScopeMismatch},
		{"job token reveal denied", jobTok, authz.ActionReveal, jobInWf42, false, authz.DenyCodeJobScopeMismatch},

		// ── Rule 6: owner check for users/operators ──
		{"alice reads her own job", alice, authz.ActionRead, aliceJob, true, ""},
		{"alice cancels her own workflow", alice, authz.ActionCancel, aliceWf, true, ""},
		{"alice deletes her own dataset", alice, authz.ActionDelete, aliceDataset, true, ""},
		{"bob reads alice's job denied", bob, authz.ActionRead, aliceJob, false, authz.DenyCodeNotOwner},
		{"bob cancels alice's workflow denied", bob, authz.ActionCancel, aliceWf, false, authz.DenyCodeNotOwner},
		{"operator carol reads alice's job denied", opCarol, authz.ActionRead, aliceJob, false, authz.DenyCodeNotOwner},

		// ── Empty owner on non-system resource fails closed ──
		{"empty owner fails closed", alice, authz.ActionRead, &authz.Resource{Kind: authz.ResourceKindJob, ID: "j-?", OwnerPrincipal: ""}, false, authz.DenyCodeNotOwner},

		// ── Reveal: even the owner cannot reveal their own secret ──
		// Reveal is admin-only; the owner-check rule would pass it for
		// a user owner. Document current behaviour: the rule doesn't
		// carve out reveal today, so an owner CAN reveal their own
		// job secret. This matches the pre-feature-37 behaviour where
		// reveal-secret sat behind adminMiddleware only. We assert
		// the current behaviour here so a policy change is explicit.
		{"owner reveals own secret (allowed; admin-middleware remains on route)", alice, authz.ActionReveal, aliceJob, true, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := authz.Allow(c.p, c.action, c.res)
			if c.wantAllow {
				if err != nil {
					t.Fatalf("Allow: want nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Allow: want deny %q, got nil", c.wantCode)
			}
			var de *authz.DenyError
			if !errors.As(err, &de) {
				t.Fatalf("Allow: want *DenyError, got %T", err)
			}
			if de.Code != c.wantCode {
				t.Fatalf("Allow code = %q; want %q (reason=%s)", de.Code, c.wantCode, de.Reason)
			}
		})
	}
}

// TestAllow_UnknownKind_FailsClosed covers the defensive default
// branch for a future Principal.Kind that's added without a
// matching rule.
func TestAllow_UnknownKind_FailsClosed(t *testing.T) {
	// Fabricate a Principal with an unknown Kind by direct
	// struct construction (the exported constructors all
	// produce a known Kind; we bypass them here to exercise
	// the default branch).
	p := &principal.Principal{
		ID:   "weird:zzz",
		Kind: principal.Kind("weird"),
	}
	err := authz.Allow(p, authz.ActionRead, authz.JobResource("j-1", "user:alice", ""))
	if err == nil {
		t.Fatalf("want deny, got nil")
	}
	var de *authz.DenyError
	if !errors.As(err, &de) {
		t.Fatalf("want *DenyError, got %T", err)
	}
	if de.Code != authz.DenyCodeUnknownKind {
		t.Fatalf("code = %q; want %q", de.Code, authz.DenyCodeUnknownKind)
	}
}

// TestAllow_DenyErrorCarriesContext verifies the error payload
// is useful for audit. The evaluator is pure, so this is a
// shape test, not a behaviour test.
func TestAllow_DenyErrorCarriesContext(t *testing.T) {
	alice := principal.User("alice", "user")
	res := authz.JobResource("j-7", "user:bob", "")

	err := authz.Allow(alice, authz.ActionCancel, res)
	if err == nil {
		t.Fatalf("want deny, got nil")
	}
	var de *authz.DenyError
	if !errors.As(err, &de) {
		t.Fatalf("want *DenyError, got %T", err)
	}
	if de.Code != authz.DenyCodeNotOwner {
		t.Errorf("code = %q; want not_owner", de.Code)
	}
	if de.Action != authz.ActionCancel {
		t.Errorf("action = %q; want cancel", de.Action)
	}
	if de.Kind != string(principal.KindUser) {
		t.Errorf("kind = %q; want %q", de.Kind, principal.KindUser)
	}
	if de.ResourceKind != authz.ResourceKindJob {
		t.Errorf("resource kind = %q; want %q", de.ResourceKind, authz.ResourceKindJob)
	}
}
