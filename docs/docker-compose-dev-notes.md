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
  The root token is written to `HELION_TOKEN_FILE` (default `/var/lib/helion/root-token`,
  mode `0600`). Save it before the container restarts.

## E2E test overlay

An E2E-specific compose overlay (`docker-compose.e2e.yml`) exposes the coordinator HTTP
API on host port `8080` and writes the root token to `./state/root-token` so Playwright
can read it:

```bash
# Start the E2E cluster
docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d --build

# Or use the one-command script (from project root):
make test-e2e
```

The overlay sets `HELION_ROTATE_TOKEN=false` so the token remains stable across restarts
during test development. See `scripts/run-e2e.sh` for the full lifecycle (start → wait →
test → tear down).