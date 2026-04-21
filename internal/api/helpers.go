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
// See AUDIT 2026-04-11-01/L2.
func writeJSON(w http.ResponseWriter, handler string, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("response encode failed",
			slog.String("handler", handler), slog.Any("err", err))
	}
}

// jobToResponse converts a protobuf Job into the HTTP JobResponse shape.
//
// Feature 26: env values whose key is in j.SecretKeys render as
// "[REDACTED]" here; the stored Job record keeps plaintext because
// the runtime needs it to dispatch. SecretKeys is echoed back so
// clients can render a badge next to the redacted field. Operators
// who need the real value must call
// POST /admin/jobs/{id}/reveal-secret (admin-only, audited).
func jobToResponse(j *cpb.Job) JobResponse {
	resp := JobResponse{
		ID:             j.ID,
		Command:        j.Command,
		Args:           j.Args,
		Env:            redactSecretEnv(j.Env, j.SecretKeys),
		SecretKeys:     j.SecretKeys,
		TimeoutSeconds: j.TimeoutSeconds,
		Limits: ResourceLimits{
			MemoryBytes: j.Limits.MemoryBytes,
			CPUQuotaUS:  j.Limits.CPUQuotaUS,
			CPUPeriodUS: j.Limits.CPUPeriodUS,
		},
		Status:      j.Status.String(),
		NodeID:      j.NodeID,
		Runtime:     j.Runtime,
		CreatedAt:   j.CreatedAt,
		Error:       j.Error,
		SubmittedBy:    j.SubmittedBy,    // AUDIT L1 — legacy alias, retained one release
		OwnerPrincipal: j.OwnerPrincipal, // Feature 36 — authoritative owner
	}
	if !j.DispatchedAt.IsZero() {
		t := j.DispatchedAt
		resp.DispatchedAt = &t
	}
	if !j.FinishedAt.IsZero() {
		t := j.FinishedAt
		resp.FinishedAt = &t
	}
	resp.Priority = j.Priority
	if j.Attempt > 0 {
		resp.Attempt = j.Attempt
	}
	if !j.RetryAfter.IsZero() {
		t := j.RetryAfter
		resp.RetryAfter = &t
	}
	if j.Service != nil {
		resp.Service = &ServiceSpecRequest{
			Port:            j.Service.Port,
			HealthPath:      j.Service.HealthPath,
			HealthInitialMs: j.Service.HealthInitialMS,
		}
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

