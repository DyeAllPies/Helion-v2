package runtime

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"
)

// printEnvVar builds a command that prints the value of the named env
// variable to stdout. Cross-platform wrapper so the GPU tests can
// inspect CUDA_VISIBLE_DEVICES without needing a real GPU.
func printEnvVar(name string) (string, []string) {
	if goruntime.GOOS == "windows" {
		return "cmd", []string{"/c", "echo %" + name + "%"}
	}
	// POSIX: `sh -c 'echo "$VAR"'` is the most portable form.
	return "/bin/sh", []string{"-c", "echo \"$" + name + "\""}
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

// TestGoRuntime_NonGPUJob_OnGPURuntime_Works asserts a CPU job runs
// normally on a GPU-enabled runtime without touching the allocator
// (GPUs: 0 in the request → no claim, no env var set).
func TestGoRuntime_NonGPUJob_OnGPURuntime_Works(t *testing.T) {
	rt := NewGoRuntimeWithGPUs(4)
	defer rt.Close()

	cmd, args := printEnvVar("CUDA_VISIBLE_DEVICES")
	res, err := rt.Run(context.Background(), RunRequest{
		JobID:          "cpu-job",
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
	// The subprocess must see no CUDA_VISIBLE_DEVICES (empty echo
	// on POSIX, "%CUDA_VISIBLE_DEVICES%" literal on Windows cmd),
	// NOT a "0" or "0,1".
	got := strings.TrimSpace(string(res.Stdout))
	if strings.HasPrefix(got, "0") || got == "1" || got == "0,1" {
		t.Fatalf("CUDA_VISIBLE_DEVICES leaked to CPU job: %q", got)
	}
	if rt.gpus.InUse() != 0 {
		t.Fatalf("CPU job claimed devices: InUse=%d", rt.gpus.InUse())
	}
}
