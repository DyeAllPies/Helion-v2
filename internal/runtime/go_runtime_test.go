package runtime

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func trueCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/true"
}

func falseCmd() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "exit 1"}
	}
	return "/bin/false", nil
}

func echoArgs(msg string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "echo " + msg}
	}
	return "/usr/bin/echo", []string{msg}
}

func sleepCmd(_ int) (string, []string) {
	if runtime.GOOS == "windows" {
		// ping runs without reading stdin and is killable directly (no intermediate
		// cmd.exe child), so TerminateProcess closes the stdout pipe immediately.
		return "ping", []string{"-n", "9999", "127.0.0.1"}
	}
	return "/bin/sleep", []string{"999"}
}

// TestGoRuntime_SuccessfulJob verifies a zero-exit job returns exit code 0.
func TestGoRuntime_SuccessfulJob(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "test-success",
		Command:        trueCmd(),
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code: got %d want 0", res.ExitCode)
	}
	if res.KillReason != "" {
		t.Errorf("unexpected kill_reason: %q", res.KillReason)
	}
}

// TestGoRuntime_FailingJob verifies a non-zero exit is reported correctly.
func TestGoRuntime_FailingJob(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()

	cmd, args := falseCmd()
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "test-fail",
		Command:        cmd,
		Args:           args,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode == 0 {
		t.Error("expected non-zero exit code")
	}
}

// TestGoRuntime_StdoutCaptured verifies stdout is returned in RunResult.
func TestGoRuntime_StdoutCaptured(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()

	cmd, args := echoArgs("helion")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "test-stdout",
		Command:        cmd,
		Args:           args,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "helion") {
		t.Errorf("stdout %q does not contain 'helion'", res.Stdout)
	}
}

// TestGoRuntime_Timeout verifies a job that runs too long is killed with
// kill_reason="Timeout".
func TestGoRuntime_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	rt := NewGoRuntime()
	defer rt.Close()

	cmd, args := sleepCmd(9)
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "test-timeout",
		Command:        cmd,
		Args:           args,
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.KillReason != "Timeout" {
		t.Errorf("kill_reason: got %q want %q", res.KillReason, "Timeout")
	}
}

// TestGoRuntime_ContextCancelled verifies that cancelling ctx terminates the job.
func TestGoRuntime_ContextCancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cancel test in short mode")
	}
	rt := NewGoRuntime()
	defer rt.Close()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan RunResult, 1)
	cmd, args := sleepCmd(9)
	go func() {
		res, _ := rt.Run(ctx, RunRequest{
			JobID:          "test-ctx-cancel",
			Command:        cmd,
			Args:           args,
			TimeoutSeconds: 30,
		})
		done <- res
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		if res.ExitCode == 0 {
			t.Error("expected non-zero exit after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Error("Run did not return after ctx cancel")
	}
}

// TestGoRuntime_Cancel verifies that Runtime.Cancel terminates a running job.
func TestGoRuntime_Cancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cancel test in short mode")
	}
	rt := NewGoRuntime()
	defer rt.Close()

	done := make(chan RunResult, 1)
	cmd, args := sleepCmd(9)
	go func() {
		res, _ := rt.Run(context.Background(), RunRequest{
			JobID:          "test-cancel",
			Command:        cmd,
			Args:           args,
			TimeoutSeconds: 30,
		})
		done <- res
	}()

	time.Sleep(100 * time.Millisecond)
	if err := rt.Cancel("test-cancel"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-done:
		// job terminated — pass
	case <-time.After(3 * time.Second):
		t.Error("job did not terminate after Cancel")
	}
}

// TestGoRuntime_CancelIdempotent verifies cancelling a non-existent job is safe.
func TestGoRuntime_CancelIdempotent(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()
	if err := rt.Cancel("does-not-exist"); err != nil {
		t.Errorf("Cancel non-existent job: %v", err)
	}
}

// TestGoRuntime_Concurrent verifies multiple jobs run concurrently without
// interfering with each other.
func TestGoRuntime_Concurrent(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()

	const n = 5
	results := make(chan RunResult, n)
	for i := 0; i < n; i++ {
		go func(id int) {
			cmd, args := echoArgs("job")
			res, _ := rt.Run(context.Background(), RunRequest{
				JobID:          "concurrent-" + string(rune('0'+id)),
				Command:        cmd,
				Args:           args,
				TimeoutSeconds: 5,
			})
			results <- res
		}(i)
	}

	for i := 0; i < n; i++ {
		res := <-results
		if res.ExitCode != 0 {
			t.Errorf("concurrent job failed with exit_code %d", res.ExitCode)
		}
	}
}
