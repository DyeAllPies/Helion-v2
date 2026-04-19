// internal/authz/rules.go
//
// Per-kind action allow-lists for non-user principals. Read
// during review as a compile-time policy artifact — a new entry
// is a policy decision that goes through code review, not a
// config flag someone can flip at runtime.
//
// Philosophy
// ──────────
// Users and operators are permission-checked against the owner
// of the resource (`p.ID == res.OwnerPrincipal`). Everyone else
// — nodes, services, job-scoped tokens — gets a narrow,
// explicit allow-list. The absence of a rule means deny; there
// is no implicit fall-through.

package authz

// ── Node principals ────────────────────────────────────────────

// nodeActionsByResource is the set of (Action, ResourceKind)
// pairs a node principal may perform. Today's list is empty —
// nodes never authorise against REST resources. The placeholder
// structure exists so a future gRPC-over-authz migration can
// populate the pairs without a second edit pass here.
//
// If you're about to add an entry, ask: is the caller really a
// registered node acting on the node-surface gRPC? If yes, the
// gRPC handler is probably the right place to enforce (not the
// REST Allow call). If the caller is a human impersonating a
// node, that's exactly what this empty table keeps out.
var nodeActionsByResource = map[ResourceKind]map[Action]bool{
	// Deliberately empty. REST surface is closed to node
	// principals.
}

func nodeAllowed(action Action, kind ResourceKind) bool {
	return nodeActionsByResource[kind][action]
}

// ── Service principals ────────────────────────────────────────

// serviceActions is the allow-list keyed by service name. Each
// service has a narrow set of (action, resource_kind) it may
// perform. Services are coordinator-internal loops that stamp
// themselves via `principal.Service(name)`; a name that isn't
// in this map is implicitly denied on every action.
//
// Naming invariant: the keys here MUST match the
// DisplayName set by the corresponding `principal.Service*`
// constructor in internal/principal/principal.go. A rename there
// without a matching rename here is a silent authz regression.
var serviceActions = map[string]map[ResourceKind]map[Action]bool{
	// dispatcher — internal/cluster/dispatch.go drives job
	// state transitions. Reads jobs to compute eligibility,
	// transitions them as it dispatches. No delete, no
	// reveal.
	"dispatcher": {
		ResourceKindJob: {
			ActionRead:   true,
			ActionList:   true,
			ActionCancel: true, // timeout-driven cancel
		},
	},

	// workflow_runner — orchestrates workflow lifecycle.
	// Creates child jobs, reads workflow state.
	"workflow_runner": {
		ResourceKindJob: {
			ActionRead:  true,
			ActionWrite: true, // creates materialised child jobs
			ActionList:  true,
		},
		ResourceKindWorkflow: {
			ActionRead:   true,
			ActionList:   true,
			ActionCancel: true,
		},
	},

	// retry_loop — feature 02 retry logic.
	"retry_loop": {
		ResourceKindJob: {
			ActionRead:   true,
			ActionList:   true,
			ActionWrite:  true, // re-enqueues failed jobs
			ActionCancel: true, // gives up after max_attempts
		},
	},

	// log_ingester — feature 28 per-job log capture.
	"log_ingester": {
		ResourceKindJob: {
			ActionRead: true,
		},
	},

	// retention — feature 28 analytics retention cron.
	// Reads audit events for PG forwarding; no mutation of
	// user-owned resources.
	"retention": {
		// Intentionally empty: retention operates on its own
		// PG tables, not on coordinator resources. Kept as a
		// named entry so a future addition goes through
		// review.
	},

	// log_reconciler — feature 28 Badger→PG log flusher.
	"log_reconciler": {
		ResourceKindJob: {
			ActionRead: true,
		},
	},

	// coordinator — generic lifecycle audit principal. The
	// start/stop events do not target a specific resource;
	// they use SystemResource. But admin short-circuits on
	// system, and this service is not admin, so coordinator
	// has no allow here. It's present in the list for audit
	// completeness (so `p.Kind == service` + coordinator name
	// is not a silent-unknown).
	"coordinator": {},
}

func serviceAllowed(name string, action Action, kind ResourceKind) bool {
	byResource, ok := serviceActions[name]
	if !ok {
		return false
	}
	return byResource[kind][action]
}
