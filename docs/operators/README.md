> **Audience:** operators
> **Scope:** Index for day-2 operations — env vars, runbooks, cert + auth rotation.
> **Depth:** runbook

# Operators

Everything needed to run a Helion cluster: environment variables, startup
checklists, rotation runbooks, and authentication setup for dashboard operators.
For design rationale behind the mitigations below, see
[`../SECURITY.md`](../SECURITY.md) (splits into `../security/` in feature 44
commit 3).

## Files in this folder

| File | Contents |
|---|---|
| [runbook.md](runbook.md) | Security-ops checklist — restart, recovery, rotation, revocation. |
| [cert-rotation.md](cert-rotation.md) | Minting, installing, rotating, and revoking operator client certificates (feature 27 + 31). |
| [webauthn.md](webauthn.md) | Registering hardware authenticators and the step-up login flow (feature 34). |
| [jwt.md](jwt.md) | JWT token lifecycle — issuance, usage, revocation. |
| [docker-compose.md](docker-compose.md) | Local-dev compose workflow notes. |

## Environment variable reference

_Populated in feature 44 commit 4 — see [`../README.md` § Environment variables](../README.md#environment-variables) until then._
