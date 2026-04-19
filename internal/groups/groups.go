// Package groups is the feature-38 group-membership store for
// the Helion coordinator.
//
// Role
// ────
// Features 35–37 established identity (Principal), ownership
// (OwnerPrincipal), and the admin-or-owner authz rule. This
// package adds the first piece of delegation: a named, flat
// collection of Principal IDs that can appear as a grantee on
// a resource `Share`. When Bob is a member of `ml-team` and
// Alice shares a workflow with `group:ml-team`, feature 37's
// authz evaluator (rule 6b) treats Bob as permitted for the
// share's Actions without making him an admin or a co-owner.
//
// Safety properties
// ─────────────────
//
//  1. Flat membership only. v1 does NOT support groups-of-groups
//     (nested memberships). Recursion-risk without a concrete
//     use case; a future slice can reconsider.
//
//  2. Typed namespace. A group is referenced as `group:<name>`
//     everywhere (audit events, share grantee fields, authz
//     grantee matching). A principal ID with prefix `user:`,
//     `operator:`, etc. cannot collide with a group name because
//     the prefix itself is different.
//
//  3. Admin-only management. Create, delete, and member-list
//     edits are `ActionAdmin` endpoints. Non-admins cannot enumerate
//     groups either — group names are admin knowledge.
//
//  4. Reverse index coherent. Each (group, member) pair is
//     reflected in TWO Badger keys: `groups/{name}` (the Group
//     record with full membership list) and `groups/members/
//     {principal_id}/{group_name}` (the reverse index used by
//     `GroupsFor`). Both indices are updated in a single Badger
//     transaction — a half-applied write never leaves the store
//     with one side without the other.
//
//  5. Name validation. Group names are a limited ASCII charset
//     ([a-zA-Z0-9._-], 1–64 chars). This keeps the key-space
//     predictable, prevents path traversal via `../`, and makes
//     the `group:<name>` serialisation unambiguous to parse.
//
// Not in v1
// ─────────
//   - Nested groups (groups whose members are other groups).
//   - Per-group roles or per-group permissions — a group is
//     just a named set; permissions come from the Share that
//     names the group as grantee.
//   - Soft-delete / undelete. A deleted group is gone; the
//     feature-38 delete path also sweeps dangling shares
//     referencing the group.
package groups

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ── Errors ──────────────────────────────────────────────────

// ErrGroupExists is returned by Create when a group with the
// same name already exists. Re-creating is a caller error, not
// a silent upsert.
var ErrGroupExists = errors.New("groups: group already exists")

// ErrGroupNotFound is returned by Get, member edits, and Delete
// when the target group does not exist.
var ErrGroupNotFound = errors.New("groups: group not found")

// ErrInvalidName is returned by Create / any name-taking method
// when the name fails validation.
var ErrInvalidName = errors.New("groups: invalid group name")

// ErrInvalidPrincipal is returned when a member-edit gets a
// principal ID that's empty or malformed. Feature-35 IDs are
// prefix-qualified; a bare subject without a `kind:` prefix is
// rejected at the boundary so the audit trail never carries
// ambiguous identifiers.
var ErrInvalidPrincipal = errors.New("groups: invalid principal id")

// ── Group record ────────────────────────────────────────────

// Group is a named, flat collection of Principal IDs. See
// package doc for invariants.
type Group struct {
	// Name is the unique group name. Case-sensitive. Stored
	// verbatim in the `group:<Name>` share grantee shape.
	Name string `json:"name"`

	// Members is the full list of Principal IDs. Ordered by
	// insertion time. Duplicates are rejected at AddMember time
	// (idempotent: re-adding returns nil without changing the
	// list; feature 37's share policy is set-semantic).
	Members []string `json:"members,omitempty"`

	CreatedAt time.Time `json:"created_at"`

	// CreatedBy is the Principal ID of the admin that created
	// the group. Immutable after creation. Used for audit, not
	// authz.
	CreatedBy string `json:"created_by"`

	UpdatedAt time.Time `json:"updated_at"`
}

// ── Store interface ─────────────────────────────────────────

// Store is the narrow interface the admin endpoints + feature 35
// principal resolver use. The BadgerDB implementation is in
// badger.go; tests use the in-memory fake in memstore.go.
type Store interface {
	// Create persists a new Group. Returns ErrGroupExists if
	// the name is already taken, ErrInvalidName if the name is
	// malformed.
	Create(ctx context.Context, g Group) error

	// Get reads a Group by name. Returns ErrGroupNotFound on a
	// missing key.
	Get(ctx context.Context, name string) (*Group, error)

	// List returns every Group, ordered by CreatedAt
	// descending (newest first, matching the rest of the API's
	// list conventions). Admin-only at the HTTP layer; the
	// store does not enforce.
	List(ctx context.Context) ([]Group, error)

	// AddMember adds a Principal ID to the group. Idempotent —
	// re-adding an existing member returns nil without
	// changing the list or updating UpdatedAt.
	AddMember(ctx context.Context, name, principalID string) error

	// RemoveMember removes a Principal ID from the group.
	// Idempotent — removing an absent member returns nil.
	RemoveMember(ctx context.Context, name, principalID string) error

	// Delete removes the group and every reverse-index entry
	// for its members. Does NOT sweep dangling `group:{name}`
	// shares on resources — that's a feature-38 follow-up in
	// the share-delete path (see share.go).
	Delete(ctx context.Context, name string) error

	// GroupsFor returns the list of group names the principal
	// is a member of. Feature 35's resolvePrincipal calls this
	// to populate Principal.Groups. O(1) in the group count;
	// O(n_groups_for_principal) in entries returned. Unknown
	// principals return (nil, nil).
	GroupsFor(ctx context.Context, principalID string) ([]string, error)
}

// ── Name + principal validation ────────────────────────────

// ValidateName checks that name is well-formed.
func ValidateName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidName)
	}
	if len(name) > 64 {
		return fmt.Errorf("%w: over 64 chars", ErrInvalidName)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == '.'
		if !ok {
			return fmt.Errorf("%w: disallowed char %q at %d", ErrInvalidName, c, i)
		}
	}
	// A name starting with '.' would produce a Badger key
	// ending in a path-traversal-looking sequence. Reject even
	// though we sanitise via key-prefix join; defence in depth.
	if name[0] == '.' {
		return fmt.Errorf("%w: must not start with '.'", ErrInvalidName)
	}
	return nil
}

// ValidatePrincipalID checks that id looks like a feature-35
// prefixed Principal ID. A bare subject ("alice") without a
// kind prefix ("user:alice") would silently break the authz
// match later, so we reject at the boundary.
//
// Allowed:
//   - "anonymous" (the sentinel — though AddMember on anonymous
//     is admin-permitted but policy-useless; we don't block it
//     here).
//   - "<kind>:<suffix>" where suffix is 1+ chars.
//
// Rejected:
//   - Empty.
//   - No colon.
//   - Prefix starts with `group:` (groups-of-groups not in v1).
func ValidatePrincipalID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrInvalidPrincipal)
	}
	if id == "anonymous" {
		return nil
	}
	colon := -1
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			colon = i
			break
		}
	}
	if colon <= 0 {
		return fmt.Errorf("%w: %q missing kind prefix (expected 'kind:subject')", ErrInvalidPrincipal, id)
	}
	if colon == len(id)-1 {
		return fmt.Errorf("%w: %q has empty subject", ErrInvalidPrincipal, id)
	}
	kind := id[:colon]
	if kind == "group" {
		return fmt.Errorf("%w: %q nested groups are not supported", ErrInvalidPrincipal, id)
	}
	return nil
}
