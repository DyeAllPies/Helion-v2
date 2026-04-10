// rust_client.go
//
// RustClient forwards job requests to the helion-runtime Rust binary over a
// Unix domain socket, using the protobuf-framed IPC protocol defined in
// proto/runtime.proto.
//
// Each Run call opens a new connection for that job (one job per connection).
// Cancel opens a separate short-lived connection to send a CancelRequest.
// The Rust binary handles each connection in its own async task.

package runtime

import (
	"context"
	"fmt"
	"net"
)

// RustClient implements Runtime by delegating to the Rust runtime process.
type RustClient struct {
	socketPath string
}

// NewRustClient returns a RustClient pointing at socketPath.
// The Rust helion-runtime binary must already be listening on that path.
func NewRustClient(socketPath string) *RustClient {
	return &RustClient{socketPath: socketPath}
}

// Run sends a RunRequest to the Rust runtime and blocks until the job
// completes or ctx is cancelled.
func (c *RustClient) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return RunResult{}, fmt.Errorf("runtime socket: %w", err)
	}
	defer conn.Close()

	// Close the connection when ctx is cancelled so the blocking readFrame
	// returns immediately.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	if err := writeFrame(conn, MsgRunRequest, encodeRunRequest(req)); err != nil {
		if ctx.Err() != nil {
			return RunResult{ExitCode: -1, KillReason: "Cancelled"}, nil
		}
		return RunResult{}, fmt.Errorf("runtime write RunRequest: %w", err)
	}

	msgType, data, err := readFrame(conn)
	if err != nil {
		if ctx.Err() != nil {
			return RunResult{ExitCode: -1, KillReason: "Cancelled"}, nil
		}
		return RunResult{}, fmt.Errorf("runtime read RunResponse: %w", err)
	}
	if msgType != MsgRunResponse {
		return RunResult{}, fmt.Errorf("runtime: unexpected msg_type %d (want %d)", msgType, MsgRunResponse)
	}

	_, result, err := decodeRunResponse(data)
	return result, err
}

// Cancel sends a CancelRequest to the Rust runtime.
func (c *RustClient) Cancel(jobID string) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("runtime socket: %w", err)
	}
	defer conn.Close()

	if err := writeFrame(conn, MsgCancelRequest, encodeCancelRequest(jobID)); err != nil {
		return fmt.Errorf("runtime write CancelRequest: %w", err)
	}

	msgType, data, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("runtime read CancelResponse: %w", err)
	}
	if msgType != MsgCancelResponse {
		return fmt.Errorf("runtime: unexpected msg_type %d (want %d)", msgType, MsgCancelResponse)
	}

	ok, errMsg := decodeCancelResponse(data)
	if !ok {
		return fmt.Errorf("runtime cancel: %s", errMsg)
	}
	return nil
}

// Close is a no-op; connections are short-lived per-request.
func (c *RustClient) Close() error { return nil }