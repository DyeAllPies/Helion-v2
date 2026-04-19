## Deferred: Key-level ACL on secrets

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 26 — secret env vars](../implemented/26-secret-env-vars.md)

## Context

Today any admin-role token can read back any secret env value on any
job via `POST /admin/jobs/{id}/reveal-secret`. The "reason" string
(audited) is the only accountability control. A finer-grained policy
would let an operator say:

- *"Only members of the `ml-training` group may set `HF_TOKEN` as a
  secret on a new job."*
- *"Only members of the `sre` group may call `reveal-secret` on a job
  whose secret key is `AWS_SECRET_ACCESS_KEY`."*

Key-level ACLs would also let a downstream dispatch flow check "is
this node permitted to RECEIVE the decrypted value of this key" and
refuse the dispatch otherwise — the "who can SET" and "who can
DISPATCH a job that reads one" split named in the original feature
26 spec.

## Why deferred

The coordinator has no group/identity model today beyond the binary
`admin` / `node` JWT role. Adding ACLs would require:

1. **A directory of identities with groups / roles** — either
   directly in BadgerDB or via OIDC/SSO integration. Every decision
   about which storage backend to prefer is out of scope for feature
   26.
2. **A policy engine or at least a policy grammar.** Inline
   per-endpoint hard-coded checks would not scale to "dataset X is
   readable by group Y but writable by group Z" once the registry
   ACL follow-up arrives.
3. **An audit + UI story** for each of the split verbs. We already
   audit reveal; adding submit-time per-key ACL denials, and a
   dashboard surface for managing the policy itself, is its own
   slice.

Feature 26 ships with the single "admin vs not-admin" control + the
mandatory audit `reason` because that is the minimum viable security
story on the current JWT model. Layering per-key ACLs belongs after
(or together with) whichever identity/RBAC overhaul Helion adopts.

## Revisit trigger

- An operator runs more than one team out of one Helion cluster and
  needs to prevent cross-team secret reads without spinning up a
  second cluster.
- An OIDC/SSO integration lands and brings a natural group claim we
  can check against a policy map.
