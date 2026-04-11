# Helion v2

[![CI](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml)

A from-scratch distributed job scheduler written in Go — built as a vehicle for
studying systems programming, distributed systems theory, container
orchestration, and production security practices.

All project documentation lives under **[`docs/`](docs/)**:

| Document | Contents |
|---|---|
| **[docs/README.md](docs/README.md)** | Project overview, stack, environment variables, local dev, deployment |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Component responsibilities, lifecycle, concurrency model |
| [docs/SECURITY.md](docs/SECURITY.md) | Threat model, mTLS + PQC, JWT lifecycle, audit log schema |
| [docs/AUDIT.md](docs/AUDIT.md) | Security & code-quality audit template |
| [docs/dashboard.md](docs/dashboard.md) | Angular 18 dashboard notes |
| [docs/persistence.md](docs/persistence.md) | `internal/persistence` rules and key schema |
| [docs/docker-compose-dev-notes.md](docs/docker-compose-dev-notes.md) | Local Docker Compose workflow |

Start with **[docs/README.md](docs/README.md)** for the full overview.

---

*Dennis Alves Pedersen · April 2026*
