package runtime

import (
	"bytes"
	"encoding/binary"
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

func TestWriteFrame_OversizedPayload_ReturnsError(t *testing.T) {
	// Allocate a payload that exceeds maxFrameBytes (64 MiB + 1 byte).
	// The slice is allocated but never written to, so it's cheap.
	big := make([]byte, maxFrameBytes+1)
	var buf bytes.Buffer
	err := writeFrame(&buf, MsgRunRequest, big)
	if err == nil {
		t.Error("expected error for oversized payload, got nil")
	}
}

func TestReadFrame_OversizedFrame_ReturnsError(t *testing.T) {
	// Craft a header that claims the payload is maxFrameBytes+1.
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], MsgRunResponse)
	binary.BigEndian.PutUint32(hdr[4:8], maxFrameBytes+1) // exceeds limit
	buf := bytes.NewReader(hdr[:])
	_, _, err := readFrame(buf)
	if err == nil {
		t.Error("expected error for oversized frame, got nil")
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

// TestEncodeRunRequestWorkingDir verifies the working_dir string is emitted
// on field 7 (bytes wire type, tag 0x3a) when non-empty, and omitted when
// empty. Keeping the Rust runtime in sync with the staging layer depends on
// this field surviving the round trip.
func TestEncodeRunRequestWorkingDir(t *testing.T) {
	req := RunRequest{JobID: "j", Command: "c", WorkingDir: "/tmp/helion-job-abc"}
	b := encodeRunRequest(req)
	if !bytes.Contains(b, []byte("/tmp/helion-job-abc")) {
		t.Error("encoded RunRequest missing working_dir payload")
	}
	// Field 7, bytes type → tag byte = (7<<3)|2 = 0x3a.
	if !bytes.Contains(b, []byte{0x3a}) {
		t.Error("encoded RunRequest missing field-7 tag for working_dir")
	}

	// Empty WorkingDir must not emit the tag (pbString skips empties).
	req2 := RunRequest{JobID: "j", Command: "c"}
	if bytes.Contains(encodeRunRequest(req2), []byte{0x3a}) {
		t.Error("empty working_dir should not emit field-7 tag")
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

// ── skipField ─────────────────────────────────────────────────────────────────

func TestSkipField_VarintType(t *testing.T) {
	// Varint: encode a small value (1 = 0x01).
	data := []byte{0x01}
	n, err := skipField(data, 0) // VarintType = 0
	if err != nil {
		t.Fatalf("skipField varint: %v", err)
	}
	if n != 1 {
		t.Errorf("want n=1, got %d", n)
	}
}

func TestSkipField_Fixed64Type(t *testing.T) {
	data := make([]byte, 8)
	n, err := skipField(data, 1) // Fixed64Type = 1
	if err != nil {
		t.Fatalf("skipField fixed64: %v", err)
	}
	if n != 8 {
		t.Errorf("want n=8, got %d", n)
	}
}

func TestSkipField_Fixed64Type_TooShort_ReturnsError(t *testing.T) {
	data := make([]byte, 4) // only 4 bytes, need 8
	_, err := skipField(data, 1)
	if err == nil {
		t.Error("expected error for short fixed64")
	}
}

func TestSkipField_BytesType(t *testing.T) {
	// Length-prefixed: 1-byte length prefix (3) + 3 bytes payload.
	data := []byte{0x03, 'a', 'b', 'c'}
	n, err := skipField(data, 2) // BytesType = 2
	if err != nil {
		t.Fatalf("skipField bytes: %v", err)
	}
	if n != 4 {
		t.Errorf("want n=4, got %d", n)
	}
}

func TestSkipField_Fixed32Type(t *testing.T) {
	data := make([]byte, 4)
	n, err := skipField(data, 5) // Fixed32Type = 5
	if err != nil {
		t.Fatalf("skipField fixed32: %v", err)
	}
	if n != 4 {
		t.Errorf("want n=4, got %d", n)
	}
}

func TestSkipField_Fixed32Type_TooShort_ReturnsError(t *testing.T) {
	data := make([]byte, 2) // only 2 bytes, need 4
	_, err := skipField(data, 5)
	if err == nil {
		t.Error("expected error for short fixed32")
	}
}

func TestSkipField_UnknownType_ReturnsError(t *testing.T) {
	data := make([]byte, 8)
	_, err := skipField(data, 3) // StartGroup = 3, not handled
	if err == nil {
		t.Error("expected error for unknown wire type")
	}
}

// ── skipField called via decode functions with unknown field numbers ──────────

func TestDecodeRunResponse_UnknownVarintField_Skipped(t *testing.T) {
	// Field 9 (unknown), VarintType (0): tag = (9 << 3) | 0 = 72 = 0x48, value = 1
	// followed by field 1 (job_id): tag=0x0a, length=3, "abc"
	data := []byte{
		0x48, 0x01, // field 9, varint, value=1
		0x0a, 0x03, 'a', 'b', 'c', // field 1, job_id="abc"
	}
	jobID, _, err := decodeRunResponse(data)
	if err != nil {
		t.Fatalf("decodeRunResponse with unknown field: %v", err)
	}
	if jobID != "abc" {
		t.Errorf("want job_id='abc', got %q", jobID)
	}
}

func TestDecodeRunResponse_InvalidTag_ReturnsError(t *testing.T) {
	// A single byte 0xFF is an invalid varint tag (needs continuation byte).
	data := []byte{0xFF}
	_, _, err := decodeRunResponse(data)
	if err == nil {
		t.Error("expected error for invalid tag, got nil")
	}
}

func TestDecodeRunResponse_BadField1_ReturnsError(t *testing.T) {
	// Tag for field 1, BytesType (0x0a), then truncated length prefix.
	data := []byte{0x0a, 0xFF} // 0xFF alone is an invalid varint (no continuation)
	_, _, err := decodeRunResponse(data)
	if err == nil {
		t.Error("expected error for bad field 1")
	}
}

func TestDecodeRunResponse_BadField2_ReturnsError(t *testing.T) {
	// Tag for field 2, VarintType (0x10), then truncated varint.
	data := []byte{0x10, 0xFF} // 0xFF alone is an invalid varint
	_, _, err := decodeRunResponse(data)
	if err == nil {
		t.Error("expected error for bad field 2")
	}
}

func TestDecodeRunResponse_BadField5_ReturnsError(t *testing.T) {
	// Field 5 (error), BytesType (0x2a), truncated length.
	data := []byte{0x2a, 0xFF}
	_, _, err := decodeRunResponse(data)
	if err == nil {
		t.Error("expected error for bad field 5")
	}
}

func TestDecodeRunResponse_BadField6_ReturnsError(t *testing.T) {
	// Field 6 (kill_reason), BytesType (0x32), truncated.
	data := []byte{0x32, 0xFF}
	_, _, err := decodeRunResponse(data)
	if err == nil {
		t.Error("expected error for bad field 6")
	}
}

func TestDecodeCancelResponse_InvalidTag_ReturnsDefaults(t *testing.T) {
	// 0xFF alone is an invalid varint — ConsumeTag returns n < 0.
	ok, errMsg := decodeCancelResponse([]byte{0xFF})
	// Should return defaults (ok=false, errMsg="") without panic.
	if ok {
		t.Error("expected ok=false on bad tag")
	}
	_ = errMsg
}

func TestDecodeCancelResponse_UnknownField_Skipped(t *testing.T) {
	// Field 3 (unknown), VarintType (0): tag = (3 << 3) | 0 = 24 = 0x18, value = 42
	// followed by field 1 (ok): tag=0x08, value=1
	data := []byte{
		0x18, 0x2a, // field 3, varint, value=42 (unknown field)
		0x08, 0x01, // field 1, ok=true
	}
	ok, errMsg := decodeCancelResponse(data)
	if !ok {
		t.Error("expected ok=true after skipping unknown field")
	}
	if errMsg != "" {
		t.Errorf("expected empty errMsg, got %q", errMsg)
	}
}
