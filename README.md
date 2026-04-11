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
| [docs/AUDIT.md](docs/AUDIT.md) | Security & code-quality audit **template** (blank — copy into `docs/audits/<YYYY-MM-DD>.md` when starting a new audit) |
| [docs/audits/](docs/audits/) | Archive of closed audits, one Markdown file per audit run (grows over time) |
| [docs/dashboard.md](docs/dashboard.md) | Angular 18 dashboard notes |
| [docs/persistence.md](docs/persistence.md) | `internal/persistence` rules and key schema |
| [docs/docker-compose-dev-notes.md](docs/docker-compose-dev-notes.md) | Local Docker Compose workflow |

Start with **[docs/README.md](docs/README.md)** for the full overview.

---

## Packing the repo for download (excluding audit logs)

`docs/audits/` grows over time — a long audit history can make the repo
heavy when bundling it for an AI assistant or sharing it offline. Pack
the source tree **without** the audits using [repomix](https://github.com/yamadashy/repomix):

```bash
# One-shot: pack the whole repo to repomix-output.xml, skipping audit archives.
npx repomix --ignore "docs/audits/**"
```

Prefer git? The same idea works as a git archive:

```bash
git archive --format=tar.gz -o helion-v2.tar.gz HEAD \
  -- ':(exclude)docs/audits'
```

Either command keeps the template (`docs/AUDIT.md`) and ships the code,
but drops every dated audit file under `docs/audits/`.

---

*Dennis Alves Pedersen · April 2026*
