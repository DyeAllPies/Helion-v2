// internal/principal/principal_test.go

package principal_test

import (
	"context"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// ── ID formats ─────────────────────────────────────────────────────────────

func TestPrincipal_IDFormats_AllKindsPrefixed(t *testing.T) {
	cases := []struct {
		name string
		p    *principal.Principal
		want string
	}{
		{"user", principal.User("alice", "user"), "user:alice"},
		{"user admin", principal.User("root", "admin"), "user:root"},
		{"operator", principal.Operator("alice@ops", "admin"), "operator:alice@ops"},
		{"node", principal.Node("gpu-01"), "node:gpu-01"},
		{"service", principal.Service("dispatcher"), "service:dispatcher"},
		{"job", principal.Job("wf-42"), "job:wf-42"},
		{"anonymous singleton", principal.Anonymous(), "anonymous"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.p.ID != c.want {
				t.Errorf("ID = %q, want %q", c.p.ID, c.want)
			}
		})
	}
}

func TestPrincipal_IDKindConsistency(t *testing.T) {
	// Invariant: ID's prefix matches Kind's prefix mapping.
	// Regression guard: a future edit to Kind's prefix table
	// without touching the constructors would desync the two.
	cases := []*principal.Principal{
		principal.User("alice", "user"),
		principal.Operator("alice@ops", "admin"),
		principal.Node("gpu-01"),
		principal.Service("dispatcher"),
		principal.Job("wf-42"),
	}
	for _, p := range cases {
		k, _, err := principal.ParseID(p.ID)
		if err != nil {
			t.Errorf("ParseID(%q): unexpected err %v", p.ID, err)
			continue
		}
		if k != p.Kind {
			t.Errorf("ID %q parses to kind %q but Principal.Kind = %q",
				p.ID, k, p.Kind)
		}
	}
}

// ── Anonymous singleton ────────────────────────────────────────────────────

func TestPrincipal_Anonymous_Singleton(t *testing.T) {
	a := principal.Anonymous()
	b := principal.Anonymous()
	if a != b {
		t.Errorf("Anonymous is not a singleton: a=%p b=%p", a, b)
	}
	if a.Kind != principal.KindAnonymous {
		t.Errorf("Kind = %q, want %q", a.Kind, principal.KindAnonymous)
	}
	if a.ID != "anonymous" {
		t.Errorf("ID = %q, want 'anonymous'", a.ID)
	}
	// Regression guard: the singleton must not have a role;
	// feature 37 denies anonymous and relies on Role being
	// empty so an accidental role set would be noticeable.
	if a.Role != "" {
		t.Errorf("Role = %q, want empty", a.Role)
	}
}

// ── Context plumbing ───────────────────────────────────────────────────────

func TestFromContext_NeverReturnsNil(t *testing.T) {
	// Explicit expectation from the feature 35 spec.
	p := principal.FromContext(context.Background())
	if p == nil {
		t.Fatal("FromContext(bg) returned nil — must be Anonymous()")
	}
	if p.Kind != principal.KindAnonymous {
		t.Errorf("Kind = %q, want %q", p.Kind, principal.KindAnonymous)
	}
}

func TestFromContext_NilContext(t *testing.T) {
	// Handlers shouldn't pass nil but defensive coding: FromContext
	// handles it without panic.
	var nilCtx context.Context //nolint:staticcheck // intentional: testing nil-ctx defence
	p := principal.FromContext(nilCtx)
	if p == nil || p.Kind != principal.KindAnonymous {
		t.Errorf("FromContext(nil): want Anonymous, got %+v", p)
	}
}

func TestNewContext_RoundTrip(t *testing.T) {
	alice := principal.User("alice", "admin")
	ctx := principal.NewContext(context.Background(), alice)
	got := principal.FromContext(ctx)
	if got != alice {
		t.Errorf("round-trip: got %+v, want %+v", got, alice)
	}
}

func TestNewContext_NilPrincipal_StoresAnonymous(t *testing.T) {
	// Defensive: a caller who accidentally stores nil must not
	// break FromContext's never-nil contract.
	ctx := principal.NewContext(context.Background(), nil)
	got := principal.FromContext(ctx)
	if got.Kind != principal.KindAnonymous {
		t.Errorf("nil store: want Anonymous, got %+v", got)
	}
}

func TestNewContext_OverwriteInChild(t *testing.T) {
	// Feature 27 + feature 35 will both stamp a Principal: cert
	// middleware runs before authMiddleware. Ensure child-context
	// semantics let a later middleware override an earlier one.
	parent := principal.NewContext(context.Background(),
		principal.User("alice", "user"))
	child := principal.NewContext(parent,
		principal.Operator("alice@ops", "user"))
	if principal.FromContext(child).Kind != principal.KindOperator {
		t.Error("child context should see the later Principal")
	}
	if principal.FromContext(parent).Kind != principal.KindUser {
		t.Error("parent context should retain its own Principal")
	}
}

// ── ParseID ────────────────────────────────────────────────────────────────

func TestParseID_ValidKinds(t *testing.T) {
	cases := map[string]struct {
		kind   principal.Kind
		suffix string
	}{
		"user:alice":             {principal.KindUser, "alice"},
		"operator:alice@ops":     {principal.KindOperator, "alice@ops"},
		"node:gpu-01":            {principal.KindNode, "gpu-01"},
		"service:dispatcher":     {principal.KindService, "dispatcher"},
		"job:wf-42":              {principal.KindJob, "wf-42"},
		"anonymous":              {principal.KindAnonymous, ""},
	}
	for id, want := range cases {
		k, suffix, err := principal.ParseID(id)
		if err != nil {
			t.Errorf("ParseID(%q): err %v", id, err)
			continue
		}
		if k != want.kind || suffix != want.suffix {
			t.Errorf("ParseID(%q) = (%q, %q), want (%q, %q)",
				id, k, suffix, want.kind, want.suffix)
		}
	}
}

func TestParseID_UnknownKindRejected(t *testing.T) {
	cases := []string{
		"unknown:foo",
		"evil:alice",
		"adminOnly:alice", // no such kind
	}
	for _, id := range cases {
		if _, _, err := principal.ParseID(id); err == nil {
			t.Errorf("ParseID(%q): expected err for unknown kind", id)
		}
	}
}

func TestParseID_MissingPrefix(t *testing.T) {
	cases := []string{
		"alice",         // no prefix at all
		":alice",        // empty kind
		"",              // empty id
	}
	for _, id := range cases {
		if _, _, err := principal.ParseID(id); err == nil {
			t.Errorf("ParseID(%q): expected err for missing/empty prefix", id)
		}
	}
}

// ── IsAdmin ────────────────────────────────────────────────────────────────

func TestPrincipal_IsAdmin(t *testing.T) {
	cases := []struct {
		name string
		p    *principal.Principal
		want bool
	}{
		{"user admin", principal.User("root", "admin"), true},
		{"user non-admin", principal.User("alice", "user"), false},
		{"operator admin", principal.Operator("alice@ops", "admin"), true},
		{"operator non-admin", principal.Operator("alice@ops", "user"), false},
		{"node never admin", principal.Node("gpu-01"), false},
		{"service never admin", principal.Service("dispatcher"), false},
		{"job never admin", principal.Job("wf-42"), false},
		{"anonymous never admin", principal.Anonymous(), false},
		{"nil never admin", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.IsAdmin(); got != c.want {
				t.Errorf("IsAdmin = %v, want %v", got, c.want)
			}
		})
	}
}

// ── Node-never-admin regression guard ─────────────────────────────────────

func TestPrincipal_NodeWithAdminRole_StillNotAdmin(t *testing.T) {
	// Defensive: Node constructor doesn't set Role, but if a
	// future code path ever did, IsAdmin must still refuse.
	// This is the load-bearing defence against a compromised
	// node pretending to be admin on the REST surface.
	n := principal.Node("gpu-01")
	n.Role = "admin" // simulate a mistaken edit
	if n.IsAdmin() {
		t.Error("node with Role=admin must never report IsAdmin=true")
	}
}

func TestPrincipal_ServiceWithAdminRole_StillNotAdmin(t *testing.T) {
	s := principal.Service("dispatcher")
	s.Role = "admin"
	if s.IsAdmin() {
		t.Error("service with Role=admin must never report IsAdmin=true")
	}
}

// ── Package-level service principals ───────────────────────────────────────

func TestServicePrincipals_Consistent(t *testing.T) {
	// The package exports a handful of pre-constructed service
	// principals. Verify they are correctly shaped — a typo in
	// the service name would desync audit events from the
	// dashboard filter.
	cases := map[*principal.Principal]string{
		principal.ServiceDispatcher:     "service:dispatcher",
		principal.ServiceWorkflowRunner: "service:workflow_runner",
		principal.ServiceRetryLoop:      "service:retry_loop",
		principal.ServiceLogIngester:    "service:log_ingester",
		principal.ServiceRetention:      "service:retention",
		principal.ServiceLogReconciler:  "service:log_reconciler",
	}
	for p, wantID := range cases {
		if p.ID != wantID {
			t.Errorf("%+v: ID = %q, want %q", p, p.ID, wantID)
		}
		if p.Kind != principal.KindService {
			t.Errorf("%+v: Kind = %q, want %q", p, p.Kind, principal.KindService)
		}
	}
}

// ── Defensive: weird characters in subjects ────────────────────────────────

func TestPrincipal_SubjectWithColon(t *testing.T) {
	// A subject like "user:alice" containing a colon itself could
	// confuse ParseID — it would misparse the suffix. Current
	// behaviour: ParseID splits on the FIRST colon, so
	// "user:evil:alice" parses to kind=user, suffix="evil:alice"
	// which is arguably correct. Document the behaviour with a
	// test so a future change doesn't silently regress it.
	p := principal.User("evil:alice", "user")
	k, suffix, err := principal.ParseID(p.ID)
	if err != nil {
		t.Fatalf("ParseID: %v", err)
	}
	if k != principal.KindUser {
		t.Errorf("kind = %q, want %q", k, principal.KindUser)
	}
	if suffix != "evil:alice" {
		t.Errorf("suffix = %q, want 'evil:alice'", suffix)
	}
}
