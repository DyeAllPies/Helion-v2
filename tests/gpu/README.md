# GPU integration tests — local-only harness

Tests under this directory touch real NVIDIA hardware via `nvidia-smi`
and the CUDA runtime. They are **not** part of the normal CI run and
are gated by the `gpu` build tag so they don't even compile unless
the tag is explicitly set.

## Why gated

GitHub Actions free-tier runners have no GPU. Any test that actually
requires a device would either fail silently (`nvidia-smi` missing)
or be accidentally skipped, which is worse than never running — the
CI signal would lie about coverage. The scheduling, allocation, and
`CUDA_VISIBLE_DEVICES` plumbing is already covered by the pure-Go
unit tests under `internal/runtime/` and `internal/cluster/`. What
stays here is the smoke-level check that, on a real GPU box, the
pieces fit together.

## Running

```bash
# From the repo root — runs only the gpu-tagged tests, nothing else.
./scripts/test-gpu.sh

# Or directly:
go test -tags gpu -count=1 ./tests/gpu/...
```

Prerequisites:

- A host with at least one NVIDIA GPU.
- `nvidia-smi` on `$PATH` (ships with the NVIDIA driver).
- Go 1.21+.

No CUDA toolkit required — these tests don't compile CUDA code, they
just verify the coordinator + node + runtime plumbing produces the
right `CUDA_VISIBLE_DEVICES` value for a real device inventory.

## What lives here

`real_nvidia_smi_test.go` — spot-check that `runNvidiaSmiCount()` and
`runNvidiaSmi()` from `cmd/helion-node/labels.go` return something
sensible on an actual host. These are the two functions stubbed out
in every other test; here is where the real-probe code path gets
exercised at least once before a release.

## Adding new GPU tests

Keep them small (see the project memory note about GPU testing). A
good GPU test here:

- Starts with `//go:build gpu` as the first line of the file.
- Runs in seconds, not minutes. One GPU test at a time.
- Does not train a model, load a real checkpoint, or stress PCIe.
  The point is to catch integration bugs in Helion's plumbing,
  not to benchmark CUDA.
- Skips gracefully when a prerequisite is missing (e.g. if the
  host has a GPU but not the specific compute capability a test
  needs, call `t.Skip` with a clear reason).

Anything bigger (e.g. end-to-end PyTorch training on a real dataset)
belongs in a separate benchmarking repo, not here.
