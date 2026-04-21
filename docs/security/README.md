> **Audience:** engineers + operators
> **Scope:** Index for the security reference — threat model grouped by subsystem.
> **Depth:** reference

# Security

Threat model, crypto, auth, runtime hardening, and data-plane protections for
Helion v2. This folder replaces the single 1,772-line `SECURITY.md` with
subsystem-owned files so future features append to the matching file, not
the end of a monolith.

## Files in this folder

_The split lands in feature 44 commit 3. Until then, the canonical security
reference is [`../SECURITY.md`](../SECURITY.md)._

| File | Contents | Status |
|---|---|---|
| crypto.md | mTLS + PQC + cert lifecycle. | Planned |
| auth.md | JWT + WebAuthn + operator certs + rate limiting + REST API. | Planned |
| runtime.md | Seccomp, cgroups, env denylist, secret scrubbing. | Planned |
| data-plane.md | Audit log, analytics PII, log redaction, artifact attestation. | Planned |

## Where things live today

| Topic | Current location |
|---|---|
| Threat-model summary table | [`../SECURITY.md` § 1](../SECURITY.md#1-threat-model) |
| mTLS + cert architecture | [`../SECURITY.md` § 2](../SECURITY.md#2-mtls-and-certificate-architecture) |
| Post-quantum cryptography | [`../SECURITY.md` § 3](../SECURITY.md#3-post-quantum-cryptography) |
| JWT authentication | [`../SECURITY.md` § 4](../SECURITY.md#4-jwt-authentication) + [`../operators/jwt.md`](../operators/jwt.md) |
| Rate limiting | [`../SECURITY.md` § 5](../SECURITY.md#5-rate-limiting) |
| Audit logging | [`../SECURITY.md` § 6](../SECURITY.md#6-audit-logging) |
| Runtime hardening / env denylist / secret scrubbing | [`../SECURITY.md` § 9](../SECURITY.md#9-dashboard-security) |
| Operator runbook | [`../operators/runbook.md`](../operators/runbook.md) |
