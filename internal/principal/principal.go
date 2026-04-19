// Package principal is the feature-35 unified identity model for
// the Helion coordinator.
//
// Background
// ──────────
// Before feature 35, Helion identified "who is acting" four
// different ways:
//
//   - HTTP handlers read `claims.Subject` from the JWT.
//   - gRPC handlers read `nodeID` from the registered certificate.
//   - Feature 27's clientCertMiddleware stored an operator CN as
//     a separate string in the request context.
//   - Coordinator-internal actions (dispatch loop, workflow
//     runner, retry loop, log ingester, retention cron) labelled
//     themselves `"system"` in audit events.
//
// Every downstream consumer had to know which string shape it was
// looking at. An audit row with `actor=alice` could be a user, an
// operator cert CN, or a node ID — the type system did not tell
// them apart, and a collision (node registered as "alice") would
// have silently passed the `claims.Subject == job.SubmittedBy`
// RBAC check in handleGetJob.
//
// This package gives every acting identity a single typed shape:
// a `*Principal`. The value is resolved once at the edge of the
// coordinator (authMiddleware for REST, nodePrincipal for gRPC,
// `Service(name)` for coordinator-internal goroutines) and
// threaded through the request context. Later IAM slices
// (features 36–38) consult the Principal rather than the bare
// subject string.
//
// Design invariants
// ─────────────────
//
//  1. Principal IDs are prefixed with their Kind — `user:alice`,
//     `operator:alice@ops`, `node:gpu-01`, `service:dispatcher`,
//     `job:wf-42`, `anonymous`. A collision between a user
//     subject and a node ID is impossible: their IDs differ in
//     the prefix even when the suffix matches.
//
//  2. `FromContext` never returns nil. A missing Principal reads
//     back as `Anonymous()` so handlers can treat the return
//     value without a nil check.
//
//  3. Resolution is pure. Constructors do not allocate storage,
//     read persistence, or touch the network. Resolution that
//     requires side effects (e.g. looking up group membership
//     in feature 38) lives outside this package and enriches the
//     Principal after construction.
//
//  4. The zero value of `Kind` is intentional garbage
//     (empty string). Callers must use the exported constructors
//     or the `Anonymous()` / `Service(name)` helpers — there is
//     no valid zero-valued Principal.
package principal

import (
	"context"
	"fmt"
	"strings"
)

// Kind names the family of a Principal — how the coordinator
// learned about the identity, NOT what role it has.
//
// Role (admin / user / node / job in JWT terms) is a separate
// concept carried on the Principal struct and consumed by the
// authz evaluator in feature 37. A single Kind can host multiple
// roles; KindUser with Role="admin" and KindUser with Role="user"
// are the same kind of principal with different authorities.
type Kind string

const (
	// KindUser is a human operator authenticated via JWT alone.
	// Common in dev clusters; production deployments should
	// prefer KindOperator by layering mTLS (feature 27).
	KindUser Kind = "user"

	// KindOperator is a human operator authenticated via a
	// verified client certificate (feature 27). A KindOperator
	// principal always also has a JWT — the cert CN is the
	// primary ID; the JWT role + subject ride along for authz
	// + audit context.
	KindOperator Kind = "operator"

	// KindNode is a registered worker node authenticated via
	// mTLS using its coordinator-issued certificate. Node
	// principals are denied on REST actions by feature 37's
	// policy; they may only act on node-facing gRPC surfaces.
	KindNode Kind = "node"

	// KindService is a coordinator-internal actor — the
	// dispatch loop, workflow runner, retry loop, log
	// ingester, analytics retention cron, etc. Service
	// principals replace the old literal "system" actor
	// string. Their authority is scoped by service name.
	KindService Kind = "service"

	// KindJob is the workflow-scoped token minted by submit.py
	// per workflow (feature 19 and related). JobRole tokens
	// are short-lived and can only act on jobs belonging to
	// the same workflow.
	KindJob Kind = "job"

	// KindAnonymous is the dev-mode fallback when auth is
	// disabled (srv.DisableAuth). Feature 37 denies every
	// non-trivial action for KindAnonymous principals.
	KindAnonymous Kind = "anonymous"
)

// prefix maps Kind → the ID prefix used when composing a
// Principal ID. Keep in sync with the IDs in Kind's constants
// above — a mismatch would let a hostile ID synthesise a valid
// Principal. Tested in TestPrincipal_IDFormats.
var prefix = map[Kind]string{
	KindUser:     "user:",
	KindOperator: "operator:",
	KindNode:     "node:",
	KindService:  "service:",
	KindJob:      "job:",
	// KindAnonymous is the exact literal "anonymous" — no ':'
	// separator because it has no suffix. See Anonymous().
}

// Principal is the unified identity carried on every request
// after feature 35. Always accessed via a pointer; never use
// the zero value.
type Principal struct {
	// ID is the globally-unique, prefix-qualified handle. Read
	// by authz (feature 37) and by ownership comparisons
	// (feature 36). Never mutate after construction.
	ID string

	// Kind is the derived enum; stored redundantly for
	// zero-allocation fast-path comparisons in the hot authz
	// path. Kept in sync with ID's prefix — `TestPrincipal_IDKindConsistency`
	// guards the invariant.
	Kind Kind

	// DisplayName is the raw human-friendly suffix (e.g.
	// "alice", "gpu-01"). UI-only; never used for identity
	// decisions.
	DisplayName string

	// Role is the JWT role claim for Kind ∈ {user, operator,
	// job} principals. Empty for Kind ∈ {node, service,
	// anonymous} — their authority is derived from Kind, not
	// role.
	//
	// Feature 37's policy evaluator short-circuits on
	// Role="admin" for User/Operator kinds. Other values
	// flow through the per-Kind rules.
	Role string

	// Groups names the groups this principal belongs to.
	// Reserved for feature 38; nil / empty in features 35–37.
	// Exposed now so later slices don't need a principal-shape
	// migration.
	Groups []string
}

// ── Constructors ───────────────────────────────────────────────────────────

// User creates a KindUser principal from a JWT subject and role.
// role is the raw JWT role claim ("admin", "user", …).
func User(subject, role string) *Principal {
	return &Principal{
		ID:          prefix[KindUser] + subject,
		Kind:        KindUser,
		DisplayName: subject,
		Role:        role,
	}
}

// Operator creates a KindOperator principal from a verified
// client certificate CN (feature 27). role is the JWT role
// claim carried alongside the cert for authz decisions.
func Operator(cn, role string) *Principal {
	return &Principal{
		ID:          prefix[KindOperator] + cn,
		Kind:        KindOperator,
		DisplayName: cn,
		Role:        role,
	}
}

// Node creates a KindNode principal from a registered node ID.
// The node ID is trusted because the coordinator verified the
// node's mTLS certificate chain to its own CA before this
// constructor is called — never construct a Node principal
// from unverified input.
func Node(nodeID string) *Principal {
	return &Principal{
		ID:          prefix[KindNode] + nodeID,
		Kind:        KindNode,
		DisplayName: nodeID,
	}
}

// Service creates a KindService principal for a coordinator-
// internal actor. name is the service name ("dispatcher",
// "workflow_runner", "retry_loop", "log_ingester",
// "retention"). Service principals are typically package-level
// variables; see the ServiceXxx values exported by this package.
func Service(name string) *Principal {
	return &Principal{
		ID:          prefix[KindService] + name,
		Kind:        KindService,
		DisplayName: name,
	}
}

// Job creates a KindJob principal for a workflow-scoped token.
// subject is the JWT subject (typically the workflow ID, per
// submit.py's minting convention).
func Job(subject string) *Principal {
	return &Principal{
		ID:          prefix[KindJob] + subject,
		Kind:        KindJob,
		DisplayName: subject,
		Role:        "job",
	}
}

// anonymousSingleton is the singleton value returned by
// Anonymous(). See Anonymous() for the rationale.
var anonymousSingleton = &Principal{
	ID:          "anonymous",
	Kind:        KindAnonymous,
	DisplayName: "anonymous",
}

// Anonymous returns the singleton anonymous Principal.
//
// Returning the singleton means a caller who accidentally
// mutates the returned Principal would leak into every future
// Anonymous() call. The Principal struct is documented as
// immutable after construction — mutation is already a
// contract violation. The singleton shape lets the hot path
// avoid a one-allocation-per-request cost on auth-disabled
// dev builds.
func Anonymous() *Principal {
	return anonymousSingleton
}

// ── Coordinator-internal service principals ───────────────────────────────────
//
// Package-level variables so call sites that today hard-code
// the literal "system" actor string have a drop-in replacement.
// Kind is KindService; DisplayName is the service name so the
// dashboard audit view can render it directly.
//
// A service name added here is a wire-stable value — the
// analytics auth-events panel and the audit log filter on it.
// Renaming is a schema change.
var (
	// ServiceDispatcher is the principal for the job-dispatch
	// loop in internal/cluster/dispatch.go.
	ServiceDispatcher = Service("dispatcher")

	// ServiceWorkflowRunner is the principal for the workflow
	// state machine + Start/Cancel transitions.
	ServiceWorkflowRunner = Service("workflow_runner")

	// ServiceRetryLoop is the principal for feature-02 job
	// retries.
	ServiceRetryLoop = Service("retry_loop")

	// ServiceLogIngester is the principal for gRPC-side log
	// chunk persistence (StreamLogs handler).
	ServiceLogIngester = Service("log_ingester")

	// ServiceRetention is the principal for the feature-28
	// analytics retention cron.
	ServiceRetention = Service("retention")

	// ServiceLogReconciler is the principal for the feature-28
	// Badger→PG log reconciler (follow-up to the retention
	// commit).
	ServiceLogReconciler = Service("log_reconciler")

	// ServiceCoordinator is the generic "coordinator itself"
	// principal — used by lifecycle events (start/stop) and by
	// non-request-scoped audit calls that don't fit one of the
	// more specific loop principals above. Prefer the specific
	// one (Dispatcher, WorkflowRunner, etc.) when the calling
	// code runs inside one of those loops.
	ServiceCoordinator = Service("coordinator")
)

// ── Context plumbing ───────────────────────────────────────────────────────

// principalKey is the context-value key. A struct type so it
// cannot collide with any string-keyed value set by a third
// party.
type principalKey struct{}

// NewContext returns a child context carrying p. A nil p is
// replaced with Anonymous() so FromContext never observes nil.
func NewContext(parent context.Context, p *Principal) context.Context {
	if p == nil {
		p = Anonymous()
	}
	return context.WithValue(parent, principalKey{}, p)
}

// FromContext returns the Principal stored in ctx, or
// Anonymous() if none is stored.
//
// The function never returns nil. Handlers can safely write:
//
//	p := principal.FromContext(ctx)
//	if p.Kind != principal.KindAnonymous { ... }
//
// without a nil check.
func FromContext(ctx context.Context) *Principal {
	if ctx == nil {
		return Anonymous()
	}
	if v, ok := ctx.Value(principalKey{}).(*Principal); ok && v != nil {
		return v
	}
	return Anonymous()
}

// ── ID parsing ─────────────────────────────────────────────────────────────

// ParseID splits an ID into its Kind and suffix. Useful for
// tests that construct IDs by hand and for the audit-log
// dashboard's "filter by kind" behaviour.
//
// For the literal "anonymous" ID, returns (KindAnonymous, "",
// nil) — no suffix.
func ParseID(id string) (Kind, string, error) {
	if id == "anonymous" {
		return KindAnonymous, "", nil
	}
	colon := strings.IndexByte(id, ':')
	if colon <= 0 {
		return "", "", fmt.Errorf("principal: id %q is not prefixed with a Kind", id)
	}
	k := Kind(id[:colon])
	if _, ok := prefix[k]; !ok {
		return "", "", fmt.Errorf("principal: unknown kind %q in id %q", k, id)
	}
	return k, id[colon+1:], nil
}

// IsAdmin returns true iff the Principal is authorised for
// admin actions. Kept here (rather than inlined at every call
// site) so the admin rule has exactly one definition and a
// future change (e.g. admin-via-group from feature 38) lands
// in one place.
//
// Today: a User or Operator kind with Role="admin" is admin.
// Nothing else is. In particular:
//
//   - Kind=node is NEVER admin even if someone contrived a
//     JWT with Role=admin attached; node mTLS identity is
//     authoritative for nodes and the node role is what it
//     is.
//   - Kind=service is NEVER admin. Service principals have
//     their authority scoped per action by feature 37.
//   - Kind=anonymous is NEVER admin.
func (p *Principal) IsAdmin() bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case KindUser, KindOperator:
		return p.Role == "admin"
	default:
		return false
	}
}
