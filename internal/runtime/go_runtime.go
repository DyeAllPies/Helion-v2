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
}

// NewGoRuntime returns a GoRuntime ready to execute jobs. It reads
// HELION_DEFAULT_TIMEOUT_SEC and HELION_ALLOWED_COMMANDS from the environment
// once at construction; subsequent env changes do not affect the instance.
func NewGoRuntime() *GoRuntime {
	return &GoRuntime{
		running:         make(map[string]context.CancelFunc),
		defaultTimeout:  readDefaultTimeout(),
		allowedCommands: readAllowedCommands(),
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