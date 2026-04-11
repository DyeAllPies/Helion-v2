// internal/api/helpers.go
//
// Shared helper functions used across multiple handler files.

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// writeError writes a JSON error response with the given HTTP status code.
func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: msg}); err != nil {
		slog.Warn("writeError: encode failed", slog.Any("err", err))
	}
}

// writeJSON encodes v as the response body and logs at warn on failure.
// Callers must have already set Content-Type and any non-200 status code.
// handler is the handler name, used in the failure log for debugging.
// See AUDIT 2026-04-11/L2.
func writeJSON(w http.ResponseWriter, handler string, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("response encode failed",
			slog.String("handler", handler), slog.Any("err", err))
	}
}

// jobToResponse converts a protobuf Job into the HTTP JobResponse shape.
func jobToResponse(j *cpb.Job) JobResponse {
	resp := JobResponse{
		ID:             j.ID,
		Command:        j.Command,
		Args:           j.Args,
		Env:            j.Env,
		TimeoutSeconds: j.TimeoutSeconds,
		Limits: ResourceLimits{
			MemoryBytes: j.Limits.MemoryBytes,
			CPUQuotaUS:  j.Limits.CPUQuotaUS,
			CPUPeriodUS: j.Limits.CPUPeriodUS,
		},
		Status:      j.Status.String(),
		NodeID:      j.NodeID,
		CreatedAt:   j.CreatedAt,
		Error:       j.Error,
		SubmittedBy: j.SubmittedBy, // AUDIT L1
	}
	if !j.FinishedAt.IsZero() {
		t := j.FinishedAt
		resp.FinishedAt = &t
	}
	return resp
}

// logAuditErr logs an audit-write failure at the level appropriate for the
// operation. Security-critical paths (auth, token lifecycle) use Error so that
// a broken audit store surfaces immediately in monitoring. Non-critical paths
// use Warn.
//
// AUDIT M2 (fixed): previously all audit failures were silently discarded
// (assigned to _) or logged at Warn regardless of criticality. Now security-
// critical events (auth failures, token lifecycle) log at Error.
func logAuditErr(critical bool, op string, err error) {
	if critical {
		slog.Error("audit log failed — security event may be unrecorded",
			slog.String("op", op), slog.Any("err", err))
	} else {
		slog.Warn("audit log failed", slog.String("op", op), slog.Any("err", err))
	}
}

