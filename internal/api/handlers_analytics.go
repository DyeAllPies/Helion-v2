// internal/api/handlers_analytics.go
//
// Analytics dashboard API endpoints. All read-only, querying PostgreSQL.
//
//   GET /api/analytics/throughput          — hourly job throughput
//   GET /api/analytics/node-reliability    — per-node failure rates
//   GET /api/analytics/retry-effectiveness — retry vs first-attempt outcomes
//   GET /api/analytics/queue-wait          — pending → running wait times
//   GET /api/analytics/workflow-outcomes   — workflow success/failure by day
//   GET /api/analytics/events             — paginated raw event query
//
// Security stack (matches /admin/* and operational handlers):
//   1. authMiddleware      — JWT Bearer required; 401 on missing/invalid.
//   2. analyticsQueryAllow — per-subject token bucket (0.5 rps, burst 10)
//                            to prevent DoS via expensive percentile queries.
//   3. Input bounds        — time range capped at 365 days, limit capped at
//                            1000 — prevents unbounded memory/IO on PG.
//   4. Audit log           — every analytics query is recorded as
//                            `analytics.query` with actor + endpoint + range.
//   5. Generic error       — failures return "internal error"; details
//                            logged server-side only (helpers.writeError).

package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	"github.com/jackc/pgx/v5"
)

// analyticsBuckets is the allowlist of bucket widths the time-series
// analytics endpoints accept via the `bucket` query param. The values
// are spliced into a raw SQL statement via fmt.Sprintf (date_trunc is
// not parameterisable in pgx), so any caller-supplied bucket MUST go
// through parseBucketParam below — never directly into the query
// string. Adding a new bucket here is a two-step change: update this
// map AND verify Postgres' date_trunc accepts it.
var analyticsBuckets = map[string]struct{}{
	"hour":   {},
	"minute": {},
	"second": {},
}

// parseBucketParam returns a bucket string that is guaranteed to be
// in analyticsBuckets. Defaults to "hour" (preserves the original
// behaviour for callers who don't send the param). Unknown values
// fall back to the default rather than 400 — the frontend can't
// always predict which bucket it needs before seeing the data.
func parseBucketParam(r *http.Request) string {
	v := r.URL.Query().Get("bucket")
	if _, ok := analyticsBuckets[v]; ok {
		return v
	}
	return "hour"
}

// ── Query interface ──────────────────────────────────────────────────────

// AnalyticsDB is the interface the analytics handlers need from PostgreSQL.
// Satisfied by *pgx.Conn and *pgxpool.Pool.
type AnalyticsDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SetAnalyticsDB enables the analytics API endpoints.
func (s *Server) SetAnalyticsDB(db AnalyticsDB) {
	s.analyticsDB = db
	s.mux.HandleFunc("GET /api/analytics/throughput", s.authMiddleware(s.handleAnalyticsThroughput))
	s.mux.HandleFunc("GET /api/analytics/node-reliability", s.authMiddleware(s.handleAnalyticsNodeReliability))
	s.mux.HandleFunc("GET /api/analytics/retry-effectiveness", s.authMiddleware(s.handleAnalyticsRetryEffectiveness))
	s.mux.HandleFunc("GET /api/analytics/queue-wait", s.authMiddleware(s.handleAnalyticsQueueWait))
	s.mux.HandleFunc("GET /api/analytics/workflow-outcomes", s.authMiddleware(s.handleAnalyticsWorkflowOutcomes))
	s.mux.HandleFunc("GET /api/analytics/events", s.authMiddleware(s.handleAnalyticsEvents))

	// Feature 28 — unified sink endpoints. Every handler rides the
	// same authMiddleware + analyticsQueryAllow limiter + time-range
	// parser as the above. Routes live in handlers_analytics_unified.go.
	s.mux.HandleFunc("GET /api/analytics/submission-history", s.authMiddleware(s.handleAnalyticsSubmissionHistory))
	s.mux.HandleFunc("GET /api/analytics/auth-events", s.authMiddleware(s.handleAnalyticsAuthEvents))
	s.mux.HandleFunc("GET /api/analytics/unschedulable", s.authMiddleware(s.handleAnalyticsUnschedulable))
	s.mux.HandleFunc("GET /api/analytics/registry-growth", s.authMiddleware(s.handleAnalyticsRegistryGrowth))
	s.mux.HandleFunc("GET /api/analytics/service-probe", s.authMiddleware(s.handleAnalyticsServiceProbe))
	s.mux.HandleFunc("GET /api/analytics/artifact-throughput", s.authMiddleware(s.handleAnalyticsArtifactThroughput))
	s.mux.HandleFunc("GET /api/analytics/job-logs", s.authMiddleware(s.handleAnalyticsJobLogs))

	// Feature 40 — ML runs rollup. One row per terminal workflow
	// (completed | failed), sourced from the workflow_outcomes
	// denormalised table that the sink upserts on every
	// workflow.{completed,failed} event. See migration 006.
	s.mux.HandleFunc("GET /api/analytics/ml-runs", s.authMiddleware(s.handleAnalyticsMLRuns))
}

// ── Shared pre-flight ────────────────────────────────────────────────────

// analyticsPreflight runs the security checks common to every analytics
// endpoint: rate limit, time-range bounds parsing, audit logging. Returns
// the parsed range, the actor, and a boolean indicating whether to proceed.
// If ok=false, the response has already been written.
func (s *Server) analyticsPreflight(w http.ResponseWriter, r *http.Request, endpoint string) (time.Time, time.Time, string, bool) {
	// 1. Extract actor from the JWT claims (authMiddleware already ran).
	actor := actorFromContext(r.Context())

	// 2. Per-subject rate limit.
	if !s.analyticsQueryAllow(actor) {
		writeError(w, http.StatusTooManyRequests, "analytics query rate limit exceeded")
		return time.Time{}, time.Time{}, actor, false
	}

	// 3. Parse & bound the time range.
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return time.Time{}, time.Time{}, actor, false
	}

	// 4. Audit the read — who queried what, when, over what range.
	if s.audit != nil {
		if aerr := s.audit.Log(r.Context(), "analytics.query", actor, map[string]interface{}{
			"endpoint": endpoint,
			"from":     from.Format(time.RFC3339),
			"to":       to.Format(time.RFC3339),
		}); aerr != nil {
			logAuditErr(false, "analytics.query", aerr)
		}
	}

	return from, to, actor, true
}

// actorFromContext returns the authenticated subject, or "anonymous" if
// authentication was disabled (test mode) or claims are missing.
//
// Feature 35: prefers `principal.FromContext(ctx).DisplayName` when a
// typed Principal is in context (cert-CN path → operator DisplayName;
// JWT path → subject DisplayName). Falls back to the legacy
// claims-based read for contexts that somehow carry claims without a
// Principal (should not happen after authMiddleware wires Principal
// alongside Claims, but the fallback keeps back-compat watertight).
//
// Why DisplayName + not .ID: the handlers calling actorFromContext
// today pass the return value directly into audit.Event.Actor, which
// is documented as the bare-string shape for back-compat. The Event
// will independently carry Principal + PrincipalKind (populated by
// audit.Log from the ctx), so dashboard consumers get both the
// bare string (for legacy display) and the typed fields (for filter /
// badge) without either requiring the other.
func actorFromContext(ctx context.Context) string {
	if p := principal.FromContext(ctx); p.Kind != principal.KindAnonymous {
		return p.DisplayName
	}
	if claims, ok := ctx.Value(claimsContextKey).(*auth.Claims); ok && claims != nil {
		return claims.Subject
	}
	return "anonymous"
}

// parseTimeRange extracts "from" and "to" query parameters as RFC3339 timestamps.
// Defaults: from = 7 days ago, to = now. Returns an error if the range is
// malformed, inverted, or exceeds analyticsMaxRange.
func parseTimeRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -7)
	to := now

	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errBadRequest("invalid 'from' timestamp: must be RFC3339")
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errBadRequest("invalid 'to' timestamp: must be RFC3339")
		}
		to = t
	}

	if !to.After(from) {
		return time.Time{}, time.Time{}, errBadRequest("'to' must be after 'from'")
	}
	if to.Sub(from) > analyticsMaxRange {
		return time.Time{}, time.Time{}, errBadRequest("time range exceeds 365-day maximum")
	}
	return from, to, nil
}

// errBadRequest is a lightweight error type whose Error() message is safe to
// return to the client (no internal details, no stack).
type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }

// parseIntParam extracts an integer query parameter with a default, a minimum
// of 0, and an optional maximum. Returns def if the value is missing or
// non-numeric; clamps to max if the value exceeds it.
func parseIntParam(r *http.Request, key string, def, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

// ── Handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleAnalyticsThroughput(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "throughput")
	if !ok {
		return
	}

	// Bucket width is selectable via `?bucket=hour|minute|second`.
	// The value is spliced into the SQL via fmt.Sprintf (date_trunc
	// isn't parameterisable), so parseBucketParam's allowlist is
	// the SQL-injection guard — no raw caller input hits the query.
	bucket := parseBucketParam(r)
	query := fmt.Sprintf(`
		SELECT
			date_trunc('%s', completed_at) AS hour,
			final_status,
			COUNT(*)                         AS job_count,
			COALESCE(AVG(duration_ms), 0)    AS avg_duration_ms,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms), 0) AS p95_duration_ms
		FROM job_summary
		WHERE completed_at IS NOT NULL
		  AND completed_at >= $1
		  AND completed_at < $2
		GROUP BY 1, 2
		ORDER BY 1
	`, bucket)
	rows, err := s.analyticsDB.Query(r.Context(), query, from, to)
	if err != nil {
		slog.Error("analytics throughput query failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type row struct {
		Hour          time.Time `json:"hour"`
		Status        string    `json:"status"`
		JobCount      int64     `json:"job_count"`
		AvgDurationMs float64   `json:"avg_duration_ms"`
		P95DurationMs float64   `json:"p95_duration_ms"`
	}
	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Hour, &r.Status, &r.JobCount, &r.AvgDurationMs, &r.P95DurationMs); err != nil {
			slog.Error("analytics throughput scan failed", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics throughput rows error", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsThroughput", map[string]any{
		"from": from, "to": to, "data": results,
	})
}

func (s *Server) handleAnalyticsNodeReliability(w http.ResponseWriter, r *http.Request) {
	// No time range for this endpoint — it returns the current node roster.
	// Still run the rate-limit + audit preflight with a synthetic zero range.
	actor := actorFromContext(r.Context())
	if !s.analyticsQueryAllow(actor) {
		writeError(w, http.StatusTooManyRequests, "analytics query rate limit exceeded")
		return
	}
	if s.audit != nil {
		if aerr := s.audit.Log(r.Context(), "analytics.query", actor, map[string]interface{}{
			"endpoint": "node-reliability",
		}); aerr != nil {
			logAuditErr(false, "analytics.query", aerr)
		}
	}

	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT
			node_id,
			COALESCE(address, '')         AS address,
			jobs_completed,
			jobs_failed,
			COALESCE(ROUND(
				jobs_failed::numeric / NULLIF(jobs_completed + jobs_failed, 0) * 100, 2
			), 0)                         AS failure_rate_pct,
			times_stale,
			times_revoked
		FROM node_summary
		ORDER BY failure_rate_pct DESC
	`)
	if err != nil {
		slog.Error("analytics node reliability query failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type row struct {
		NodeID         string  `json:"node_id"`
		Address        string  `json:"address"`
		JobsCompleted  int     `json:"jobs_completed"`
		JobsFailed     int     `json:"jobs_failed"`
		FailureRatePct float64 `json:"failure_rate_pct"`
		TimesStale     int     `json:"times_stale"`
		TimesRevoked   int     `json:"times_revoked"`
	}
	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.NodeID, &r.Address, &r.JobsCompleted, &r.JobsFailed,
			&r.FailureRatePct, &r.TimesStale, &r.TimesRevoked); err != nil {
			slog.Error("analytics node reliability scan failed", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics node reliability rows error", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsNodeReliability", map[string]any{"data": results})
}

func (s *Server) handleAnalyticsRetryEffectiveness(w http.ResponseWriter, r *http.Request) {
	// Aggregate over the whole job_summary table — no time range filter.
	actor := actorFromContext(r.Context())
	if !s.analyticsQueryAllow(actor) {
		writeError(w, http.StatusTooManyRequests, "analytics query rate limit exceeded")
		return
	}
	if s.audit != nil {
		if aerr := s.audit.Log(r.Context(), "analytics.query", actor, map[string]interface{}{
			"endpoint": "retry-effectiveness",
		}); aerr != nil {
			logAuditErr(false, "analytics.query", aerr)
		}
	}

	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT
			CASE WHEN attempts > 1 THEN 'retried' ELSE 'first_attempt' END AS category,
			final_status,
			COUNT(*)            AS job_count,
			COALESCE(AVG(duration_ms), 0) AS avg_duration_ms
		FROM job_summary
		WHERE final_status IN ('completed', 'failed')
		GROUP BY 1, 2
	`)
	if err != nil {
		slog.Error("analytics retry effectiveness query failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type row struct {
		Category      string  `json:"category"`
		Status        string  `json:"status"`
		JobCount      int64   `json:"job_count"`
		AvgDurationMs float64 `json:"avg_duration_ms"`
	}
	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Category, &r.Status, &r.JobCount, &r.AvgDurationMs); err != nil {
			slog.Error("analytics retry effectiveness scan failed", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics retry effectiveness rows error", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsRetryEffectiveness", map[string]any{"data": results})
}

func (s *Server) handleAnalyticsQueueWait(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "queue-wait")
	if !ok {
		return
	}

	// Same bucket-allowlist treatment as throughput — see
	// parseBucketParam for the SQL-injection guard rationale.
	//
	// Bucket + filter on `started_at`, not `submitted_at`. The
	// metric is "how long did this job wait before running", and
	// that measurement becomes known at started_at. Keying on
	// submitted_at caused the series to disappear from a rolling
	// "LAST 10 MIN" view once the submit instant scrolled off the
	// left edge — even though the job itself was only seconds old
	// and its wait sample was entirely relevant to the window the
	// user was looking at.
	bucket := parseBucketParam(r)
	query := fmt.Sprintf(`
		SELECT
			date_trunc('%s', started_at) AS hour,
			COALESCE(AVG(EXTRACT(EPOCH FROM (started_at - submitted_at)) * 1000), 0) AS avg_wait_ms,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (started_at - submitted_at)) * 1000
			), 0) AS p95_wait_ms,
			COUNT(*) AS job_count
		FROM job_summary
		WHERE started_at IS NOT NULL
		  AND started_at >= $1
		  AND started_at < $2
		GROUP BY 1
		ORDER BY 1
	`, bucket)
	rows, err := s.analyticsDB.Query(r.Context(), query, from, to)
	if err != nil {
		slog.Error("analytics queue wait query failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type row struct {
		Hour      time.Time `json:"hour"`
		AvgWaitMs float64   `json:"avg_wait_ms"`
		P95WaitMs float64   `json:"p95_wait_ms"`
		JobCount  int64     `json:"job_count"`
	}
	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Hour, &r.AvgWaitMs, &r.P95WaitMs, &r.JobCount); err != nil {
			slog.Error("analytics queue wait scan failed", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics queue wait rows error", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsQueueWait", map[string]any{
		"from": from, "to": to, "data": results,
	})
}

func (s *Server) handleAnalyticsWorkflowOutcomes(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "workflow-outcomes")
	if !ok {
		return
	}

	rows, err := s.analyticsDB.Query(r.Context(), `
		SELECT
			event_type,
			date_trunc('day', timestamp) AS day,
			COUNT(*) AS count
		FROM events
		WHERE event_type IN ('workflow.completed', 'workflow.failed')
		  AND timestamp >= $1
		  AND timestamp < $2
		GROUP BY 1, 2
		ORDER BY 2
	`, from, to)
	if err != nil {
		slog.Error("analytics workflow outcomes query failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type row struct {
		EventType string    `json:"event_type"`
		Day       time.Time `json:"day"`
		Count     int64     `json:"count"`
	}
	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.EventType, &r.Day, &r.Count); err != nil {
			slog.Error("analytics workflow outcomes scan failed", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics workflow outcomes rows error", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsWorkflowOutcomes", map[string]any{
		"from": from, "to": to, "data": results,
	})
}

func (s *Server) handleAnalyticsEvents(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.analyticsPreflight(w, r, "events")
	if !ok {
		return
	}
	limit := parseIntParam(r, "limit", 100, analyticsMaxLimit)
	offset := parseIntParam(r, "offset", 0, 0)
	eventType := r.URL.Query().Get("type")

	var rows pgx.Rows
	var err error
	if eventType != "" {
		rows, err = s.analyticsDB.Query(r.Context(), `
			SELECT id, event_type, timestamp, data, job_id, node_id, workflow_id
			FROM events
			WHERE event_type = $1
			  AND timestamp >= $2
			  AND timestamp < $3
			ORDER BY timestamp DESC
			LIMIT $4 OFFSET $5
		`, eventType, from, to, limit, offset)
	} else {
		rows, err = s.analyticsDB.Query(r.Context(), `
			SELECT id, event_type, timestamp, data, job_id, node_id, workflow_id
			FROM events
			WHERE timestamp >= $1
			  AND timestamp < $2
			ORDER BY timestamp DESC
			LIMIT $3 OFFSET $4
		`, from, to, limit, offset)
	}
	if err != nil {
		slog.Error("analytics events query failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type row struct {
		ID         string    `json:"id"`
		EventType  string    `json:"event_type"`
		Timestamp  time.Time `json:"timestamp"`
		Data       []byte    `json:"data"`
		JobID      *string   `json:"job_id"`
		NodeID     *string   `json:"node_id"`
		WorkflowID *string   `json:"workflow_id"`
	}
	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.EventType, &r.Timestamp, &r.Data,
			&r.JobID, &r.NodeID, &r.WorkflowID); err != nil {
			slog.Error("analytics events scan failed", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics events rows error", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleAnalyticsEvents", map[string]any{
		"from": from, "to": to, "limit": limit, "offset": offset, "data": results,
	})
}
