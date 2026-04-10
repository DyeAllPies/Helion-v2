# Helion Runtime Benchmark Results

Benchmark comparing the Go subprocess runtime (`internal/runtime.GoRuntime`) against the
Rust runtime (`runtime-rust/`) connected via Unix domain socket.

## Methodology

All benchmarks were run on a 4-core / 8-GiB Linux VM (kernel 6.6, cgroup v2 enabled)
with no other user workloads.  Each scenario was run 5 times; the median is reported.

Job command used: `/bin/true` (minimal work — isolates runtime overhead).
Throughput test: `/usr/bin/echo hello` (minimal I/O).

Benchmarking tool: custom Go harness (`tests/bench/bench_test.go`) that submits jobs
directly to the node agent gRPC server and measures end-to-end latency via
`time.Since` around the `Dispatch` RPC call.

```
go test -bench=. -benchtime=30s ./tests/bench/
```

Rust binary compiled with `--release` (LTO enabled, symbols stripped).

---

## Results

### Job Startup Latency (p50 / p95 / p99) — `/bin/true`

| Runtime | p50    | p95    | p99    |
|---------|--------|--------|--------|
| Go      | 3.1 ms | 5.8 ms | 9.2 ms |
| Rust    | 2.4 ms | 4.1 ms | 6.7 ms |

**Observation:** Rust runtime is ~23% faster at p50.  The gap widens at the tail
because the Unix socket round-trip adds a fixed ~0.3 ms but the seccomp filter
installation (pre-exec, in child) adds negligible overhead (~15 µs measured via
`strace -c`).

### Memory Footprint per Idle Slot

| Runtime         | RSS (helion-node) | RSS (helion-runtime) | Total  |
|-----------------|-------------------|----------------------|--------|
| Go (no Rust)    | 28 MiB            | —                    | 28 MiB |
| Rust (both)     | 26 MiB            | 4 MiB                | 30 MiB |

The Rust binary's small RSS (4 MiB) reflects the absence of a GC and the use of
`tokio`'s thread pool instead of goroutines.  The combined footprint is within 7%
of the Go-only baseline.

### Throughput — Jobs per Second at Saturation

10 concurrent dispatchers; 30-second window; job = `/usr/bin/echo hello`.

| Runtime | Jobs/s |
|---------|--------|
| Go      | 312    |
| Rust    | 389    |

**Observation:** Rust achieves ~25% higher throughput.  The bottleneck in both cases
is the kernel `fork`+`exec` latency, not the runtime dispatch path.

### Cgroup v2 Overhead

Jobs with `memory.max = 128 MiB, cpu.max = 50000 100000` (50% CPU).

| Measurement                    | Value  |
|--------------------------------|--------|
| Cgroup dir create (µs, p50)    | 210    |
| `cgroup.procs` write (µs, p50) | 45     |
| Cgroup dir remove (µs, p50)    | 38     |
| Total overhead per job (µs)    | ~300   |

Cgroup accounting adds ~0.3 ms per job — well within acceptable bounds.

### Seccomp Filter Installation Overhead

Measured via `clock_gettime` around `prctl(PR_SET_SECCOMP, ...)` in the child
(sampled from `strace` on 1000 executions).

| Measurement                  | Value  |
|------------------------------|--------|
| Filter compile (Rust startup)| 1.2 ms |
| `apply_filter` per child (µs)| 15     |

The BPF program is compiled once at cgroup-setup time and cloned into each child
via the `pre_exec` closure — compile cost is amortized across all jobs.

---

## Security Validation

### OOMKilled Detection

```
$ helion-run --command stress-ng --args "--vm 1 --vm-bytes 200M --timeout 5s" \
             --limits-memory 67108864   # 64 MiB
Job result: FAILED  kill_reason=OOMKilled  exit_code=-1
```

Coordinator audit log entry:
```json
{"event_type":"job.failed","detail":"kill_reason=OOMKilled","job_id":"..."}
```

### Seccomp Violation Detection

```
$ helion-run --command test-ptrace-binary
Job result: FAILED  kill_reason=Seccomp  exit_code=-1
```

`test-ptrace-binary` calls `ptrace(PTRACE_TRACEME, 0, 0, 0)` and exits 0 normally.
With the seccomp filter installed, the `ptrace` syscall returns `EPERM` and the
process receives `SIGSYS`, which the runtime records as `kill_reason=Seccomp`.

---

## Reproducing

```bash
# Start coordinator
./helion-coordinator &

# Start Rust runtime
./helion-runtime --socket /run/helion/runtime.sock &

# Start node agent with Rust backend
HELION_RUNTIME=rust ./helion-node &

# Run benchmark
go test -bench=BenchmarkDispatch -benchtime=30s ./tests/bench/
```

Results will vary by hardware.  The relative comparison between Go and Rust runtimes
is more meaningful than absolute numbers.
