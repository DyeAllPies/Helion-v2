# internal/persistence

BadgerDB wrapper for the Helion coordinator.

## Rules

**No package outside `persistence/` imports BadgerDB.**
All storage access goes through `Store`. This is the boundary that makes the §3.3
swap path to etcd possible without touching business logic.

**All keys are built through the typed constructors in `keys.go`.**
Never write `[]byte("nodes/" + addr)` in a business-logic file.
Use `persistence.NodeKey(addr)` instead. A rename is then a one-file change.

**Proto types are the wire format.**
`Put[T]` and `Get[T]` only accept `proto.Message` values.
The sole exception is `PutRaw`/`GetRaw`, reserved for X.509 DER bytes under `certs/`.

**TTL is explicit.**
`Put` never sets a TTL. If a value must expire (nodes/, tokens/), use `PutWithTTL`.
This makes expiry intent visible at the call site.

**Audit entries are append-only.**
Use `AppendAudit`. Never `Put` to a key under `audit/` — the key schema would
allow it, but the audit log must be immutable.

## Key schema (§4.5)

| Prefix         | Value type         | TTL               |
|----------------|--------------------|-------------------|
| `nodes/{addr}` | Node (proto)       | 2× heartbeat interval |
| `jobs/{id}`    | Job (proto)        | none (permanent)  |
| `workflows/{id}` | Workflow (JSON)  | none (permanent)  |
| `certs/{id}`   | X.509 DER (raw)    | none (permanent)  |
| `audit/{ts}-{id}` | AuditEvent (proto) | none (append-only) |
| `tokens/{jti}` | JWT metadata (proto) | token expiry    |
| `log:{job_id}:{seq}` | LogEntry (JSON) | 7 days (configurable) |

## Running tests

```
go test ./internal/persistence/... -v
```

Skip the TTL test (which sleeps 2 s) with `-short`:

```
go test ./internal/persistence/... -short
```

Run with the race detector (required in CI):

```
go test -race ./internal/persistence/...
```
