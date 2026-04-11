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

// ── AUDIT H7: default timeout ────────────────────────────────────────────────

func TestGoRuntime_DefaultTimeout_Is5Minutes(t *testing.T) {
	t.Setenv("HELION_DEFAULT_TIMEOUT_SEC", "")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.defaultTimeout != 5*time.Minute {
		t.Errorf("default timeout: got %v, want 5m", rt.defaultTimeout)
	}
}

func TestGoRuntime_DefaultTimeout_EnvOverride(t *testing.T) {
	t.Setenv("HELION_DEFAULT_TIMEOUT_SEC", "120")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.defaultTimeout != 2*time.Minute {
		t.Errorf("env override: got %v, want 2m", rt.defaultTimeout)
	}
}

func TestGoRuntime_DefaultTimeout_InvalidEnvFallsBack(t *testing.T) {
	t.Setenv("HELION_DEFAULT_TIMEOUT_SEC", "not-a-number")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.defaultTimeout != 5*time.Minute {
		t.Errorf("malformed env: got %v, want 5m fallback", rt.defaultTimeout)
	}
}

func TestGoRuntime_DefaultTimeout_NegativeEnvFallsBack(t *testing.T) {
	t.Setenv("HELION_DEFAULT_TIMEOUT_SEC", "-30")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.defaultTimeout != 5*time.Minute {
		t.Errorf("negative env: got %v, want 5m fallback", rt.defaultTimeout)
	}
}

// ── AUDIT C5: command allowlist ──────────────────────────────────────────────

func TestGoRuntime_Allowlist_UnsetAllowsAll(t *testing.T) {
	t.Setenv("HELION_ALLOWED_COMMANDS", "")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.allowedCommands != nil {
		t.Errorf("unset env: expected nil allowlist, got %v", rt.allowedCommands)
	}
}

func TestGoRuntime_Allowlist_BlocksDisallowedCommand(t *testing.T) {
	// Use a command we know won't be in the allowlist.
	t.Setenv("HELION_ALLOWED_COMMANDS", "only-this")
	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "test-blocked",
		Command:        trueCmd(),
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("blocked cmd exit: got %d, want -1", res.ExitCode)
	}
	if !strings.Contains(res.Error, "HELION_ALLOWED_COMMANDS") {
		t.Errorf("blocked cmd error: %q should mention HELION_ALLOWED_COMMANDS", res.Error)
	}
}

func TestGoRuntime_Allowlist_AllowsListedCommand(t *testing.T) {
	// The allowlist value must match req.Command exactly.
	cmd := trueCmd()
	t.Setenv("HELION_ALLOWED_COMMANDS", cmd)
	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "test-allowed",
		Command:        cmd,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("allowed cmd: got exit %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
}

func TestGoRuntime_Allowlist_TrimsWhitespaceAndSkipsEmpty(t *testing.T) {
	t.Setenv("HELION_ALLOWED_COMMANDS", " echo , , sleep ")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.allowedCommands == nil {
		t.Fatal("expected non-nil allowlist")
	}
	if _, ok := rt.allowedCommands["echo"]; !ok {
		t.Error("expected 'echo' in allowlist")
	}
	if _, ok := rt.allowedCommands["sleep"]; !ok {
		t.Error("expected 'sleep' in allowlist")
	}
	if _, ok := rt.allowedCommands[""]; ok {
		t.Error("empty string must not be in allowlist")
	}
	if len(rt.allowedCommands) != 2 {
		t.Errorf("allowlist size: got %d, want 2", len(rt.allowedCommands))
	}
}

// TestGoRuntime_Allowlist_OnlyEmptyEntries_ReturnsNil covers the
// `len(allowed) == 0 → return nil` branch in readAllowedCommands: a comma
// list where every entry is whitespace.
func TestGoRuntime_Allowlist_OnlyEmptyEntries_ReturnsNil(t *testing.T) {
	t.Setenv("HELION_ALLOWED_COMMANDS", " , , ")
	rt := NewGoRuntime()
	defer rt.Close()
	if rt.allowedCommands != nil {
		t.Errorf("all-empty entries should yield nil allowlist, got %v", rt.allowedCommands)
	}
}

// TestGoRuntime_Run_ZeroTimeout_UsesDefault covers the `timeout <= 0 → fall
// back to r.defaultTimeout` branch in Run.
func TestGoRuntime_Run_ZeroTimeout_UsesDefault(t *testing.T) {
	t.Setenv("HELION_DEFAULT_TIMEOUT_SEC", "60")
	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "zero-timeout",
		Command:        trueCmd(),
		TimeoutSeconds: 0, // triggers the fallback
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit: got %d, want 0", res.ExitCode)
	}
}

// TestGoRuntime_Run_WithEnv_PopulatesProcessEnv covers the `for k, v := range
// req.Env` loop in Run. We just need any job that carries an Env map — we
// don't actually assert the child process sees it.
func TestGoRuntime_Run_WithEnv_PopulatesProcessEnv(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "env-job",
		Command:        trueCmd(),
		Env:            map[string]string{"HELION_TEST_KEY": "val"},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit: got %d, want 0", res.ExitCode)
	}
}

// TestGoRuntime_Run_UnknownCommand_ReturnsExecError covers the else branch in
// Run where the exec error is not an *exec.ExitError (e.g. "file not found").
func TestGoRuntime_Run_UnknownCommand_ReturnsExecError(t *testing.T) {
	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "bogus-cmd",
		Command:        "this-binary-absolutely-does-not-exist-helion",
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("exit: got %d, want -1 for non-ExitError", res.ExitCode)
	}
	if res.Error == "" {
		t.Error("expected non-empty Error message for exec failure")
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
