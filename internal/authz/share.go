// internal/authz/share.go
//
// Feature 38 — resource shares. A Share is a per-resource grant
// that widens the default owner-or-admin policy to named
// principals or named groups, for a specific action set.
//
// Semantics
// ─────────
//
//   - A Share is attached to ONE resource. Sharing Alice's
//     workflow with Bob does not share her other workflows;
//     each resource keeps its own share list.
//   - Grantee is prefix-qualified. `user:bob` grants Bob
//     personally; `group:ml-team` grants every member of
//     `ml-team`. There is no wildcard grantee in v1 — sharing
//     with `everyone` is not expressible through this field.
//   - Actions is enumerated. A Share can grant
//     `[ActionRead]`, `[ActionRead, ActionCancel]`, etc. There
//     is no wildcard action. Feature 38 intentionally keeps
//     the evaluator deterministic by table lookup.
//   - GrantedBy is the Principal ID that created the Share —
//     typically the resource owner or an admin. Used for
//     audit; never consulted in the Allow decision.
//
// Non-transitive
// ──────────────
// A grantee with ActionRead on a resource cannot re-share
// onward — the share-mutation endpoints require the caller to
// be the owner OR have ActionAdmin. Transitive delegation is a
// slippery slope and explicitly deferred per the feature spec.
//
// Legacy-owner interaction
// ────────────────────────
// Resources backfilled with the `legacy:` owner sentinel
// (feature 36) are admin-only regardless of Share list — the
// evaluator denies on the legacy check BEFORE rule 6b (shares)
// runs. An admin can still share a legacy-owned resource to
// delegate access while keeping the owner unknown.

package authz

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// MaxSharesPerResource caps the share list per resource. Beyond
// this the share endpoint returns 400 with a hint to use a
// group instead. Keeps the per-request Allow scan cheap and
// nudges operators toward groups for large teams. Enforced at
// the HTTP layer.
const MaxSharesPerResource = 32

// Share is a single grant on one resource.
type Share struct {
	// Grantee is the full Principal ID or a group reference.
	// Must start with one of: "user:", "operator:", "group:",
	// "service:", "job:", or be the literal "anonymous" (an
	// anonymous share is nonsensical and rejected at the
	// share-mutation endpoint, but kept expressible so an
	// attempted-anonymous grant logs an audit deny).
	Grantee string `json:"grantee"`

	// Actions enumerates which actions the grantee may perform
	// on the resource. Empty Actions is rejected at validate
	// time — a zero-action share grants nothing and is pure
	// storage bloat.
	Actions []Action `json:"actions"`

	// GrantedBy is the Principal ID of the caller that
	// created the share. Audit-only.
	GrantedBy string `json:"granted_by"`

	// GrantedAt records when the share was recorded.
	GrantedAt time.Time `json:"granted_at"`
}

// ── Validation ─────────────────────────────────────────────

// ErrShareInvalid is the sentinel returned by ValidateShare
// for any malformed Share. Errors wrap it with fmt.Errorf.
var ErrShareInvalid = errors.New("authz: invalid share")

// ValidateShare checks Grantee + Actions shape. Does NOT check
// whether the grantee exists (group resolution happens at Allow
// time; a share on a non-existent group is inert but not
// invalid).
func ValidateShare(s Share) error {
	if s.Grantee == "" {
		return errorsf("empty grantee")
	}
	if s.Grantee == "anonymous" {
		return errorsf("grantee %q is meaningless (anonymous denied everywhere)", s.Grantee)
	}
	colon := strings.IndexByte(s.Grantee, ':')
	if colon <= 0 || colon == len(s.Grantee)-1 {
		return errorsf("grantee %q missing prefix or empty subject", s.Grantee)
	}
	kind := s.Grantee[:colon]
	switch kind {
	case "user", "operator", "group", "service", "job":
		// ok
	default:
		return errorsf("grantee %q has unknown kind %q", s.Grantee, kind)
	}
	if len(s.Actions) == 0 {
		return errorsf("share on %q has no actions", s.Grantee)
	}
	for _, a := range s.Actions {
		if !knownAction(a) {
			return errorsf("unknown action %q in share on %q", a, s.Grantee)
		}
		// ActionAdmin cannot be share-granted. Admin is a
		// kind-level role, not a per-resource capability.
		if a == ActionAdmin {
			return errorsf("ActionAdmin cannot be granted via share")
		}
	}
	return nil
}

// knownAction guards the Share validator against typos that
// would produce an inert Share. Mirrors the constants above.
func knownAction(a Action) bool {
	switch a {
	case ActionRead, ActionList, ActionWrite,
		ActionCancel, ActionDelete, ActionReveal, ActionAdmin:
		return true
	}
	return false
}

// errorsf builds a *shareInvalidError carrying a formatted
// message. Wrapping ErrShareInvalid lets callers use
// errors.Is(err, ErrShareInvalid) without caring about the
// specific reason string.
func errorsf(format string, args ...any) error {
	return &shareInvalidError{msg: fmt.Sprintf(format, args...)}
}

type shareInvalidError struct{ msg string }

func (e *shareInvalidError) Error() string { return "authz: invalid share: " + e.msg }
func (e *shareInvalidError) Unwrap() error { return ErrShareInvalid }

// ── Share matching (evaluator) ─────────────────────────────

// matchesGrantee returns true iff p falls inside grantee's
// namespace. Two shapes:
//
//   - `user:bob`, `operator:alice@ops`, etc. → exact ID match.
//   - `group:ml-team` → p.Groups must contain "ml-team".
//
// Nil principal returns false. Empty grantee returns false.
func matchesGrantee(p *principal.Principal, grantee string) bool {
	if p == nil || grantee == "" {
		return false
	}
	if p.ID == grantee {
		return true
	}
	if strings.HasPrefix(grantee, "group:") {
		name := strings.TrimPrefix(grantee, "group:")
		for _, g := range p.Groups {
			if g == name {
				return true
			}
		}
	}
	return false
}

// containsAction returns true iff action is in actions.
func containsAction(actions []Action, action Action) bool {
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}
