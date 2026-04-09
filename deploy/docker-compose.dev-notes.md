# Docker Compose — local dev notes

## Start the cluster

```bash
docker compose up --build
```

## Stop cleanly

```bash
docker compose down
```

## Rebuild a single service after code changes

```bash
docker compose up --build coordinator
```

## View logs

```bash
docker compose logs -f coordinator
docker compose logs -f node1
```

## Notes

- Ports are bound to `127.0.0.1` only — not reachable from LAN.
- `restart: "no"` prevents containers restarting automatically during dev.
- `state/` and `logs/` are mounted as volumes so data survives container restarts.
- `HELION_ALLOW_ISOLATION=false` because namespace isolation requires root or CAP_SYS_ADMIN.
- The healthcheck on the coordinator uses `/healthz` — this endpoint is wired up in Phase 2.
  Until then, `service_started` (not `service_healthy`) is used as the depends_on condition.
- mTLS between coordinator and nodes is fully active from Phase 1.
  The CA is generated in-memory on coordinator startup and node certs are issued on registration.