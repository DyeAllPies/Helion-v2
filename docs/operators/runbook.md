> **Audience:** operators
> **Scope:** Day-2 ops checklist — restart, rotation, revocation, troubleshooting.
> **Depth:** runbook

# Helion v2 — Security Operations Guide

Operational checklists, environment variables, and troubleshooting for the
security subsystem. For the threat model and architecture, see [security/](../security/).

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `HELION_RATE_LIMIT_RPS` | `10` | Per-node rate limit (jobs/second) |
| `HELION_AUDIT_TTL` | `7776000` (90 days) | Audit event TTL in seconds; `0` = no expiry |
| `HELION_TOKEN_FILE` | `/var/lib/helion/root-token` | Path where root token is written (mode `0600`) |
| `HELION_ROTATE_TOKEN` | `true` | Rotate root token on every restart |

---

## First-start checklist

- [ ] **Save the root token.** It is written to `HELION_TOKEN_FILE` (default
      `/var/lib/helion/root-token`, mode `0600`). Store it in a password manager.
- [ ] **Verify TLS with Wireshark.** Confirm `x25519_mlkem768 (0x6399)` appears in the
      ClientHello supported_groups extension.
- [ ] **Confirm audit logging.** Submit a test job and verify a `job_submit` event appears
      at `GET /audit`.
- [ ] **Test rate limiting.** Submit burst traffic and confirm `ResourceExhausted` responses
      and `rate_limit_hit` audit events.

---

## Saving the root token

```bash
export HELION_ROOT_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
echo "export HELION_ROOT_TOKEN=\"$HELION_ROOT_TOKEN\"" >> ~/.helion_env
chmod 600 ~/.helion_env
source ~/.helion_env
```

---

## Production recommendations

1. Rotate root token periodically (every 90 days).
2. Monitor `auth_failure` events — alert on sustained spikes.
3. Export audit log to a SIEM for long-term retention.
4. Restrict coordinator API access via firewall / VPN.
5. Enable Kubernetes `NetworkPolicy` to limit pod-to-pod traffic.

---

## Troubleshooting

### "token revoked or invalid JTI"

The JTI record is absent from BadgerDB (revoked, expired TTL, or DB wiped). Generate a
new root token by restarting the coordinator against an empty BadgerDB.

### Rate limit hit immediately

The burst is exhausted or the rate is set too low. Increase `HELION_RATE_LIMIT_RPS` and
restart the coordinator.

### Node rejected with Unauthenticated

The node's certificate may be revoked or the CA has been regenerated (coordinator restarted
against empty BadgerDB). Delete the node's certificate on disk and let it re-register.

### WebSocket connection fails (4001 or no data)

WebSocket auth uses first-message pattern: the client must send
`{"type":"auth","token":"<jwt>"}` as the first frame after connecting. The
server replies `{"type":"auth_ok"}` on success or closes with code 4001 on
failure. Verify the token has not expired and is being sent as the first frame.

### Seccomp or OOMKilled in job result

Expected behaviour — the Rust runtime enforces syscall and memory limits. Check the
coordinator audit log for a `security_violation` event with details about the violation.
If the limits are too restrictive, adjust via the job submission request fields.
