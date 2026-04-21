# Helion v2

[![CI](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml)

A minimal distributed job scheduler written in Go — built as a student
learning project for systems programming, distributed systems theory,
container orchestration, and production security practices.

All project documentation lives under **[`docs/`](docs/)**. Pick a lane:

| I want to… | Start here |
|---|---|
| Read the project overview, stack, and quickstart | **[docs/README.md](docs/README.md)** |
| Understand how Helion is built (components, protocols, persistence) | [docs/architecture/](docs/architecture/) |
| Run a cluster (env vars, rotation, revocation, runbooks) | [docs/operators/](docs/operators/) |
| Write a workflow or ML pipeline | [docs/guides/](docs/guides/) |
| Understand the threat model (crypto, auth, runtime, data plane) | [docs/security/](docs/security/) |
| Contribute (feature specs, audits, deferred items) | [docs/DOCS-WORKFLOW.md](docs/DOCS-WORKFLOW.md) |

## Packing the repo for download

`docs/audits/` and `docs/planned-features/` grow over time — a long
audit history can make the repo heavy when bundling for an AI assistant
or sharing offline. Pack **without** them using
[repomix](https://github.com/yamadashy/repomix):

```bash
# One-shot pack skipping the archive directories:
npx repomix --ignore "docs/audits/**,docs/planned-features/**"
```

Or with git:

```bash
git archive --format=tar.gz -o helion-v2.tar.gz HEAD \
  -- ':(exclude)docs/audits' ':(exclude)docs/planned-features'
```

Either command keeps [`docs/DOCS-WORKFLOW.md`](docs/DOCS-WORKFLOW.md)
and the three `TEMPLATE.md` files so a fresh reader still sees how the
process works, and drops only the dated audit files and feature-spec
archive.

---

*Dennis Alves Pedersen · April 2026*
