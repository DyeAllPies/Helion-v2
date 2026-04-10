package runtime

import (
	"bytes"
	"testing"
)

// ── frame round-trip ──────────────────────────────────────────────────────────

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte("hello protobuf")
	var buf bytes.Buffer

	if err := writeFrame(&buf, MsgRunRequest, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	msgType, got, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if msgType != MsgRunRequest {
		t.Errorf("msg_type: got %d want %d", msgType, MsgRunRequest)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %q want %q", got, payload)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, MsgCancelResponse, nil); err != nil {
		t.Fatalf("writeFrame empty: %v", err)
	}
	msgType, got, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame empty: %v", err)
	}
	if msgType != MsgCancelResponse {
		t.Errorf("msg_type: got %d want %d", msgType, MsgCancelResponse)
	}
	if len(got) != 0 {
		t.Errorf("expected empty payload, got %d bytes", len(got))
	}
}

func TestFrameMultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	msgs := []struct {
		typ     uint32
		payload []byte
	}{
		{MsgRunRequest, []byte("request")},
		{MsgRunResponse, []byte("response")},
		{MsgCancelRequest, []byte("cancel")},
	}
	for _, m := range msgs {
		if err := writeFrame(&buf, m.typ, m.payload); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}
	for _, m := range msgs {
		typ, data, err := readFrame(&buf)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if typ != m.typ {
			t.Errorf("type: got %d want %d", typ, m.typ)
		}
		if !bytes.Equal(data, m.payload) {
			t.Errorf("payload: got %q want %q", data, m.payload)
		}
	}
}

// ── RunRequest encode ─────────────────────────────────────────────────────────

func TestEncodeRunRequestContainsFields(t *testing.T) {
	req := RunRequest{
		JobID:          "job-123",
		Command:        "/bin/echo",
		Args:           []string{"hello", "world"},
		Env:            map[string]string{"FOO": "bar"},
		TimeoutSeconds: 30,
		Limits: ResourceLimits{
			MemoryBytes: 128 << 20,
			CPUQuotaUS:  50_000,
			CPUPeriodUS: 100_000,
		},
	}
	b := encodeRunRequest(req)
	if len(b) == 0 {
		t.Fatal("encodeRunRequest returned empty bytes")
	}
	for _, want := range []string{"job-123", "/bin/echo", "hello", "world", "FOO", "bar"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("encoded RunRequest missing %q", want)
		}
	}
}

func TestEncodeRunRequestNoLimits(t *testing.T) {
	req := RunRequest{JobID: "j1", Command: "cmd"}
	b := encodeRunRequest(req)
	if len(b) == 0 {
		t.Fatal("encodeRunRequest with no limits returned empty bytes")
	}
}

// ── RunResponse decode ────────────────────────────────────────────────────────

func TestDecodeRunResponseFull(t *testing.T) {
	// Hand-crafted proto3 wire encoding for RunResponse:
	//   field 1 (job_id):      "test-job"  → tag 0x0a, len 8
	//   field 2 (exit_code):   42          → tag 0x10, varint 42 (0x2a)
	//   field 6 (kill_reason): "Timeout"   → tag 0x32, len 7
	data := []byte{
		0x0a, 0x08, 't', 'e', 's', 't', '-', 'j', 'o', 'b',
		0x10, 0x2a,
		0x32, 0x07, 'T', 'i', 'm', 'e', 'o', 'u', 't',
	}
	jobID, res, err := decodeRunResponse(data)
	if err != nil {
		t.Fatalf("decodeRunResponse: %v", err)
	}
	if jobID != "test-job" {
		t.Errorf("job_id: got %q want %q", jobID, "test-job")
	}
	if res.ExitCode != 42 {
		t.Errorf("exit_code: got %d want 42", res.ExitCode)
	}
	if res.KillReason != "Timeout" {
		t.Errorf("kill_reason: got %q want %q", res.KillReason, "Timeout")
	}
}

func TestDecodeRunResponseStdoutStderr(t *testing.T) {
	// field 3 (stdout): "out", field 4 (stderr): "err"
	data := []byte{
		0x1a, 0x03, 'o', 'u', 't', // field 3
		0x22, 0x03, 'e', 'r', 'r', // field 4
	}
	_, res, err := decodeRunResponse(data)
	if err != nil {
		t.Fatalf("decodeRunResponse: %v", err)
	}
	if string(res.Stdout) != "out" {
		t.Errorf("stdout: got %q want %q", res.Stdout, "out")
	}
	if string(res.Stderr) != "err" {
		t.Errorf("stderr: got %q want %q", res.Stderr, "err")
	}
}

func TestDecodeRunResponseEmpty(t *testing.T) {
	jobID, res, err := decodeRunResponse(nil)
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if jobID != "" {
		t.Errorf("expected empty job_id, got %q", jobID)
	}
	if res.ExitCode != 0 || res.KillReason != "" {
		t.Errorf("expected zero RunResult, got %+v", res)
	}
}

// ── CancelRequest encode ──────────────────────────────────────────────────────

func TestEncodeCancelRequest(t *testing.T) {
	b := encodeCancelRequest("job-xyz")
	if !bytes.Contains(b, []byte("job-xyz")) {
		t.Error("encoded CancelRequest does not contain job id")
	}
}

func TestEncodeCancelRequestEmpty(t *testing.T) {
	b := encodeCancelRequest("")
	// Empty string: pbString skips empty, so result is empty slice.
	if len(b) != 0 {
		t.Errorf("expected empty encoding for empty job_id, got %d bytes", len(b))
	}
}

// ── CancelResponse decode ─────────────────────────────────────────────────────

func TestDecodeCancelResponseOK(t *testing.T) {
	// field 1 = ok (bool true = varint 1)
	data := []byte{0x08, 0x01}
	ok, errMsg := decodeCancelResponse(data)
	if !ok {
		t.Error("expected ok=true")
	}
	if errMsg != "" {
		t.Errorf("expected empty error, got %q", errMsg)
	}
}

func TestDecodeCancelResponseFailed(t *testing.T) {
	// field 1 = false (0), field 2 = "not found"
	data := []byte{
		0x08, 0x00,
		0x12, 0x09, 'n', 'o', 't', ' ', 'f', 'o', 'u', 'n', 'd',
	}
	ok, errMsg := decodeCancelResponse(data)
	if ok {
		t.Error("expected ok=false")
	}
	if errMsg != "not found" {
		t.Errorf("error: got %q want %q", errMsg, "not found")
	}
}

func TestDecodeCancelResponseEmpty(t *testing.T) {
	ok, errMsg := decodeCancelResponse(nil)
	if ok {
		t.Error("expected ok=false for empty input")
	}
	if errMsg != "" {
		t.Errorf("expected empty error for empty input, got %q", errMsg)
	}
}
