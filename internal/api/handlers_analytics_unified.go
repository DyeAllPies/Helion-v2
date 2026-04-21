// internal/api/handlers_analytics_unified.go
//
// Feature 28 — read-side endpoints for the unified analytics sink.
//
// Every handler rides:
//   - authMiddleware (added by SetAnalyticsDB route registration)
//   - analyticsPreflight (rate limiter + time-range parse +
//     `analytics.query` audit event)
//   - the same `from` / `to` RFC3339 query params the existing
//     endpoints use
//
// The handlers are intentionally thin: they parse params, run one
// SQL query against the feature-28 tables, and serialise the result
// rows verbatim. Aggregation + zero-fill happens in the dashboard
// (reusing the scaffold from feature 18's analytics pass) — putting
// those here would require replicating the bucket-enumeration
// helper in Go, which the dashboard already does in TypeScript.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// ── submission-history ───────────────────────────────────────────────────────

// SubmissionHistoryRow is one row on the submission-history panel.
type SubmissionHistoryRow struct {
	ID           string    `json:"id"`
	SubmittedAt  time.Time `json:"submitted_at"`
	Actor        string    `json:"actor"`
	OperatorCN   string    `json:"operator_cn,omitempty"`
	Source       string    `json:"source"`
	Kind         string    `json:"kind"`
	ResourceID   string    `json:"resource_id"`
	DryRun       bool      `json:"dry_run"`
	Accepted     bool      `json:"accepted"`
	RejectReason string    `json:"reject_reason,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`
}

// SubmissionHistoryResponse pages result rows. `next_cursor` carries
// the submitted_at of the last row so the dashboard can implement
// "load older" without relying on OFFSET (which degrades on large
// tables).
type SubmissionHistoryResponse struct {
	Rows       []SubmissionHistoryRow `json:"rows"`
	Total      int                    `json:"total"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

func (s *Server) handleAnalyticsSubmissionHistory(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "analytics.submission_history")
	if !ok {
		return
	}
	limit := parseLimit(r, 50, 500)
	actorFilter := r.URL.Query().Get("actor")
	kindFilter := r.URL.Query().Get("kind") // optional: job | workflow
	acceptedFilterRaw := r.URL.Query().Get("accepted")

	sql := `
		SELECT id, submitted_at, actor, COALESCE(operator_cn, ''),
		       source, kind, resource_id, dry_run, accepted,
		       COALESCE(reject_reason, ''), COALESCE(user_agent, '')
		  FROM submission_history
		 WHERE submitted_at >= $1 AND submitted_at < $2`
	args := []any{from, to}
	idx := 3
	if actorFilter != "" {
		sql += fmt.Sprintf(" AND actor = $%d", idx)
		args = append(args, actorFilter)
		idx++
	}
	if kindFilter == "job" || kindFilter == "workflow" {
		sql += fmt.Sprintf(" AND kind = $%d", idx)
		args = append(args, kindFilter)
		idx++
	}
	if acceptedFilterRaw != "" {
		if acceptedFilterRaw == "true" || acceptedFilterRaw == "false" {
			sql += fmt.Sprintf(" AND accepted = $%d", idx)
			args = append(args, acceptedFilterRaw == "true")
			idx++
		}
	}
	sql += fmt.Sprintf(" ORDER BY submitted_at DESC LIMIT $%d", idx)
	args = append(args, limit+1)

	rows, err := s.analyticsDB.Query(r.Context(), sql, args...)
	if err != nil {
		slog.Error("submission-history query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var out []SubmissionHistoryRow
	for rows.Next() {
		var row SubmissionHistoryRow
		if err := rows.Scan(&row.ID, &row.SubmittedAt, &row.Actor, &row.OperatorCN,
			&row.Source, &row.Kind, &row.ResourceID, &row.DryRun, &row.Accepted,
			&row.RejectReason, &row.UserAgent); err != nil {
			slog.Error("submission-history scan", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "row iter failed")
		return
	}

	resp := SubmissionHistoryResponse{Rows: out, Total: len(out)}
	if len(out) > limit {
		// We fetched limit+1 to detect the "more available" case.
		resp.Rows = out[:limit]
		resp.Total = limit
		resp.NextCursor = out[limit-1].SubmittedAt.Format(time.RFC3339Nano)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsSubmissionHistory", resp)
}

// ── auth-events ───────────────────────────────────────────────────────────────

type AuthEventRow struct {
	OccurredAt time.Time `json:"occurred_at"`
	EventType  string    `json:"event_type"`
	Actor      string    `json:"actor,omitempty"`
	RemoteIP   string    `json:"remote_ip,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

type AuthEventsResponse struct {
	Rows  []AuthEventRow `json:"rows"`
	Total int            `json:"total"`
}

func (s *Server) handleAnalyticsAuthEvents(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "analytics.auth_events")
	if !ok {
		return
	}
	limit := parseLimit(r, 100, 1000)
	eventTypeFilter := r.URL.Query().Get("event_type")

	sql := `
		SELECT occurred_at, event_type,
		       COALESCE(actor, ''),
		       COALESCE(host(remote_ip), ''),
		       COALESCE(user_agent, ''),
		       COALESCE(reason, '')
		  FROM auth_events
		 WHERE occurred_at >= $1 AND occurred_at < $2`
	args := []any{from, to}
	idx := 3
	if eventTypeFilter != "" {
		sql += fmt.Sprintf(" AND event_type = $%d", idx)
		args = append(args, eventTypeFilter)
		idx++
	}
	sql += fmt.Sprintf(" ORDER BY occurred_at DESC LIMIT $%d", idx)
	args = append(args, limit)

	out, err := scanAuthEvents(r.Context(), s.analyticsDB, sql, args...)
	if err != nil {
		slog.Error("auth-events query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsAuthEvents", AuthEventsResponse{Rows: out, Total: len(out)})
}

func scanAuthEvents(ctx context.Context, db AnalyticsDB, sql string, args ...any) ([]AuthEventRow, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthEventRow
	for rows.Next() {
		var row AuthEventRow
		if err := rows.Scan(&row.OccurredAt, &row.EventType,
			&row.Actor, &row.RemoteIP, &row.UserAgent, &row.Reason); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ── unschedulable ────────────────────────────────────────────────────────────

type UnschedulableRow struct {
	OccurredAt time.Time `json:"occurred_at"`
	JobID      string    `json:"job_id"`
	Reason     string    `json:"reason"`
	Selector   string    `json:"selector"` // JSON string; dashboard parses for display
}

type UnschedulableResponse struct {
	Rows  []UnschedulableRow `json:"rows"`
	Total int                `json:"total"`
}

func (s *Server) handleAnalyticsUnschedulable(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "analytics.unschedulable")
	if !ok {
		return
	}
	limit := parseLimit(r, 100, 500)

	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT occurred_at, job_id, reason, selector::text
		  FROM unschedulable_events
		 WHERE occurred_at >= $1 AND occurred_at < $2
		 ORDER BY occurred_at DESC
		 LIMIT $3
	`, from, to, limit)
	if err != nil {
		slog.Error("unschedulable query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var out []UnschedulableRow
	for rows.Next() {
		var row UnschedulableRow
		if err := rows.Scan(&row.OccurredAt, &row.JobID, &row.Reason, &row.Selector); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsUnschedulable", UnschedulableResponse{Rows: out, Total: len(out)})
}

// ── registry-growth ──────────────────────────────────────────────────────────

type RegistryGrowthRow struct {
	Day   string `json:"day"`    // YYYY-MM-DD
	Kind  string `json:"kind"`   // dataset | model
	Net   int    `json:"net"`    // registered - deleted on that day
	Added int    `json:"added"`
}

type RegistryGrowthResponse struct {
	Rows []RegistryGrowthRow `json:"rows"`
}

func (s *Server) handleAnalyticsRegistryGrowth(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "analytics.registry_growth")
	if !ok {
		return
	}
	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT to_char(date_trunc('day', occurred_at), 'YYYY-MM-DD') AS day,
		       kind,
		       SUM(CASE WHEN action = 'registered' THEN 1 WHEN action = 'deleted' THEN -1 ELSE 0 END) AS net,
		       SUM(CASE WHEN action = 'registered' THEN 1 ELSE 0 END) AS added
		  FROM registry_mutations
		 WHERE occurred_at >= $1 AND occurred_at < $2
		 GROUP BY day, kind
		 ORDER BY day ASC, kind ASC
	`, from, to)
	if err != nil {
		slog.Error("registry-growth query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var out []RegistryGrowthRow
	for rows.Next() {
		var row RegistryGrowthRow
		if err := rows.Scan(&row.Day, &row.Kind, &row.Net, &row.Added); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsRegistryGrowth", RegistryGrowthResponse{Rows: out})
}

// ── service-probe ────────────────────────────────────────────────────────────

type ServiceProbeRow struct {
	OccurredAt        time.Time `json:"occurred_at"`
	JobID             string    `json:"job_id"`
	NewState          string    `json:"new_state"`
	ConsecutiveFails  int       `json:"consecutive_fails,omitempty"`
}

type ServiceProbeResponse struct {
	Rows  []ServiceProbeRow `json:"rows"`
	Total int               `json:"total"`
}

func (s *Server) handleAnalyticsServiceProbe(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "analytics.service_probe")
	if !ok {
		return
	}
	limit := parseLimit(r, 100, 500)
	jobID := r.URL.Query().Get("job_id")

	sql := `
		SELECT occurred_at, job_id, new_state, COALESCE(consecutive_fails, 0)
		  FROM service_probe_events
		 WHERE occurred_at >= $1 AND occurred_at < $2`
	args := []any{from, to}
	idx := 3
	if jobID != "" {
		sql += fmt.Sprintf(" AND job_id = $%d", idx)
		args = append(args, jobID)
		idx++
	}
	sql += fmt.Sprintf(" ORDER BY occurred_at DESC LIMIT $%d", idx)
	args = append(args, limit)

	rows, err := s.analyticsDB.Query(r.Context(), sql, args...)
	if err != nil {
		slog.Error("service-probe query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var out []ServiceProbeRow
	for rows.Next() {
		var row ServiceProbeRow
		if err := rows.Scan(&row.OccurredAt, &row.JobID, &row.NewState, &row.ConsecutiveFails); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsServiceProbe", ServiceProbeResponse{Rows: out, Total: len(out)})
}

// ── artifact-throughput ──────────────────────────────────────────────────────

type ArtifactThroughputRow struct {
	Bucket    time.Time `json:"bucket"`
	Direction string    `json:"direction"`
	Bytes     int64     `json:"bytes"`
	Count     int       `json:"count"`
}

type ArtifactThroughputResponse struct {
	Rows []ArtifactThroughputRow `json:"rows"`
}

func (s *Server) handleAnalyticsArtifactThroughput(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "analytics.artifact_throughput")
	if !ok {
		return
	}
	bucket := bucketFromQuery(r)
	rows, err := s.analyticsDB.Query(r.Context(), fmt.Sprintf(`
		SELECT date_trunc('%s', occurred_at) AS bucket,
		       direction,
		       COALESCE(SUM(bytes), 0) AS bytes,
		       COUNT(*) AS count
		  FROM artifact_transfers
		 WHERE occurred_at >= $1 AND occurred_at < $2
		 GROUP BY bucket, direction
		 ORDER BY bucket ASC, direction ASC
	`, bucket), from, to)
	if err != nil {
		slog.Error("artifact-throughput query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var out []ArtifactThroughputRow
	for rows.Next() {
		var row ArtifactThroughputRow
		if err := rows.Scan(&row.Bucket, &row.Direction, &row.Bytes, &row.Count); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsArtifactThroughput", ArtifactThroughputResponse{Rows: out})
}

// ── job-logs (feature 28 — Postgres-backed log retrieval) ──────────────────

type JobLogRow struct {
	OccurredAt time.Time `json:"occurred_at"`
	Seq        int64     `json:"seq"`
	Data       string    `json:"data"`
}

type JobLogsAnalyticsResponse struct {
	JobID string      `json:"job_id"`
	Rows  []JobLogRow `json:"rows"`
	Total int         `json:"total"`
}

// handleAnalyticsJobLogs serves log lines for one job from the
// PostgreSQL `job_log_entries` table.
//
// Complementary to the existing GET /jobs/{id}/logs endpoint which
// reads from BadgerDB. Once the PG store has a full retention
// window of coverage, a follow-up slice can switch the primary
// read path over and relax BadgerDB's TTL.
func (s *Server) handleAnalyticsJobLogs(w http.ResponseWriter, r *http.Request) {
	_, _, _, ok := s.analyticsPreflight(w, r, "analytics.job_logs")
	if !ok {
		return
	}
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}
	limit := parseLimit(r, 500, 5000)

	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT occurred_at, seq, data
		  FROM job_log_entries
		 WHERE job_id = $1
		 ORDER BY seq ASC
		 LIMIT $2
	`, jobID, limit)
	if err != nil {
		slog.Error("job-logs query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var out []JobLogRow
	for rows.Next() {
		var row JobLogRow
		if err := rows.Scan(&row.OccurredAt, &row.Seq, &row.Data); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsJobLogs", JobLogsAnalyticsResponse{
		JobID: jobID, Rows: out, Total: len(out),
	})
}

// ── ml-runs (feature 40) ──────────────────────────────────────────────────
//
// Dashboard-facing rollup of the workflow_outcomes table. One row
// per workflow run — the ML demo spec asserts against this
// endpoint once the MNIST parallel pipeline completes.
//
// Returns rows descending by completed_at so the most recent run
// lands first. The ?limit= query param follows the same shape
// feature-28 uses (default 50, cap 500).

type MLRunRow struct {
	WorkflowID     string            `json:"workflow_id"`
	Status         string            `json:"status"`
	CompletedAt    time.Time         `json:"completed_at"`
	JobCount       int               `json:"job_count"`
	SuccessCount   int               `json:"success_count"`
	FailedCount    int               `json:"failed_count"`
	FailedJob      string            `json:"failed_job,omitempty"`
	OwnerPrincipal string            `json:"owner_principal,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	// Feature 40c — nullable on the server side (the rollup
	// never starts a run). omitempty keeps the JSON shape small
	// for the "never started" case where both fields are absent.
	StartedAt  *time.Time `json:"started_at,omitempty"`
	DurationMs *int64     `json:"duration_ms,omitempty"`
}

type MLRunsResponse struct {
	Rows  []MLRunRow `json:"rows"`
	Total int        `json:"total"`
}

func (s *Server) handleAnalyticsMLRuns(w http.ResponseWriter, r *http.Request) {
	_, _, _, ok := s.analyticsPreflight(w, r, "analytics.ml_runs")
	if !ok {
		return
	}
	limit := parseLimit(r, 50, 500)

	// No mandatory time-range filter here because a demo run might
	// complete seconds before the dashboard queries, and clamping
	// to the default 24h window would risk the "just submitted"
	// row missing from the "since yesterday" range on a fresh
	// test-cluster volume where clock skew matters.
	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT workflow_id, status, completed_at,
		       job_count, success_count, failed_count,
		       COALESCE(failed_job, ''),
		       COALESCE(owner_principal, ''),
		       COALESCE(tags, '{}'::JSONB),
		       started_at, duration_ms
		  FROM workflow_outcomes
		 ORDER BY completed_at DESC
		 LIMIT $1
	`, limit)
	if err != nil {
		slog.Error("ml-runs query", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var out []MLRunRow
	for rows.Next() {
		var row MLRunRow
		var tagsRaw []byte
		// Feature 40c — started_at + duration_ms are nullable in
		// the schema. Scan into pointer types so SQL NULL stays
		// JSON-absent rather than rendering as 0 / epoch.
		var startedAt *time.Time
		var durationMs *int64
		if err := rows.Scan(
			&row.WorkflowID, &row.Status, &row.CompletedAt,
			&row.JobCount, &row.SuccessCount, &row.FailedCount,
			&row.FailedJob, &row.OwnerPrincipal, &tagsRaw,
			&startedAt, &durationMs,
		); err != nil {
			slog.Error("ml-runs scan", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		if len(tagsRaw) > 0 {
			// Tolerant JSONB decode: bad-shape JSONB should be
			// treated as no-tags rather than killing the response.
			var t map[string]string
			if err := json.Unmarshal(tagsRaw, &t); err == nil && len(t) > 0 {
				row.Tags = t
			}
		}
		row.StartedAt = startedAt
		row.DurationMs = durationMs
		out = append(out, row)
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsMLRuns", MLRunsResponse{Rows: out, Total: len(out)})
}

// ── shared helpers ──────────────────────────────────────────────────────────

// parseLimit returns the clamped limit from the ?limit= query
// param. Defaults to def; caps at max so a malicious client can't
// ask for a million rows.
func parseLimit(r *http.Request, def, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// bucketFromQuery returns the date_trunc bucket string for the
// ?bucket= query param. Accepted values match the existing
// throughput endpoint so the dashboard has one bucketing model.
func bucketFromQuery(r *http.Request) string {
	b := r.URL.Query().Get("bucket")
	switch b {
	case "second", "minute", "hour", "day":
		return b
	default:
		return "minute"
	}
}
