// Package runtime defines the interface through which the node agent executes
// jobs and the types shared by all implementations.
//
// Two implementations are provided:
//
//   - GoRuntime  – executes jobs as plain subprocesses (no isolation).
//     Suitable for development and platforms other than Linux.
//
//   - RustClient – forwards jobs over a Unix domain socket to the
//     helion-runtime Rust binary, which applies cgroup v2 resource limits
//     and seccomp-bpf syscall filtering before exec-ing the job command.
//
// Select the implementation via HELION_RUNTIME env var ("go" or "rust").
// When "rust", HELION_RUNTIME_SOCKET must point to the socket path.
package runtime

import "context"

// Runtime executes job processes on behalf of the node agent.
type Runtime interface {
	// Run executes the job described by req and blocks until the job
	// completes, ctx is cancelled, or the timeout elapses.
	// Run must be safe to call concurrently.
	Run(ctx context.Context, req RunRequest) (RunResult, error)

	// Cancel terminates a running job by ID. Idempotent — returns nil if the
	// job is not currently running.
	Cancel(jobID string) error

	// Close releases resources held by the runtime (sockets, goroutines).
	Close() error
}

// RunRequest describes a job to execute.
type RunRequest struct {
	JobID          string
	Command        string
	Args           []string
	Env            map[string]string
	TimeoutSeconds int64
	Limits         ResourceLimits

	// WorkingDir is the directory the runtime cd's into before exec.
	// Empty means "inherit the node agent's cwd" — the caller (typically
	// the staging layer in internal/staging) is expected to set this to
	// a per-job directory so the job cannot see another job's files.
	WorkingDir string

	// GPUs is the whole-GPU reservation for this job. When > 0 the
	// runtime claims that many device indices from its node-local
	// allocator and sets CUDA_VISIBLE_DEVICES on the subprocess env.
	// The indices are released when the subprocess exits (success,
	// failure, timeout, or context cancel).
	GPUs uint32

	// IsService marks this job as a long-running inference service
	// (feature 17). When true, the runtime skips default-timeout
	// enforcement — the process runs until the caller cancels ctx
	// or the service exits on its own. TimeoutSeconds is ignored.
	// The probe loop that watches the service's health endpoint is
	// owned by the caller (nodeserver), not the runtime, because the
	// runtime does not know the service port.
	IsService bool
}

// ResourceLimits constrains a job's CPU and memory usage via cgroup v2.
// Zero values mean "no limit".
type ResourceLimits struct {
	MemoryBytes uint64 // maximum RSS in bytes
	CPUQuotaUS  uint64 // CPU quota per period in microseconds
	CPUPeriodUS uint64 // accounting period in microseconds (default 100 ms)
}

// RunResult holds the outcome of a completed job.
type RunResult struct {
	ExitCode   int32
	Stdout     []byte
	Stderr     []byte
	Error      string
	KillReason string // "OOMKilled" | "Timeout" | "Seccomp" | ""
}