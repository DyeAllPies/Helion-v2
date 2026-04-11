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
- The healthcheck on the coordinator uses `/healthz`. Node depends_on uses `service_healthy`.
- mTLS between coordinator and nodes is fully active. The CA is generated in-memory on
  coordinator startup and node certs are issued on registration.
- JWT authentication is required on all API endpoints except `/healthz` and `/readyz`.
  The root token is printed to stdout on first start — save it before the container restarts.