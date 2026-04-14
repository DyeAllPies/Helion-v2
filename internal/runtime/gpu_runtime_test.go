package runtime

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"
)

// printEnvVar builds a command that prints the value of the named env
// variable to stdout, in a way that reliably distinguishes
// "set to empty string" from "unset" on every platform.
//
// POSIX: `sh -c '...'` is straightforward — set vars print as their
// value (empty = blank line); unset vars also print blank.
// We don't need to distinguish "" from "unset" on POSIX for these
// tests because we always assert against the expected value, and
// the test paths that care about empty-vs-unset are the IPC tests
// on the Rust side which read the wire bytes directly.
//
// Windows: `cmd /c echo %X%` for a defined-but-empty var prints
// the literal "%X%" (cmd treats empty as undefined for the echo
// expansion), which would mis-report the security-boundary test.
// Use `if defined` instead, which returns true for any defined
// value including empty:
//
//	defined+value → "VAL=value"
//	defined+empty → "VAL="
//	undefined     → "UNDEFINED"
func printEnvVar(name string) (string, []string) {
	if goruntime.GOOS == "windows" {
		// %% escapes the percent inside the if-defined batch
		// expression so cmd doesn't try to expand at parse time.
		return "cmd", []string{"/c", "if defined " + name + " (echo VAL=%" + name + "%) else (echo UNDEFINED)"}
	}
	return "/bin/sh", []string{"-c", "echo \"$" + name + "\""}
}

// expectedEnvOutput renders what printEnvVar's stdout will look like
// for a given expected value, taking platform quirks into account.
// Use this to build assertions instead of comparing stdout against
// the raw value string.
//
// Windows quirk: cmd.exe's `if defined` treats an env var with an
// empty value as "undefined" — same shell expansion semantics as
// truly missing. From the test's POV this is functionally what we
// want for the GPU-hide path: a subprocess on Windows cannot
// distinguish "set to empty" from "unset," so a CUDA library
// inside it reads "no devices visible" regardless. We therefore
// fold defined-empty into UNDEFINED for the Windows expected
// output. Linux semantics are unchanged (shell sees empty value).
func expectedEnvOutput(value string, defined bool) string {
	if goruntime.GOOS == "windows" {
		if !defined || value == "" {
			return "UNDEFINED"
		}
		return "VAL=" + value
	}
	return value
}

// TestGoRuntime_GPURequest_SetsCudaVisibleDevices proves the device
// indices claimed from the GPUAllocator are exported as
// CUDA_VISIBLE_DEVICES on the child process env. Uses a cross-
// platform command that prints its env so we can assert over stdout,
// no real nvidia-smi touched.
func TestGoRuntime_GPURequest_SetsCudaVisibleDevices(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(4)
	defer rt.Close()

	cmd, args := printEnvVar("CUDA_VISIBLE_DEVICES")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "gpu-job",
		Command:        cmd,
		Args:           args,
		GPUs:           2,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	got := strings.TrimSpace(string(res.Stdout))
	// Lowest-index-first allocation on a fresh runtime → "0,1".
	if !strings.Contains(got, "0,1") {
		t.Fatalf("CUDA_VISIBLE_DEVICES not exported as expected; stdout=%q", got)
	}
}

// TestGoRuntime_GPURequest_ReleasedOnExit verifies the runtime's
// device allocator returns indices to the free pool when a job
// exits, so a second back-to-back job can claim the same indices
// without contention.
func TestGoRuntime_GPURequest_ReleasedOnExit(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(2)
	defer rt.Close()

	cmd := trueCmd()
	for i := 0; i < 3; i++ {
		res, err := rt.Run(context.Background(), RunRequest{
			JobID:          "back-to-back-" + string(rune('A'+i)),
			Command:        cmd,
			GPUs:           2,
			TimeoutSeconds: 5,
		})
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("iteration %d: exit=%d", i, res.ExitCode)
		}
	}
	if inUse := rt.gpus.InUse(); inUse != 0 {
		t.Fatalf("allocator leaked devices: InUse=%d", inUse)
	}
}

// TestGoRuntime_GPURequest_NoAllocator_Fails asserts a job requesting
// GPUs on a runtime constructed via the legacy zero-GPU
// NewGoRuntime() never reaches the subprocess.
func TestGoRuntime_GPURequest_NoAllocator_Fails(t *testing.T) {
	rt := NewGoRuntime() // zero-GPU allocator
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "greedy",
		Command:        trueCmd(),
		GPUs:           1,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The guard returns ExitCode -1 and an explanatory Error string
	// without ever running the subprocess.
	if res.ExitCode != -1 {
		t.Fatalf("expected exit -1, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Error, "insufficient") &&
		!strings.Contains(res.Error, "no allocator") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

// TestGoRuntime_GPURequest_OversubscribedFails prevents the node from
// accepting a job whose GPU request exceeds what the allocator can
// satisfy — the scheduler *should* have filtered this node out, but
// a race (node reports capacity, coordinator dispatches, a prior job
// still holds devices) must not silently hand out duplicate indices.
func TestGoRuntime_GPURequest_OversubscribedFails(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(2)
	defer rt.Close()

	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "greedy",
		Command:        trueCmd(),
		GPUs:           4, // more than the allocator holds
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("expected exit -1, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Error, "insufficient") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	// No device indices should have been claimed by the failed
	// allocation — allocator invariant from the unit tests applies
	// here too.
	if rt.gpus.InUse() != 0 {
		t.Fatalf("failed allocation leaked devices: InUse=%d", rt.gpus.InUse())
	}
}

// TestGoRuntime_GPURequest_UserEnvCannotOverrideAllocator pins the
// security boundary for per-job device pinning: a job that supplies
// its own CUDA_VISIBLE_DEVICES in req.Env must NOT be able to
// shadow the allocator's assignment. Without the map-based env
// build, this test fails on Linux (POSIX env precedence is first-
// set wins; the user's entry would have been appended first).
func TestGoRuntime_GPURequest_UserEnvCannotOverrideAllocator(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(4)
	defer rt.Close()

	cmd, args := printEnvVar("CUDA_VISIBLE_DEVICES")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:   "escaping-job",
		Command: cmd,
		Args:    args,
		Env: map[string]string{
			// Attacker / confused caller: try to claim devices 5,6
			// which the allocator never handed out.
			"CUDA_VISIBLE_DEVICES": "5,6",
		},
		GPUs:           2,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	got := strings.TrimSpace(string(res.Stdout))
	// Allocator handed out 0,1 (lowest-index-first on a fresh
	// runtime). The user's "5,6" must NOT have reached the
	// subprocess.
	want := expectedEnvOutput("0,1", true)
	if got != want {
		t.Fatalf("user env shadowed allocator: subprocess saw CUDA_VISIBLE_DEVICES=%q (want %q)", got, want)
	}
}

// TestGoRuntime_GPURequest_UnrelatedUserEnvPreserved confirms the
// override is *targeted* — only CUDA_VISIBLE_DEVICES gets replaced,
// everything else the caller passed makes it through.
func TestGoRuntime_GPURequest_UnrelatedUserEnvPreserved(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(2)
	defer rt.Close()

	cmd, args := printEnvVar("MY_USER_VAR")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:   "preserve-env",
		Command: cmd,
		Args:    args,
		Env: map[string]string{
			"MY_USER_VAR":          "hello",
			"CUDA_VISIBLE_DEVICES": "999", // overridden
		},
		GPUs:           1,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	want := expectedEnvOutput("hello", true)
	if got := strings.TrimSpace(string(res.Stdout)); got != want {
		t.Fatalf("MY_USER_VAR not preserved: got %q (want %q)", got, want)
	}
}

// TestGoRuntime_CPUJobOnGPUNode_GPUsHidden pins the security
// boundary the other direction: a CPU job (GPUs == 0) running on
// a GPU-equipped node MUST see CUDA_VISIBLE_DEVICES="" so it
// cannot opportunistically use devices the allocator never
// handed it. Without the explicit hide, a malicious "CPU" job
// could supply its own CUDA_VISIBLE_DEVICES and access devices
// pinned to a concurrent GPU job.
func TestGoRuntime_CPUJobOnGPUNode_GPUsHidden(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(4) // GPU-equipped node
	defer rt.Close()

	cmd, args := printEnvVar("CUDA_VISIBLE_DEVICES")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "cpu-on-gpu-host",
		Command:        cmd,
		Args:           args,
		GPUs:           0,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	got := strings.TrimSpace(string(res.Stdout))
	want := expectedEnvOutput("", true) // defined-and-empty
	if got != want {
		t.Fatalf("CPU job on GPU node saw CUDA_VISIBLE_DEVICES output %q (want %q for defined-empty)", got, want)
	}
	if rt.gpus.InUse() != 0 {
		t.Fatalf("CPU job claimed devices: InUse=%d", rt.gpus.InUse())
	}
}

// TestGoRuntime_CPUJobOnGPUNode_UserCannotOverrideHide guards the
// same escape route the GPU-job override test does, but for the
// CPU-on-GPU-node path: a malicious CPU job that supplies its own
// CUDA_VISIBLE_DEVICES="0,1,2,3" must STILL see "" because the
// runtime overrides via map assignment.
func TestGoRuntime_CPUJobOnGPUNode_UserCannotOverrideHide(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(4)
	defer rt.Close()

	cmd, args := printEnvVar("CUDA_VISIBLE_DEVICES")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:   "escape-attempt",
		Command: cmd,
		Args:    args,
		Env: map[string]string{
			// "Pretend I have access to all four devices."
			"CUDA_VISIBLE_DEVICES": "0,1,2,3",
		},
		GPUs:           0,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	got := strings.TrimSpace(string(res.Stdout))
	want := expectedEnvOutput("", true)
	if got != want {
		t.Fatalf("user env shadowed hide: subprocess saw %q (want %q for defined-empty)", got, want)
	}
}

// TestGoRuntime_CPUJobOnCPUNode_EnvUntouched documents the
// CPU-only-node policy: when the runtime has no GPU capacity, we
// leave CUDA_VISIBLE_DEVICES alone so legacy CPU workloads on
// hosts without any GPUs continue to see whatever env they always
// did. The hide is a *posture decision* for nodes that actually
// have GPUs to protect.
func TestGoRuntime_CPUJobOnCPUNode_EnvUntouched(t *testing.T) {
	rt := NewGoRuntime() // CPU-only: zero-capacity allocator
	defer rt.Close()

	cmd, args := printEnvVar("CUDA_VISIBLE_DEVICES")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:   "cpu-on-cpu-host",
		Command: cmd,
		Args:    args,
		Env: map[string]string{
			// User's value must pass through unchanged on a CPU node.
			"CUDA_VISIBLE_DEVICES": "user-value",
		},
		GPUs:           0,
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	got := strings.TrimSpace(string(res.Stdout))
	want := expectedEnvOutput("user-value", true)
	if got != want {
		t.Fatalf("CPU-only node clobbered user env: got %q (want %q)", got, want)
	}
}
