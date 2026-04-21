> **Audience:** engineers
> **Scope:** Rust runtime — cgroup v2, seccomp, Unix-socket IPC protocol.
> **Depth:** reference

# Helion Rust Runtime (`runtime-rust/`)

The Rust runtime is the hardened job-execution backend for the Helion node
agent. It runs as its own long-lived process next to the Go node agent,
listens on a Unix domain socket, and executes jobs inside a cgroup v2 scope
with a seccomp-bpf syscall allowlist applied to the child.

The Go runtime (`internal/runtime/go_runtime.go`) remains the default; the
Rust runtime is opt-in and meant for Linux nodes where isolation matters.

---

## 1. Where it fits

```
 coordinator ──gRPC──► node agent ──UDS──► helion-runtime (Rust)
                      (Go)                   └─► cgroup v2
                                              └─► seccomp-bpf
                                              └─► child process
```

The node agent chooses a runtime at startup from two env vars:

| Env var                   | Values          | Effect                          |
| ------------------------- | --------------- | ------------------------------- |
| `HELION_RUNTIME`          | `go` \| `rust`  | Selects implementation          |
| `HELION_RUNTIME_SOCKET`   | path            | UDS path when `rust` selected   |

When `rust` is selected, [rust_client.go](../../internal/runtime/rust_client.go)
dials the socket once per job, writes a `RunRequest` frame, and blocks on
the `RunResponse`.

---

## 2. Source layout

```
runtime-rust/
├── Cargo.toml
├── build.rs              # prost_build — compiles ../proto/runtime.proto
└── src/
    ├── main.rs           # socket accept loop, signal handling
    ├── ipc.rs            # frame I/O + message dispatch
    ├── proto.rs          # include! of prost-generated types
    ├── executor.rs       # fork/exec, timeout, cancel, kill-reason
    ├── cgroups.rs        # cgroup v2 scope lifecycle (Linux only)
    └── seccomp_filter.rs # static syscall allowlist
```

Generated proto types land in `$OUT_DIR/helion.rs` and are re-exported via
`proto.rs` — you never commit generated code.

---

## 3. Wire protocol

Defined in [proto/runtime.proto](../../proto/runtime.proto). The Go and Rust
sides both target proto3 wire encoding; the Go side hand-rolls it with
`google.golang.org/protobuf/encoding/protowire` to avoid the protoc codegen
dependency.

Frame:

```
[4 bytes BE msg_type][4 bytes BE payload_length][payload]
```

Message types: `1=RunRequest 2=RunResponse 3=CancelRequest 4=CancelResponse`.

### `RunRequest` fields

| # | Name              | Notes                                              |
|---|-------------------|----------------------------------------------------|
| 1 | `job_id`          | Runtime-opaque identifier; echoed in the response  |
| 2 | `command`         | Absolute path recommended                          |
| 3 | `args`            | Repeated                                           |
| 4 | `env`             | Child env is cleared first, then this map applied  |
| 5 | `timeout_seconds` | Wall-clock; `0` → 30 min default                   |
| 6 | `limits`          | `ResourceLimits` sub-message (cgroup v2)           |
| 7 | `working_dir`     | Child is `chdir`'d here before exec; empty = agent cwd |

Field 7 is populated by the staging layer
([internal/staging](../../internal/staging/staging.go)) with a per-job
directory so the child process cannot see another job's inputs.

### `RunResponse.kill_reason`

Free-form string, one of:

- `""`       — normal exit (code in `exit_code`)
- `Timeout`  — wall-clock timeout fired, child was SIGKILLed
- `OOMKilled` — cgroup memory event observed (`cgroups::was_oom_killed()`)
- `Seccomp`  — child received SIGSYS (filter default-action)

---

## 4. Isolation primitives

### 4.1 cgroup v2 (`cgroups.rs`)

Per-job scope at `/sys/fs/cgroup/helion/<job_id>/`. Sets `memory.max` and
`cpu.max` before adding the child PID. Scope directory is removed when the
`CgroupHandle` is dropped after the child exits.

Requires a host on cgroup v2 unified hierarchy. Scope creation is
best-effort: failures are logged, and the job still runs (without limits)
so that a misconfigured host degrades to the GoRuntime experience rather
than dropping jobs outright.

### 4.2 seccomp-bpf (`seccomp_filter.rs`)

Compiled once per job, installed in the child via `Command::pre_exec` —
after fork, before exec, at a point where only async-signal-safe syscalls
(`prctl`) are allowed.

Allowlist is currently **static** and covers basic I/O, memory management,
and process teardown. Jobs that need unusual syscalls (GPU ioctls, raw
sockets, perf counters) will be killed with `kill_reason="Seccomp"`. Making
the filter configurable per-job is expected to land alongside the ML
pipeline GPU work.

### 4.3 What the runtime does **not** do

- **Artifact staging**. Input download and output upload live in the Go
  staging layer. The runtime receives only the final `working_dir` + env
  and has no awareness of artifact URIs. This keeps the hot path small
  and means artifact backends can change without touching Rust.
- **Command allowlist**. Enforced upstream in GoRuntime
  (`HELION_ALLOWED_COMMANDS`) and is not currently mirrored on the Rust
  path. The Rust binary trusts its caller (the node agent on the same
  host, over a UDS).
- **Output size limits**. stdout/stderr are captured fully into memory
  before being returned — a pathological job can OOM the node agent. Both
  runtimes share this behaviour; capping is a future hardening task.

---

## 5. Building and running

All Makefile targets are Linux-first:

```bash
make build-rust   # cargo build --release -p helion-runtime
make test-rust    # cargo test  -p helion-runtime
make lint-rust    # cargo clippy -p helion-runtime -- -D warnings
```

The release binary lives at `runtime-rust/target/release/helion-runtime`.
Dockerfile.node-rust builds it into the same image as the node agent.

Run standalone:

```bash
HELION_RUNTIME_SOCKET=/tmp/helion-runtime.sock \
  ./target/release/helion-runtime
```

Point a node agent at it:

```bash
HELION_RUNTIME=rust \
HELION_RUNTIME_SOCKET=/tmp/helion-runtime.sock \
  ./helion-node ...
```

---

## 6. Non-Linux behaviour

The executor compiles on macOS/Windows so `cargo test` runs in CI on
every platform. cgroup + seccomp paths are gated behind
`#[cfg(target_os = "linux")]`; on other platforms the runtime falls back
to plain subprocess execution (equivalent to GoRuntime). Do not ship the
Rust runtime to non-Linux production nodes — the isolation guarantees
simply aren't there.

---

## 7. Integration points you need to keep in sync

When changing `proto/runtime.proto` you must update **three** places:

1. The proto file itself.
2. `internal/runtime/ipc.go` — hand-rolled protowire encoder/decoder.
   Field numbers must match 1:1.
3. Rust side regenerates automatically via `build.rs`, but callers
   (`executor.rs`, `ipc.rs`) must consume any new fields.

Matching tests:

- [internal/runtime/ipc_test.go](../../internal/runtime/ipc_test.go) —
  `TestEncodeRunRequest*` asserts the encoder emits each field.
- [runtime-rust/src/executor.rs](../../runtime-rust/src/executor.rs) —
  unit tests under `#[cfg(test)]` cover each observable effect
  (env, timeout, working_dir, cancel, seccomp kill reason).

---

## 8. Open follow-ups

These are known gaps, not bugs — documenting so they aren't rediscovered.

- **Per-job seccomp profiles.** Needed before GPU jobs (Feature 10).
- **Bounded output capture.** A single `sh -c 'yes | head -c 10G'` will
  currently OOM both runtimes.
- **`node_selector` visibility.** Scheduling is upstream; the runtime
  doesn't need it today, but a future self-attestation flow might.
- **Structured job metadata.** Adding workflow/trace IDs to `RunRequest`
  would improve multi-step ML pipeline debugging.
