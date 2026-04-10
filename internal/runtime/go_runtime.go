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
	"os/exec"
	"sync"
	"time"
)

// GoRuntime executes jobs as plain subprocesses.
type GoRuntime struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewGoRuntime returns a GoRuntime ready to execute jobs.
func NewGoRuntime() *GoRuntime {
	return &GoRuntime{running: make(map[string]context.CancelFunc)}
}

// Run executes the job and blocks until it completes or ctx is cancelled.
func (r *GoRuntime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	jctx, cancel := context.WithTimeout(ctx, timeout)
	r.mu.Lock()
	r.running[req.JobID] = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.running, req.JobID)
		r.mu.Unlock()
	}()

	cmd := exec.CommandContext(jctx, req.Command, req.Args...)
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
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