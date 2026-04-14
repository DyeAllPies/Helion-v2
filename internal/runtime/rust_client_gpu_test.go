package runtime

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

// Rust-side GPU plumbing lives entirely Go-side: RustClient claims
// device indices from a shared GPUAllocator before IPC and stamps
// CUDA_VISIBLE_DEVICES into req.Env. The Rust executor inherits the
// env unchanged (no proto/runtime.proto changes, no executor.rs
// changes). These tests pin that contract by spying on the encoded
// IPC frame the client sends — confirming the env carries the
// expected CUDA_VISIBLE_DEVICES value without needing a real Rust
// binary.

// captureRunRequest starts a mock Rust server that echoes back a
// RunResponse and stashes the encoded RunRequest payload it
// received on `out`. Returns the socket path the client should
// dial. Reuses startMockRustServer's skip-on-windows behaviour.
func captureRunRequest(t *testing.T, out chan<- []byte) string {
	t.Helper()
	return startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		out <- payload
		_ = writeFrame(conn, MsgRunResponse, runResponsePayload("job"))
	})
}

// envFromRunRequestPayload pulls the env map out of an encoded
// RunRequest payload. The encoding is repeated proto3 KvPair messages
// at field 4; we don't have a decoder symbol exposed, so this test
// helper does a lightweight scan looking specifically for the env
// var by name.
func envContains(payload []byte, key string) (string, bool) {
	// The encoder builds env entries as nested length-delimited
	// messages with the key as field 1 and value as field 2. A
	// substring search on the wire bytes is brittle but adequate
	// for a confidence check that the value made it through —
	// the key-value pair shows up as a contiguous "key" string then
	// "value" string, both LF-prefixed by the proto wire varint
	// length. Searching for the key followed shortly by what looks
	// like the value covers this for our use.
	idx := strings.Index(string(payload), key)
	if idx < 0 {
		return "", false
	}
	tail := payload[idx+len(key):]
	// Walk forward looking for any printable run after the env
	// var name; the value is the next length-prefixed string.
	for i := 0; i < len(tail); i++ {
		c := tail[i]
		if c >= 0x20 && c < 0x7f {
			// Found the start of a printable run — capture until
			// the next non-printable.
			j := i
			for j < len(tail) && tail[j] >= 0x20 && tail[j] < 0x7f {
				j++
			}
			return string(tail[i:j]), true
		}
	}
	return "", false
}

func TestRustClient_GPURequest_StampsEnvBeforeIPC(t *testing.T) {
	captured := make(chan []byte, 1)
	sock := captureRunRequest(t, captured)

	c := NewRustClientWithGPUs(sock, 4)
	res, err := c.Run(context.Background(), RunRequest{
		JobID:   "gpu-job",
		Command: "echo",
		GPUs:    2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}

	payload := <-captured
	got, ok := envContains(payload, "CUDA_VISIBLE_DEVICES")
	if !ok {
		t.Fatalf("CUDA_VISIBLE_DEVICES not found in IPC payload (len=%d)", len(payload))
	}
	// Lowest-index-first allocation on a fresh allocator → "0,1".
	if !strings.Contains(got, "0,1") {
		t.Fatalf("expected indices 0,1, got %q", got)
	}
}

func TestRustClient_NonGPUJob_DoesNotStampEnv(t *testing.T) {
	captured := make(chan []byte, 1)
	sock := captureRunRequest(t, captured)

	c := NewRustClientWithGPUs(sock, 4)
	_, err := c.Run(context.Background(), RunRequest{
		JobID:   "cpu-job",
		Command: "echo",
		GPUs:    0,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	payload := <-captured
	if _, ok := envContains(payload, "CUDA_VISIBLE_DEVICES"); ok {
		t.Fatal("CPU job's IPC payload should not carry CUDA_VISIBLE_DEVICES")
	}
}

// TestRustClient_GPURequest_NoCapacity_FailsBeforeIPC asserts that a
// runtime constructed via the legacy NewRustClient (zero-capacity
// allocator under the hood) rejects GPU jobs without ever opening
// the socket. Mirrors the equivalent guard on the Go runtime — the
// error message says "insufficient" because the allocator exists but
// holds zero devices, not "no allocator", which is fine: same effect
// (fail before IPC, no subprocess) with a slightly more specific
// reason.
func TestRustClient_GPURequest_NoCapacity_FailsBeforeIPC(t *testing.T) {
	// Note no socket — if the guard is correct, dial never happens.
	c := NewRustClient("/nonexistent.sock")
	res, err := c.Run(context.Background(), RunRequest{
		JobID:   "greedy",
		Command: "echo",
		GPUs:    1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("expected exit -1, got %d (Run hit IPC despite no GPU capacity)", res.ExitCode)
	}
	if !strings.Contains(res.Error, "insufficient") &&
		!strings.Contains(res.Error, "no allocator") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestRustClient_GPURequest_OversubscribedFailsBeforeIPC(t *testing.T) {
	// Allocator sized for 2 devices; request 4. Must fail before IPC
	// (no socket needed for this case either, but use a mock to be
	// extra sure no second connection is attempted).
	called := 0
	var mu sync.Mutex
	sock := startMockRustServer(t, func(conn net.Conn) {
		mu.Lock()
		called++
		mu.Unlock()
		conn.Close()
	})

	c := NewRustClientWithGPUs(sock, 2)
	res, err := c.Run(context.Background(), RunRequest{
		JobID:   "greedy",
		Command: "echo",
		GPUs:    4,
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
	mu.Lock()
	defer mu.Unlock()
	if called != 0 {
		t.Fatalf("dial happened despite allocation failure (count=%d)", called)
	}
	if c.gpus.InUse() != 0 {
		t.Fatalf("allocator leaked devices: InUse=%d", c.gpus.InUse())
	}
}

// TestRustClient_GPURequest_ReleasedOnExit verifies the per-job
// device claim is returned to the free pool when Run finishes,
// mirroring the GoRuntime invariant.
func TestRustClient_GPURequest_ReleasedOnExit(t *testing.T) {
	sock := startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, _, _ = readFrame(conn)
		_ = writeFrame(conn, MsgRunResponse, runResponsePayload("ok"))
	})

	c := NewRustClientWithGPUs(sock, 2)
	for i := 0; i < 3; i++ {
		_, err := c.Run(context.Background(), RunRequest{
			JobID:   "release-test-" + string(rune('A'+i)),
			Command: "echo",
			GPUs:    2,
		})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if n := c.gpus.InUse(); n != 0 {
		t.Fatalf("allocator leaked: InUse=%d", n)
	}
}
