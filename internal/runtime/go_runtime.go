// go_runtime.go
//
// GoRuntime is the default Runtime implementation. It executes jobs as plain
// OS subprocesses with no namespace isolation, cgroup limits, or seccomp
// filtering. Use it in development or when the Rust runtime binary is not
// available.

package runtime

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AUDIT H7 (fixed): default fallback for jobs submitted without an explicit
// TimeoutSeconds. Lowered from 30 min to 5 min to reduce the damage an
// unbounded job can do on a shared node. Override with HELION_DEFAULT_TIMEOUT_SEC.
const defaultJobTimeout = 5 * time.Minute

// GoRuntime executes jobs as plain subprocesses.
type GoRuntime struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc

	// defaultTimeout is applied when RunRequest.TimeoutSeconds is zero or
	// negative. Read from HELION_DEFAULT_TIMEOUT_SEC at construction time;
	// falls back to defaultJobTimeout if the env var is unset or invalid.
	defaultTimeout time.Duration

	// AUDIT C5 (fixed, runtime layer): if allowedCommands is non-nil, Run
	// rejects any command not in the set. nil means allow-all (dev mode).
	// Populated from HELION_ALLOWED_COMMANDS at construction time.
	allowedCommands map[string]struct{}

	// gpus tracks which whole-GPU device indices are currently claimed
	// by a running job on this node. nil means "GPU scheduling disabled"
	// (e.g. CPU-only node, or pre-GPU-slice binary) — a job that
	// requests GPUs on such a runtime fails fast.
	gpus *GPUAllocator
}

// NewGoRuntime returns a GoRuntime ready to execute jobs. It reads
// HELION_DEFAULT_TIMEOUT_SEC and HELION_ALLOWED_COMMANDS from the environment
// once at construction; subsequent env changes do not affect the instance.
// GPU scheduling is disabled (no allocator). Use NewGoRuntimeWithGPUs for
// nodes that will run GPU jobs.
func NewGoRuntime() *GoRuntime {
	return NewGoRuntimeWithGPUs(0)
}

// NewGoRuntimeWithGPUs returns a GoRuntime wired to a GPU allocator
// sized for totalGPUs whole devices. Pass 0 for CPU-only nodes (the
// allocator is still installed but rejects any request > 0 devices,
// which matches the scheduler's filterByGPU contract).
func NewGoRuntimeWithGPUs(totalGPUs uint32) *GoRuntime {
	return &GoRuntime{
		running:         make(map[string]context.CancelFunc),
		defaultTimeout:  readDefaultTimeout(),
		allowedCommands: readAllowedCommands(),
		gpus:            NewGPUAllocator(totalGPUs),
	}
}

// readDefaultTimeout parses HELION_DEFAULT_TIMEOUT_SEC. An unset, malformed,
// or non-positive value falls back to defaultJobTimeout (with a warning log
// for malformed values so the misconfiguration is visible).
func readDefaultTimeout() time.Duration {
	v := os.Getenv("HELION_DEFAULT_TIMEOUT_SEC")
	if v == "" {
		return defaultJobTimeout
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		slog.Warn("runtime: ignoring invalid HELION_DEFAULT_TIMEOUT_SEC",
			slog.String("value", v), slog.Any("err", err))
		return defaultJobTimeout
	}
	return time.Duration(n) * time.Second
}

// readAllowedCommands parses HELION_ALLOWED_COMMANDS. Returns nil (meaning
// allow-all) when the env var is unset or empty.
func readAllowedCommands() map[string]struct{} {
	v := os.Getenv("HELION_ALLOWED_COMMANDS")
	if v == "" {
		return nil
	}
	allowed := make(map[string]struct{})
	for _, cmd := range strings.Split(v, ",") {
		cmd = strings.TrimSpace(cmd)
		if cmd != "" {
			allowed[cmd] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

// Run executes the job and blocks until it completes or ctx is cancelled.
func (r *GoRuntime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	// AUDIT C5: enforce the command allowlist before spawning anything.
	if r.allowedCommands != nil {
		if _, ok := r.allowedCommands[req.Command]; !ok {
			return RunResult{
				ExitCode: -1,
				Error:    fmt.Sprintf("command %q is not in HELION_ALLOWED_COMMANDS", req.Command),
			}, nil
		}
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}
	// Feature 17 — service jobs run until explicitly cancelled. The
	// nodeserver's prober + Cancel RPC (or process self-exit) is the
	// only thing that should stop them.
	if req.IsService {
		timeout = 0
	}

	// Claim GPU device indices before anything else so a contention
	// failure short-circuits the whole dispatch. Held for the full
	// lifetime of the subprocess; released in the defer below so a
	// panic, timeout, or context cancel still returns the devices
	// to the free pool.
	var gpuIndices []int
	if req.GPUs > 0 {
		if r.gpus == nil {
			return RunResult{
				ExitCode: -1,
				Error:    "gpu: runtime has no allocator (node reported 0 GPUs)",
			}, nil
		}
		indices, err := r.gpus.Allocate(req.JobID, req.GPUs)
		if err != nil {
			return RunResult{
				ExitCode: -1,
				Error:    "gpu: " + err.Error(),
			}, nil
		}
		gpuIndices = indices
	}

	var jctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		jctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		// Feature 17 service job — cancel-only, no timeout.
		jctx, cancel = context.WithCancel(ctx)
	}
	r.mu.Lock()
	r.running[req.JobID] = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.running, req.JobID)
		r.mu.Unlock()
		if r.gpus != nil {
			r.gpus.Release(req.JobID)
		}
	}()

	// Build the subprocess env via a map first so a user-supplied
	// entry cannot shadow an allocator-set value through OS env
	// precedence (which is platform-dependent: POSIX says
	// first-set wins, Windows says last-set wins). Since the GPU
	// allocator's CUDA_VISIBLE_DEVICES is the security boundary
	// for per-job device pinning, we want unambiguous "ours wins"
	// semantics regardless of platform — a malicious or confused
	// caller setting their own CUDA_VISIBLE_DEVICES must not
	// escape the allocator's assignment.
	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	// CUDA_VISIBLE_DEVICES policy on this runtime — the security
	// boundary for per-job device pinning. We unconditionally own
	// this env var when the node is GPU-equipped (allocator
	// capacity > 0):
	//
	//   - GPU job (gpuIndices populated): set to the comma-
	//     separated allocator assignment.
	//   - CPU job (req.GPUs == 0) on a GPU-equipped node: set to
	//     "" so the subprocess sees zero devices via the CUDA
	//     runtime. Without this hide a malicious "CPU" job could
	//     supply its own CUDA_VISIBLE_DEVICES and access devices
	//     the allocator never handed it — escaping the per-job
	//     pinning that GPU jobs rely on.
	//   - CPU-only node (allocator capacity == 0): leave the var
	//     untouched so legacy CPU workloads on hosts without any
	//     GPUs see the same env they always did.
	switch {
	case len(gpuIndices) > 0:
		env["CUDA_VISIBLE_DEVICES"] = VisibleDevicesEnv(gpuIndices)
	case r.gpus != nil && r.gpus.Capacity() > 0:
		env["CUDA_VISIBLE_DEVICES"] = ""
	}
	cmd := exec.CommandContext(jctx, req.Command, req.Args...)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// When the staging layer has prepared a per-job working directory,
	// cd into it before exec. Empty means "inherit the node agent's cwd"
	// (legacy behaviour for jobs submitted without artifacts).
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := RunResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}

	if err == nil {
		return res, nil
	}

	if jctx.Err() == context.DeadlineExceeded {
		res.KillReason = "Timeout"
		res.Error = "job timed out"
		res.ExitCode = -1
		return res, nil
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		res.ExitCode = int32(exitErr.ExitCode())
		res.Error = err.Error()
	} else {
		res.ExitCode = -1
		res.Error = fmt.Sprintf("exec: %v", err)
	}
	return res, nil
}

// Cancel terminates a running job.
func (r *GoRuntime) Cancel(jobID string) error {
	r.mu.Lock()
	cancel, ok := r.running[jobID]
	r.mu.Unlock()
	if !ok {
		return nil // idempotent
	}
	cancel()
	return nil
}

// Close is a no-op for GoRuntime.
func (r *GoRuntime) Close() error { return nil }