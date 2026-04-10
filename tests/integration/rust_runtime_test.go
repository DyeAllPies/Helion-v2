// tests/integration/rust_runtime_test.go
//
// Integration tests for the Rust runtime IPC path.
//
// These tests start the helion-runtime binary, send jobs via RustClient,
// and verify results end-to-end over a real Unix domain socket.
//
// Skipped automatically unless both conditions are met:
//   1. HELION_RUNTIME_SOCKET env var is set to the socket path.
//   2. The socket is reachable (i.e., helion-runtime is already running).
//
// To run locally:
//
//	# terminal 1
//	./runtime-rust/target/debug/helion-runtime --socket /tmp/helion-test.sock
//
//	# terminal 2
//	HELION_RUNTIME_SOCKET=/tmp/helion-test.sock \
//	  go test -v -run TestRustRuntime ./tests/integration/

package integration_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/runtime"
)

// requireRustRuntime skips the test if the Rust runtime socket is not
// available, and returns a ready RustClient.
func requireRustRuntime(t *testing.T) runtime.Runtime {
	t.Helper()
	sock := os.Getenv("HELION_RUNTIME_SOCKET")
	if sock == "" {
		t.Skip("HELION_RUNTIME_SOCKET not set — skipping Rust runtime integration test")
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Skipf("Rust runtime socket %q unreachable: %v — is helion-runtime running?", sock, err)
	}
	conn.Close()
	return runtime.NewRustClient(sock)
}

// TestRustRuntime_SuccessfulJob runs /bin/true and expects exit code 0.
func TestRustRuntime_SuccessfulJob(t *testing.T) {
	rt := requireRustRuntime(t)
	defer rt.Close()

	res, err := rt.Run(context.Background(), runtime.RunRequest{
		JobID:          "rust-success",
		Command:        "/bin/true",
		TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code: got %d want 0 (error: %q)", res.ExitCode, res.Error)
	}
	if res.KillReason != "" {
		t.Errorf("unexpected kill_reason: %q", res.KillReason)
	}
}

// TestRustRuntime_StdoutReturned verifies stdout is captured and returned.
func TestRustRuntime_StdoutReturned(t *testing.T) {
	rt := requireRustRuntime(t)
	defer rt.Close()

	res, err := rt.Run(context.Background(), runtime.RunRequest{
		JobID:          "rust-stdout",
		Command:        "/usr/bin/echo",
		Args:           []string{"hello-from-rust"},
		TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit_code: got %d want 0", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), "hello-from-rust") {
		t.Errorf("stdout %q does not contain expected string", res.Stdout)
	}
}

// TestRustRuntime_FailingJob verifies a non-zero exit is reported.
func TestRustRuntime_FailingJob(t *testing.T) {
	rt := requireRustRuntime(t)
	defer rt.Close()

	res, err := rt.Run(context.Background(), runtime.RunRequest{
		JobID:          "rust-fail",
		Command:        "/bin/false",
		TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode == 0 {
		t.Error("expected non-zero exit code for /bin/false")
	}
}

// TestRustRuntime_Timeout verifies a slow job is killed with kill_reason="Timeout".
func TestRustRuntime_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	rt := requireRustRuntime(t)
	defer rt.Close()

	res, err := rt.Run(context.Background(), runtime.RunRequest{
		JobID:          "rust-timeout",
		Command:        "/bin/sleep",
		Args:           []string{"60"},
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.KillReason != "Timeout" {
		t.Errorf("kill_reason: got %q want %q", res.KillReason, "Timeout")
	}
}

// TestRustRuntime_Cancel verifies cancelling a running job terminates it.
func TestRustRuntime_Cancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cancel test in short mode")
	}
	rt := requireRustRuntime(t)
	defer rt.Close()

	done := make(chan runtime.RunResult, 1)
	go func() {
		res, _ := rt.Run(context.Background(), runtime.RunRequest{
			JobID:          "rust-cancel",
			Command:        "/bin/sleep",
			Args:           []string{"60"},
			TimeoutSeconds: 60,
		})
		done <- res
	}()

	time.Sleep(200 * time.Millisecond)
	if err := rt.Cancel("rust-cancel"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-done:
		// job terminated — pass
	case <-time.After(5 * time.Second):
		t.Error("job did not terminate after Cancel")
	}
}

// TestRustRuntime_OOMKilled verifies a job exceeding its memory limit is
// killed with kill_reason="OOMKilled".
//
// Requires Linux with cgroup v2 and write access to /sys/fs/cgroup/helion/.
func TestRustRuntime_OOMKilled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OOM test in short mode")
	}
	rt := requireRustRuntime(t)
	defer rt.Close()

	// stress-ng --vm 1 --vm-bytes 200M attempts to allocate 200 MiB.
	// We cap memory at 64 MiB so it should be OOM-killed.
	res, err := rt.Run(context.Background(), runtime.RunRequest{
		JobID:   "rust-oom",
		Command: "stress-ng",
		Args:    []string{"--vm", "1", "--vm-bytes", "200M", "--timeout", "10s"},
		Limits: runtime.ResourceLimits{
			MemoryBytes: 64 << 20, // 64 MiB
		},
		TimeoutSeconds: 15,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.KillReason != "OOMKilled" {
		t.Logf("stdout: %s", res.Stdout)
		t.Logf("stderr: %s", res.Stderr)
		t.Errorf("kill_reason: got %q want %q (stress-ng may not be installed)", res.KillReason, "OOMKilled")
	}
}

// TestRustRuntime_SeccompViolation verifies a job calling a blocked syscall
// is killed with kill_reason="Seccomp".
//
// Uses /usr/bin/strace which calls ptrace(2), blocked by the allowlist.
// If strace is not installed the test is skipped.
func TestRustRuntime_SeccompViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping seccomp test in short mode")
	}
	rt := requireRustRuntime(t)
	defer rt.Close()

	// Check strace is available — if not, skip gracefully.
	if _, err := os.Stat("/usr/bin/strace"); os.IsNotExist(err) {
		t.Skip("/usr/bin/strace not installed — skipping seccomp test")
	}

	res, err := rt.Run(context.Background(), runtime.RunRequest{
		JobID:   "rust-seccomp",
		Command: "/usr/bin/strace",
		Args:    []string{"/bin/true"},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.KillReason != "Seccomp" {
		t.Errorf("kill_reason: got %q want %q", res.KillReason, "Seccomp")
	}
}

// TestRustRuntime_Concurrent verifies multiple concurrent jobs complete
// independently without corrupting each other's results.
func TestRustRuntime_Concurrent(t *testing.T) {
	rt := requireRustRuntime(t)
	defer rt.Close()

	const n = 5
	type result struct {
		res runtime.RunResult
		err error
	}
	results := make(chan result, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			res, err := rt.Run(context.Background(), runtime.RunRequest{
				JobID:          "rust-concurrent-" + string(rune('0'+i)),
				Command:        "/usr/bin/echo",
				Args:           []string{"job"},
				TimeoutSeconds: 10,
			})
			results <- result{res, err}
		}(i)
	}

	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("Run error: %v", r.err)
			continue
		}
		if r.res.ExitCode != 0 {
			t.Errorf("exit_code: got %d want 0", r.res.ExitCode)
		}
	}
}
