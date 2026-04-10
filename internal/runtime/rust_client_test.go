// internal/runtime/rust_client_test.go
//
// Tests for RustClient — covers NewRustClient, Close, and error paths
// for Run and Cancel (dial failure when socket doesn't exist).

package runtime

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

// startMockRustServer starts a Unix socket server that handles one connection
// per call to the returned handler, responding with pre-baked frames.
// Skips the test if Unix sockets are unavailable (e.g., long path on Windows).
func startMockRustServer(t *testing.T, handler func(conn net.Conn)) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "r.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handler(conn)
		}
	}()
	return sockPath
}

// runResponsePayload encodes a minimal RunResponse proto3 payload:
// field 1 = job_id (string), field 2 = exit_code (varint 0).
func runResponsePayload(jobID string) []byte {
	return pbString(nil, 1, jobID)
}

// cancelResponseOK encodes a CancelResponse proto3 payload with ok=true.
func cancelResponseOK() []byte {
	// field 1 (ok), varint type, value=1
	return []byte{0x08, 0x01}
}

// cancelResponseFail encodes a CancelResponse with ok=false + error message.
func cancelResponseFail(msg string) []byte {
	// field 1 (ok), varint type, value=0
	b := []byte{0x08, 0x00}
	b = append(b, pbString(nil, 2, msg)...)
	return b
}

func TestNewRustClient_ReturnsNonNil(t *testing.T) {
	c := NewRustClient("/tmp/test.sock")
	if c == nil {
		t.Fatal("expected non-nil RustClient")
	}
}

func TestRustClient_Close_NoError(t *testing.T) {
	c := NewRustClient("/tmp/test.sock")
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestRustClient_Run_SocketNotExist_ReturnsError(t *testing.T) {
	c := NewRustClient("/nonexistent/path/to/runtime.sock")
	_, err := c.Run(context.Background(), RunRequest{JobID: "j1", Command: "echo"})
	if err == nil {
		t.Error("expected error when socket does not exist, got nil")
	}
}

func TestRustClient_Cancel_SocketNotExist_ReturnsError(t *testing.T) {
	c := NewRustClient("/nonexistent/path/to/runtime.sock")
	err := c.Cancel("job-1")
	if err == nil {
		t.Error("expected error when socket does not exist, got nil")
	}
}

func TestRustClient_Run_CancelledContext_SocketNotExist_ReturnsError(t *testing.T) {
	c := NewRustClient("/nonexistent/path/to/runtime.sock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := c.Run(ctx, RunRequest{JobID: "j-cancel", Command: "echo"})
	// Either error from dial or cancelled — both are non-nil.
	if err == nil {
		t.Error("expected error for cancelled context with missing socket")
	}
}

// ── mock-server tests (exercise the post-dial code paths) ─────────────────────

func TestRustClient_Run_HappyPath_ReturnsResult(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, _, err := readFrame(conn)
		if err != nil {
			return
		}
		// Respond with a valid RunResponse (exit_code=0, job_id="job-ok").
		_ = writeFrame(conn, MsgRunResponse, runResponsePayload("job-ok"))
	})

	c := NewRustClient(sockPath)
	res, err := c.Run(context.Background(), RunRequest{JobID: "job-ok", Command: "echo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code: got %d want 0", res.ExitCode)
	}
}

func TestRustClient_Run_WrongMsgType_ReturnsError(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, _, _ = readFrame(conn)
		// Respond with wrong message type (CancelResponse instead of RunResponse).
		_ = writeFrame(conn, MsgCancelResponse, cancelResponseOK())
	})

	c := NewRustClient(sockPath)
	_, err := c.Run(context.Background(), RunRequest{JobID: "job-bad", Command: "echo"})
	if err == nil {
		t.Error("expected error for wrong msg type, got nil")
	}
}

func TestRustClient_Run_ServerClosesEarly_ReturnsError(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		// Accept the connection then immediately close without responding.
		conn.Close()
	})

	c := NewRustClient(sockPath)
	_, err := c.Run(context.Background(), RunRequest{JobID: "job-eof", Command: "echo"})
	if err == nil {
		t.Error("expected error when server closes early, got nil")
	}
}

func TestRustClient_Cancel_HappyPath_NoError(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, _, _ = readFrame(conn)
		_ = writeFrame(conn, MsgCancelResponse, cancelResponseOK())
	})

	c := NewRustClient(sockPath)
	if err := c.Cancel("job-cancel"); err != nil {
		t.Errorf("Cancel: %v", err)
	}
}

func TestRustClient_Cancel_ServerReturnsNotOK_ReturnsError(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, _, _ = readFrame(conn)
		_ = writeFrame(conn, MsgCancelResponse, cancelResponseFail("job not found"))
	})

	c := NewRustClient(sockPath)
	err := c.Cancel("job-missing")
	if err == nil {
		t.Error("expected error for ok=false cancel response, got nil")
	}
}

func TestRustClient_Cancel_WrongMsgType_ReturnsError(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		defer conn.Close()
		_, _, _ = readFrame(conn)
		// Respond with RunResponse instead of CancelResponse.
		_ = writeFrame(conn, MsgRunResponse, runResponsePayload("job-x"))
	})

	c := NewRustClient(sockPath)
	err := c.Cancel("job-x")
	if err == nil {
		t.Error("expected error for wrong msg type, got nil")
	}
}

func TestRustClient_Cancel_ServerClosesEarly_ReturnsError(t *testing.T) {
	sockPath := startMockRustServer(t, func(conn net.Conn) {
		conn.Close()
	})

	c := NewRustClient(sockPath)
	err := c.Cancel("job-eof")
	if err == nil {
		t.Error("expected error when server closes early, got nil")
	}
}
