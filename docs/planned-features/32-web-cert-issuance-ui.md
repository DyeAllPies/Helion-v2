# Feature: Web-based operator cert issuance UI

**Priority:** P3
**Status:** Pending
**Affected files:**
`dashboard/src/app/features/admin/operator-certs.component.*` (new),
`dashboard/src/app/core/services/api.service.ts` (new
`issueOperatorCert` method + `listOperatorCerts` when feature 31
lands the audit backend),
`dashboard/src/app/app.routes.ts` (new `/admin/operator-certs`
route, admin-guarded),
`docs/ops/operator-cert-guide.md` (extend with the browser flow).

## Problem

Feature 27 ships issuance as a CLI (`helion-issue-op-cert`). For
an admin onboarding a new operator, that means:

1. SSH to a host with the CLI.
2. `export HELION_COORDINATOR=...; export HELION_TOKEN=...`
3. `helion-issue-op-cert --operator-cn alice --p12-password-file ...`
4. Securely hand the P12 + password to alice.

For a single-operator demo this is fine. For an admin who onboards
ops every few weeks, it's friction. A dashboard flow is much less
error-prone: the admin stays in the browser, types the new
operator's CN, the UI generates the P12 server-side, and the browser
surfaces a download link with an on-screen prompt for the password.

## Current state

- `POST /admin/operator-certs` is shipped (feature 27) — admin-only,
  audited, rate-limited. The dashboard uses bearer-only JWTs today;
  no admin UI surface exists yet.
- `feature 22` added the submission tab; the admin surface it
  sketches (revoke node, issue token) already has a pattern for
  cert-gated admin actions.

## Design

### Component

`OperatorCertsComponent` under `/admin/operator-certs` (admin
role guard — `AuthGuard.requireRole('admin')`).

Form fields:

- **Operator CN** (text, required). Valid shell-identifier-ish
  characters + `@`. Client-side validation mirrors the server's
  `validateIssueOperatorCertRequest`.
- **TTL (days)** (number, default 90, max 365).
- **P12 password** (password, required, min 8 chars, strength
  meter). Generated randomly by default via a "Generate"
  button — the operator copies it once + pastes it at browser
  import time.

On submit:

1. POST `/admin/operator-certs` via `ApiService.issueOperatorCert`.
2. On 200, the response carries `{p12_base64, audit_notice, ...}`.
3. The component offers a **Download P12** button that triggers a
   browser download of the decoded P12 blob.
4. The P12 password is shown ONCE on-screen with a "copy to
   clipboard" button and an explicit "I have saved the password
   somewhere safe" checkbox required before the download button
   unlocks.
5. The audit notice from the response is rendered prominently
   alongside the download.

### Defensive behaviour

- **Password shown once, never re-fetchable.** The server doesn't
  store it; the client must not stash it in localStorage or
  cookies. Kept in component state only; lost on route change.
- **P12 blob cleared from memory on route change.** Use an
  explicit `ngOnDestroy` that zeros the cached bytes.
- **No browser-local retention of the private key.** The download
  lands on disk; the dashboard never writes the key to
  IndexedDB / localStorage / cache.

### Listing issued certs

If feature 31 (revocation) is live, the component also shows a
list of currently-valid operator certs issued by this coordinator
— populated by querying the audit log for
`operator_cert_issued` events minus any matching
`operator_cert_revoked`. Each row has a **Revoke** action that
hits the feature-31 revoke endpoint with a mandatory "reason".

## Security plan

| Threat | Control |
|---|---|
| Password-meter advice bypassed (admin types "12345678") | Server-side validator already enforces ≥ 8 chars. Client-side meter + confirmation checkbox adds friction; server is the authority. |
| P12 lingers in browser cache after download | Blob URL revoked immediately after the download triggers; component state cleared on route change. |
| Admin's browser session hijacked mid-issuance (CSRF) | Same-origin CORS (already baked into feature 22); Authorization header set explicitly by the Angular HttpClient per-request (not cookie-based, not targetable by CSRF). |
| Admin takes a screenshot of the password | Out of scope for this feature; the password appears on-screen once by design. Using `autocomplete="one-time-code"` on the password field discourages browser save. |
| Audit record missing because the tab crashed mid-response | Server already emits `operator_cert_issued` BEFORE returning the response body (audit-before-response fail-closed). The dashboard crashing post-issuance does not lose the audit trail. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `ApiService.issueOperatorCert` method + unit test. | — | Small |
| 2 | `OperatorCertsComponent` + template + styles. | 1 | Medium |
| 3 | Role-guarded route registration + admin-nav link. | 2 | Small |
| 4 | Download-once + password-shown-once flow + confirmation gate. | 2 | Medium |
| 5 | Listing view (when feature 31 persists issued cert records). | feature 31 | Medium |

## Tests

- `TestOperatorCertsComponent_Form_ValidatesClientSide` — CN +
  TTL + password field validation mirrors the server.
- `TestOperatorCertsComponent_IssueFlow_SetsDownloadBlob` — mock
  `ApiService.issueOperatorCert`; assert the component sets a
  blob URL on success.
- `TestOperatorCertsComponent_PasswordShownOnce` — after the
  "I have saved the password" checkbox is ticked, the password
  field is cleared + marked read-only.
- `TestOperatorCertsComponent_PasswordClearedOnDestroy` — the
  component zeros its password + p12 state on `ngOnDestroy`.
- `TestOperatorCertsComponent_403Handling` — a non-admin token
  gets 403 from the server; the component renders "Admin role
  required" without leaking the raw API error.

## Acceptance criteria

1. An admin on the dashboard types `alice@ops`, clicks Issue, and
   is shown a download button + a one-time password.
2. After downloading the P12 and ticking the confirmation
   checkbox, the password field is cleared.
3. Navigating away from the component clears all in-memory state.
4. `GET /audit?type=operator_cert_issued` shows the expected
   event with the admin's subject.

## Deferred

- **Email the P12 to the operator** (mailer integration). Out of
  scope; admin still has to hand off the file through whatever
  channel they already use.

## Implementation status

_Not started. Promoted from feature 27's "Deferred / out of scope"
on 2026-04-19._
