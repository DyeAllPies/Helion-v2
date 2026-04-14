package runtime

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// TestGoRuntime_WorkingDir_CdIntoIt verifies that cmd.Dir is honoured
// when RunRequest.WorkingDir is set. Uses `pwd` / `cd` to print the
// cwd and asserts it matches the requested directory.
func TestGoRuntime_WorkingDir_CdIntoIt(t *testing.T) {
	workdir := t.TempDir()

	var command string
	var args []string
	if goruntime.GOOS == "windows" {
		command = "cmd"
		args = []string{"/c", "cd"}
	} else {
		command = "/bin/pwd"
	}

	rt := NewGoRuntime()
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "wd-test",
		Command:        command,
		Args:           args,
		TimeoutSeconds: 5,
		WorkingDir:     workdir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit_code: %d, stderr: %s", res.ExitCode, res.Stderr)
	}

	got := strings.TrimSpace(string(res.Stdout))
	// Windows `cd` returns a Windows-native path; Linux `pwd` returns
	// an absolute POSIX path. Resolve both sides through filepath.Clean
	// so the comparison is platform-agnostic.
	wantClean, _ := filepath.EvalSymlinks(workdir)
	gotClean, _ := filepath.EvalSymlinks(got)
	if !strings.EqualFold(filepath.Clean(gotClean), filepath.Clean(wantClean)) {
		t.Fatalf("cwd mismatch:\n  got:  %q\n  want: %q", gotClean, wantClean)
	}
}

// TestGoRuntime_WorkingDir_Empty_InheritsNodeCwd documents the legacy
// behaviour: when WorkingDir is empty, the subprocess inherits the
// agent's cwd. Pre-step-2 jobs rely on this.
func TestGoRuntime_WorkingDir_Empty_InheritsNodeCwd(t *testing.T) {
	nodeCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var command string
	var args []string
	if goruntime.GOOS == "windows" {
		command = "cmd"
		args = []string{"/c", "cd"}
	} else {
		command = "/bin/pwd"
	}

	rt := NewGoRuntime()
	defer rt.Close()
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "wd-empty",
		Command:        command,
		Args:           args,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(string(res.Stdout))
	// Cheap substring check — avoids full path normalisation that
	// would duplicate the previous test's logic without adding signal.
	if !strings.Contains(filepath.Clean(got), filepath.Base(nodeCwd)) {
		t.Fatalf("inherited cwd not seen in output: %q (cwd=%q)", got, nodeCwd)
	}
}
