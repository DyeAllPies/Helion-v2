// Package authz is the feature-37 authorization engine for the
// Helion coordinator.
//
// Role
// ────
// Feature 35 answers "who is this?" (the Principal). Feature 36
// answers "who owns this?" (OwnerPrincipal on every stateful
// resource). Feature 37 answers the remaining question: "is
// this Principal permitted to perform this Action on this
// Resource?"
//
// Every HTTP handler and every coordinator-internal caller that
// mutates or reads a resource funnels the decision through one
// function: `Allow(p, action, res)`. A nil return means permitted;
// any other return is a `*DenyError` carrying a stable
// machine-readable `Code` a dashboard can localise.
//
// Safety properties
// ─────────────────
//
//  1. Pure function. No I/O, no persistence, no locks. Allow is a
//     deterministic function of (p, action, res). Threadsafe.
//     A policy bug is a code change plus a code review, never a
//     runtime configuration surprise.
//
//  2. Fail closed. Unknown Kind, unknown Action, nil Principal,
//     nil Resource, `legacy:` owner sentinel — every unexpected
//     input path denies. There is no "default allow" anywhere in
//     this package.
//
//  3. Admin is the only break-glass. A `Kind=user` or
//     `Kind=operator` Principal with `Role=admin` is allowed every
//     Action on every Resource. Feature 38 will expose group
//     membership to narrow this, but the v1 rule is deliberately
//     simple: admin means admin.
//
//  4. Node principals NEVER authorise on REST actions. A
//     compromised node's mTLS identity cannot stand up a fake job,
//     read a user-owned workflow, or mutate a dataset. Node
//     principals are permitted ONLY on the internal gRPC-surface
//     actions the coordinator dispatches to (dispatch ack, log
//     stream, service-event report) — and those are evaluated
//     via the same Allow call, just with internal Action names.
//
//  5. Service principals are scoped per action. The retry loop
//     can cancel a job it's retrying but not reveal its secrets;
//     the dispatcher can transition jobs but not delete datasets.
//     The per-service action table lives in rules.go and is
//     read during review as a compile-time artifact.
//
//  6. Anonymous denied everywhere. `Kind=anonymous` (dev-mode when
//     `Server.DisableAuth` is set) cannot pass any Allow check.
//     Dev builds that want to exercise handlers under an anonymous
//     principal must either disable the middleware or stamp a
//     different Principal in context — there is no anonymous
//     bypass inside the evaluator.
//
// Legacy resources
// ────────────────
// Resources whose `OwnerPrincipal` is `principal.LegacyOwnerID`
// (the `"legacy:"` sentinel from feature 36's load-time backfill)
// are authorised admin-only. Feature 37 inherits the same
// fail-closed behaviour the pre-feature-36 AUDIT L1 check gave
// for empty SubmittedBy: a legacy record without a recoverable
// owner is admin-only, no matter what Principal asks for it.
package authz

import (
	"fmt"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// Action names what is being attempted on a Resource.
//
// Keep this list small. A new action is a policy change — it
// requires a row in the per-kind rules table (rules.go) and a
// code review. New actions that silently fall through the
// switch in rulesFor*() deny at the evaluator level, but the
// per-kind table is where policy authors declare "yes, this
// kind may do this".
type Action string

const (
	// ActionRead is GET-a-single-resource.
	ActionRead Action = "read"
	// ActionList is GET-a-list-of-resources; handlers are
	// expected to filter with per-row Allow(ActionRead) before
	// returning the window.
	ActionList Action = "list"
	// ActionWrite is POST-a-new-resource (create).
	ActionWrite Action = "write"
	// ActionCancel is a user-initiated abort of a running
	// resource (POST /jobs/{id}/cancel, DELETE /workflows/{id}).
	ActionCancel Action = "cancel"
	// ActionDelete is hard-delete of a registry entry
	// (DELETE /api/datasets/..., DELETE /api/models/...).
	ActionDelete Action = "delete"
	// ActionReveal is the admin-only secret-reveal endpoint.
	// Distinct from ActionRead so service principals can read
	// redacted jobs but never the plaintext values.
	ActionReveal Action = "reveal"
	// ActionAdmin is reserved for system-wide admin endpoints
	// (token issue/revoke, node revoke, operator-cert issue).
	// Evaluated against ResourceSystem; the admin short-circuit
	// in rule 2 makes this a plain "is p admin" check.
	ActionAdmin Action = "admin"
)

// ResourceKind is the per-resource compile-time constant the
// caller passes into Allow. Stable wire-visible strings so audit
// events carry a consistent `resource_kind`.
type ResourceKind string

const (
	ResourceKindJob      ResourceKind = "job"
	ResourceKindWorkflow ResourceKind = "workflow"
	ResourceKindDataset  ResourceKind = "dataset"
	ResourceKindModel    ResourceKind = "model"
	ResourceKindService  ResourceKind = "service"
	// ResourceKindSystem is the marker for coordinator-wide
	// actions (revoke a node, issue a token). Kind=system
	// resources have no owner; ActionAdmin is the only valid
	// pairing.
	ResourceKindSystem ResourceKind = "system"
)

// Resource names what is being accessed. Handlers construct
// this after loading the resource from the store and before
// calling Allow — the evaluator needs OwnerPrincipal to make
// the decision.
type Resource struct {
	Kind ResourceKind
	// ID identifies the specific instance (job ID, workflow ID,
	// "dataset/<name>/<version>"). Empty for ResourceKindSystem.
	// Used purely for audit detail + DenyError context — the
	// evaluator makes decisions on Kind + OwnerPrincipal.
	ID string
	// OwnerPrincipal is the feature-36 stamp. Empty for
	// ResourceKindSystem; otherwise required — an empty owner
	// on a non-system resource is treated as if the owner were
	// the legacy sentinel (admin-only).
	OwnerPrincipal string
	// WorkflowID is populated only on ResourceKindJob resources
	// that belong to a workflow. Used by rule 5 (job-role
	// principals can access jobs that belong to the same
	// workflow). Empty for standalone jobs.
	WorkflowID string

	// Shares is the feature-38 grant list attached to this
	// resource. Evaluated by rule 6b in Allow — a grantee
	// whose principal (or group membership) matches a share
	// and whose action is in the share's Actions list is
	// permitted even when the owner check fails.
	//
	// Always nil on ResourceKindSystem (system resources are
	// admin-only by construction). May be nil or empty on
	// user-owned resources — the evaluator treats nil/empty
	// exactly the same as "no shares".
	Shares []Share
}

// SystemResource returns the singleton Resource for
// coordinator-wide admin actions.
func SystemResource() *Resource {
	return &systemResourceSingleton
}

var systemResourceSingleton = Resource{Kind: ResourceKindSystem}

// JobResource builds a Resource for a cluster job. owner is the
// Job's OwnerPrincipal; workflowID is empty for standalone
// jobs; shares is the feature-38 share list (nil ok).
func JobResource(id, owner, workflowID string, shares ...[]Share) *Resource {
	r := &Resource{
		Kind:           ResourceKindJob,
		ID:             id,
		OwnerPrincipal: owner,
		WorkflowID:     workflowID,
	}
	if len(shares) > 0 {
		r.Shares = shares[0]
	}
	return r
}

// WorkflowResource builds a Resource for a workflow. shares is
// the feature-38 share list (nil ok).
func WorkflowResource(id, owner string, shares ...[]Share) *Resource {
	r := &Resource{
		Kind:           ResourceKindWorkflow,
		ID:             id,
		OwnerPrincipal: owner,
	}
	if len(shares) > 0 {
		r.Shares = shares[0]
	}
	return r
}

// DatasetResource builds a Resource for a registry dataset.
func DatasetResource(id, owner string, shares ...[]Share) *Resource {
	r := &Resource{
		Kind:           ResourceKindDataset,
		ID:             id,
		OwnerPrincipal: owner,
	}
	if len(shares) > 0 {
		r.Shares = shares[0]
	}
	return r
}

// ModelResource builds a Resource for a registry model.
func ModelResource(id, owner string, shares ...[]Share) *Resource {
	r := &Resource{
		Kind:           ResourceKindModel,
		ID:             id,
		OwnerPrincipal: owner,
	}
	if len(shares) > 0 {
		r.Shares = shares[0]
	}
	return r
}

// ServiceResource builds a Resource for a ServiceEndpoint.
// owner is the owning job's OwnerPrincipal. Services inherit
// shares from their owning Job in the handler layer — the
// ServiceEndpoint itself doesn't carry its own share list.
func ServiceResource(id, owner string, shares ...[]Share) *Resource {
	r := &Resource{
		Kind:           ResourceKindService,
		ID:             id,
		OwnerPrincipal: owner,
	}
	if len(shares) > 0 {
		r.Shares = shares[0]
	}
	return r
}

// ── DenyError ───────────────────────────────────────────────

// DenyError is the only non-nil return shape from Allow. The
// Code field is a stable string the dashboard + tooling can
// key off; Reason is free-form for logs. New codes require a
// test + a review.
type DenyError struct {
	Code   string
	Reason string
	Action Action
	// Kind is the Kind of the Principal that was denied
	// (string form for audit detail). Empty if the deny came
	// from a nil Principal.
	Kind string
	// ResourceKind is the Kind of the Resource that was being
	// accessed. Empty if the deny came from a nil Resource.
	ResourceKind ResourceKind
}

func (e *DenyError) Error() string {
	return fmt.Sprintf("authz: %s: %s", e.Code, e.Reason)
}

// Deny codes. New codes here require a test case in
// authz_test.go and matching handling in the dashboard's
// error banner.
const (
	DenyCodeNilPrincipal       = "nil_principal"
	DenyCodeAnonymous          = "anonymous_denied"
	DenyCodeUnknownKind        = "unknown_kind"
	DenyCodeNotOwner           = "not_owner"
	DenyCodeLegacyOwner        = "legacy_owner_admin_only"
	DenyCodeNodeNotAllowed     = "node_not_allowed"
	DenyCodeServiceNotAllowed  = "service_not_allowed"
	DenyCodeJobScopeMismatch   = "job_scope_mismatch"
	DenyCodeAdminRequired      = "admin_required"
	DenyCodeNilResource        = "nil_resource"
	DenyCodeSystemNonAdmin     = "system_non_admin_action"
)

// denyf is the internal shortcut: build + return a *DenyError
// with a formatted reason, in one line. Keeps Allow readable.
func denyf(code string, action Action, p *principal.Principal, res *Resource, format string, args ...any) *DenyError {
	kind := ""
	if p != nil {
		kind = string(p.Kind)
	}
	var rk ResourceKind
	if res != nil {
		rk = res.Kind
	}
	return &DenyError{
		Code:         code,
		Reason:       fmt.Sprintf(format, args...),
		Action:       action,
		Kind:         kind,
		ResourceKind: rk,
	}
}

// ── Allow ───────────────────────────────────────────────────

// Allow returns nil iff p is permitted to perform action on
// res. Non-nil returns are always *DenyError.
//
// Rule precedence (documented in feature 37 spec):
//
//  1. nil Principal → deny (nil_principal).
//  2. KindUser or KindOperator with Role=admin → allow (break-glass).
//  3. KindNode → deny on every REST action; the node-internal
//     action allow-list in rules.go is consulted for service
//     Kind jobs.
//  4. KindService → per-service allow-list in rules.go.
//  5. KindJob → scope check against res.WorkflowID.
//  6. KindUser or KindOperator (non-admin) → allow iff
//     p.ID == res.OwnerPrincipal.
//  7. KindAnonymous → deny.
//  8. Everything else → deny (unknown_kind).
func Allow(p *principal.Principal, action Action, res *Resource) error {
	if p == nil {
		return denyf(DenyCodeNilPrincipal, action, nil, res,
			"nil principal cannot act")
	}
	if res == nil {
		return denyf(DenyCodeNilResource, action, p, nil,
			"nil resource; handler must construct Resource before Allow")
	}

	// Rule 2 — admin short-circuit. Admin-on-system is the
	// expected shape for admin endpoints; admin-on-owned-resources
	// is the break-glass ops path.
	if p.IsAdmin() {
		return nil
	}

	// Rule 7 — anonymous. Evaluated early so downstream rules can
	// assume they never see anonymous.
	if p.Kind == principal.KindAnonymous {
		return denyf(DenyCodeAnonymous, action, p, res,
			"anonymous principals are denied every action")
	}

	// System resources require admin. Rule 2 already handled
	// admin; anything reaching here is non-admin.
	if res.Kind == ResourceKindSystem {
		return denyf(DenyCodeAdminRequired, action, p, res,
			"action %q on system resource requires admin", action)
	}

	// Legacy resources fail closed — admin-only. Feature 36
	// backfilled these on load; the value means "no recoverable
	// owner information". Non-admin access is refused to match
	// the pre-feature-36 AUDIT L1 fail-closed behaviour.
	if res.OwnerPrincipal == principal.LegacyOwnerID {
		return denyf(DenyCodeLegacyOwner, action, p, res,
			"resource %s/%s has legacy owner sentinel; admin-only", res.Kind, res.ID)
	}

	switch p.Kind {
	case principal.KindNode:
		// Rule 3 — node principals are refused on every REST
		// surface. The per-kind action table in rules.go
		// exposes the narrow set of internal actions a node
		// is allowed to perform; those are checked here.
		if nodeAllowed(action, res.Kind) {
			return nil
		}
		return denyf(DenyCodeNodeNotAllowed, action, p, res,
			"node principals cannot perform %q on %q", action, res.Kind)

	case principal.KindService:
		// Rule 4 — service principals carry a per-service name
		// in DisplayName. The allow-list in rules.go keys on
		// (service_name, action, resource_kind).
		if serviceAllowed(p.DisplayName, action, res.Kind) {
			return nil
		}
		return denyf(DenyCodeServiceNotAllowed, action, p, res,
			"service %q cannot perform %q on %q",
			p.DisplayName, action, res.Kind)

	case principal.KindJob:
		// Rule 5 — workflow-scoped tokens can act on jobs in
		// the same workflow. The token's suffix IS the
		// workflow ID (by submit.py convention).
		if res.Kind != ResourceKindJob {
			return denyf(DenyCodeJobScopeMismatch, action, p, res,
				"job-scoped token cannot act on %q resources", res.Kind)
		}
		if res.WorkflowID == "" || res.WorkflowID != p.DisplayName {
			return denyf(DenyCodeJobScopeMismatch, action, p, res,
				"job-scoped token for workflow %q cannot act on job in workflow %q",
				p.DisplayName, res.WorkflowID)
		}
		// Within the workflow, job-scoped tokens may only
		// read. Write/cancel/delete/reveal stay admin+owner.
		if action != ActionRead && action != ActionList {
			return denyf(DenyCodeJobScopeMismatch, action, p, res,
				"job-scoped token cannot perform %q", action)
		}
		return nil

	case principal.KindUser, principal.KindOperator:
		// Rule 6 — owner check. Empty OwnerPrincipal on a
		// non-system, non-legacy resource is a coding bug
		// (feature 36 guarantees a stamp) — fail closed.
		if res.OwnerPrincipal == "" {
			return denyf(DenyCodeNotOwner, action, p, res,
				"resource %s/%s has empty owner; refusing", res.Kind, res.ID)
		}
		if p.ID == res.OwnerPrincipal {
			return nil
		}
		// Rule 6b (feature 38) — share grants. A share that
		// names the principal directly (`user:bob`) OR a
		// group the principal belongs to (`group:ml-team`)
		// and whose Actions list contains the requested
		// action widens access without making the grantee
		// an owner or an admin. Runs AFTER the owner check
		// so the happy path (owner reading own resource) is
		// a single comparison.
		for _, sh := range res.Shares {
			if matchesGrantee(p, sh.Grantee) && containsAction(sh.Actions, action) {
				return nil
			}
		}
		return denyf(DenyCodeNotOwner, action, p, res,
			"principal %q is not owner of %s/%s (owner=%q)",
			p.ID, res.Kind, res.ID, res.OwnerPrincipal)

	default:
		// Rule 8 — unknown / future Kinds fail closed. A
		// future feature adding a new Kind must extend this
		// switch with an explicit rule.
		return denyf(DenyCodeUnknownKind, action, p, res,
			"unknown principal kind %q", p.Kind)
	}
}
