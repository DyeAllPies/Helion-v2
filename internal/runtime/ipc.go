// ipc.go
//
// Wire protocol helpers for the Go ↔ Rust Unix socket IPC.
//
// Frame format (all integers big-endian):
//
//	[4 bytes msg_type][4 bytes payload_length][payload_length bytes payload]
//
// The payload is a proto3 wire-encoded message matching runtime.proto.
// Field numbers in the encoding functions below correspond 1-to-1 with the
// proto field numbers in proto/runtime.proto.
//
// Manual encoding with protowire avoids a protoc codegen step for this
// internal IPC path. The wire format is byte-for-byte compatible with
// prost-decoded messages on the Rust side.

package runtime

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protowire"
)

// Message type constants (must match runtime-rust/src/ipc.rs).
const (
	MsgRunRequest     = uint32(1)
	MsgRunResponse    = uint32(2)
	MsgCancelRequest  = uint32(3)
	MsgCancelResponse = uint32(4)
)

const maxFrameBytes = 64 << 20 // 64 MiB

// writeFrame writes a framed message to w.
func writeFrame(w io.Writer, msgType uint32, payload []byte) error {
	if len(payload) > maxFrameBytes {
		return fmt.Errorf("ipc: payload too large (%d bytes)", len(payload))
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], msgType)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// readFrame reads the next framed message from r.
func readFrame(r io.Reader) (msgType uint32, payload []byte, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	msgType = binary.BigEndian.Uint32(hdr[0:4])
	size := binary.BigEndian.Uint32(hdr[4:8])
	if size > maxFrameBytes {
		return 0, nil, fmt.Errorf("ipc: frame too large (%d bytes)", size)
	}
	payload = make([]byte, size)
	_, err = io.ReadFull(r, payload)
	return msgType, payload, err
}

// ── proto3 encoding ───────────────────────────────────────────────────────────

// encodeRunRequest serialises a RunRequest into proto3 wire format.
// Field numbers match proto/runtime.proto message RunRequest.
func encodeRunRequest(req RunRequest) []byte {
	var b []byte
	b = pbString(b, 1, req.JobID)
	b = pbString(b, 2, req.Command)
	for _, arg := range req.Args {
		b = pbString(b, 3, arg)
	}
	for k, v := range req.Env {
		// map entry is a nested message: field 1 = key, field 2 = value
		entry := pbString(nil, 1, k)
		entry = pbString(entry, 2, v)
		b = protowire.AppendTag(b, 4, protowire.BytesType)
		b = protowire.AppendBytes(b, entry)
	}
	if req.TimeoutSeconds != 0 {
		b = protowire.AppendTag(b, 5, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(req.TimeoutSeconds))
	}
	lim := req.Limits
	if lim.MemoryBytes != 0 || lim.CPUQuotaUS != 0 || lim.CPUPeriodUS != 0 {
		var sub []byte
		if lim.MemoryBytes != 0 {
			sub = protowire.AppendTag(sub, 1, protowire.VarintType)
			sub = protowire.AppendVarint(sub, lim.MemoryBytes)
		}
		if lim.CPUQuotaUS != 0 {
			sub = protowire.AppendTag(sub, 2, protowire.VarintType)
			sub = protowire.AppendVarint(sub, lim.CPUQuotaUS)
		}
		if lim.CPUPeriodUS != 0 {
			sub = protowire.AppendTag(sub, 3, protowire.VarintType)
			sub = protowire.AppendVarint(sub, lim.CPUPeriodUS)
		}
		b = protowire.AppendTag(b, 6, protowire.BytesType)
		b = protowire.AppendBytes(b, sub)
	}
	return b
}

// decodeRunResponse parses a RunResponse from proto3 wire bytes.
// Field numbers match proto/runtime.proto message RunResponse.
func decodeRunResponse(data []byte) (jobID string, res RunResult, err error) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return jobID, res, fmt.Errorf("ipc: invalid tag")
		}
		data = data[n:]
		switch num {
		case 1: // job_id
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return jobID, res, fmt.Errorf("ipc: bad field 1")
			}
			jobID = string(v)
			data = data[n:]
		case 2: // exit_code (int32 as varint)
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return jobID, res, fmt.Errorf("ipc: bad field 2")
			}
			res.ExitCode = int32(v)
			data = data[n:]
		case 3: // stdout
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return jobID, res, fmt.Errorf("ipc: bad field 3")
			}
			res.Stdout = append(res.Stdout, v...)
			data = data[n:]
		case 4: // stderr
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return jobID, res, fmt.Errorf("ipc: bad field 4")
			}
			res.Stderr = append(res.Stderr, v...)
			data = data[n:]
		case 5: // error
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return jobID, res, fmt.Errorf("ipc: bad field 5")
			}
			res.Error = string(v)
			data = data[n:]
		case 6: // kill_reason
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return jobID, res, fmt.Errorf("ipc: bad field 6")
			}
			res.KillReason = string(v)
			data = data[n:]
		default:
			n, serr := skipField(data, typ)
			if serr != nil {
				return jobID, res, serr
			}
			data = data[n:]
		}
	}
	return jobID, res, nil
}

// encodeCancelRequest serialises a CancelRequest.
func encodeCancelRequest(jobID string) []byte {
	return pbString(nil, 1, jobID)
}

// decodeCancelResponse parses a CancelResponse.
func decodeCancelResponse(data []byte) (ok bool, errMsg string) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		switch num {
		case 1: // ok (bool = varint)
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return ok, errMsg
			}
			ok = v != 0
			data = data[n:]
		case 2: // error
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return ok, errMsg
			}
			errMsg = string(v)
			data = data[n:]
		default:
			n, serr := skipField(data, typ)
			if serr != nil {
				return ok, errMsg
			}
			data = data[n:]
		}
	}
	return ok, errMsg
}

// ── helpers ───────────────────────────────────────────────────────────────────

func pbString(b []byte, field protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func skipField(data []byte, typ protowire.Type) (int, error) {
	switch typ {
	case protowire.VarintType:
		_, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return 0, fmt.Errorf("ipc: bad varint")
		}
		return n, nil
	case protowire.Fixed32Type:
		if len(data) < 4 {
			return 0, fmt.Errorf("ipc: short fixed32")
		}
		return 4, nil
	case protowire.Fixed64Type:
		if len(data) < 8 {
			return 0, fmt.Errorf("ipc: short fixed64")
		}
		return 8, nil
	case protowire.BytesType:
		_, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return 0, fmt.Errorf("ipc: bad bytes field")
		}
		return n, nil
	default:
		return 0, fmt.Errorf("ipc: unknown wire type %v", typ)
	}
}