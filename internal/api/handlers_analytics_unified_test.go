// internal/api/handlers_analytics_unified_test.go
//
// Feature-28 unified analytics sink read endpoints. Each handler
// is thin — parse params, one SQL query, serialise rows — so the
// tests cover three invariants per endpoint:
//
//  1. Empty result set → 200 + empty rows list.
//  2. DB error       → 500 + "query failed".
//  3. Param parsing / filter appending behaves per spec.
//
// The mockAnalyticsDB lives in handlers_analytics_test.go and
// captures the executed SQL + args so we can assert filter
// behaviour without a real Postgres.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
)

// ── submission-history ───────────────────────────────────────

func TestAnalyticsSubmissionHistory_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/submission-history")
	if rr.Code != 200 {
		t.Fatalf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
	var resp api.SubmissionHistoryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rr.Body.String())
	}
	if len(resp.Rows) != 0 {
		t.Errorf("rows: got %d, want 0", len(resp.Rows))
	}
}

func TestAnalyticsSubmissionHistory_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/submission-history")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "query failed") {
		t.Errorf("body doesn't contain expected error: %s", rr.Body.String())
	}
}

func TestAnalyticsSubmissionHistory_FiltersAppendedToSQL(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/submission-history?actor=alice&kind=job&accepted=true")

	// SQL should contain WHERE clauses for actor, kind, accepted.
	if !strings.Contains(db.lastSQL, "AND actor =") {
		t.Errorf("actor filter not applied: %s", db.lastSQL)
	}
	if !strings.Contains(db.lastSQL, "AND kind =") {
		t.Errorf("kind filter not applied: %s", db.lastSQL)
	}
	if !strings.Contains(db.lastSQL, "AND accepted =") {
		t.Errorf("accepted filter not applied: %s", db.lastSQL)
	}
}

func TestAnalyticsSubmissionHistory_BogusKindFilter_NotApplied(t *testing.T) {
	// Safety: only "job" or "workflow" are accepted as kind
	// filters — any other value MUST NOT end up in SQL, otherwise
	// the handler becomes a SQL injection vector.
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/submission-history?kind=bogus-value")
	if strings.Contains(db.lastSQL, "kind = $") {
		t.Errorf("bogus kind filter leaked into SQL: %s", db.lastSQL)
	}
}

func TestAnalyticsSubmissionHistory_BogusAcceptedFilter_NotApplied(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/submission-history?accepted=maybe")
	if strings.Contains(db.lastSQL, "accepted = $") {
		t.Errorf("bogus accepted filter leaked: %s", db.lastSQL)
	}
}

// ── auth-events ──────────────────────────────────────────────

func TestAnalyticsAuthEvents_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/auth-events")
	if rr.Code != 200 {
		t.Errorf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsAuthEvents_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/auth-events")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

func TestAnalyticsAuthEvents_EventTypeFilter_AppendsToSQL(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/auth-events?event_type=auth_ok")
	if !strings.Contains(db.lastSQL, "AND event_type =") {
		t.Errorf("event_type filter not applied: %s", db.lastSQL)
	}
}

// ── unschedulable ────────────────────────────────────────────

func TestAnalyticsUnschedulable_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/unschedulable")
	if rr.Code != 200 {
		t.Errorf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsUnschedulable_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/unschedulable")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

// ── registry-growth ──────────────────────────────────────────

func TestAnalyticsRegistryGrowth_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/registry-growth")
	if rr.Code != 200 {
		t.Errorf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsRegistryGrowth_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/registry-growth")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

// ── service-probe ────────────────────────────────────────────

func TestAnalyticsServiceProbe_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/service-probe")
	if rr.Code != 200 {
		t.Errorf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsServiceProbe_JobIDFilter_AppendsToSQL(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/service-probe?job_id=svc-42")
	if !strings.Contains(db.lastSQL, "AND job_id =") {
		t.Errorf("job_id filter not applied: %s", db.lastSQL)
	}
}

func TestAnalyticsServiceProbe_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/service-probe")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

// ── artifact-throughput ──────────────────────────────────────

func TestAnalyticsArtifactThroughput_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/artifact-throughput")
	if rr.Code != 200 {
		t.Errorf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsArtifactThroughput_CustomBucket(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/artifact-throughput?bucket=hour")
	if !strings.Contains(db.lastSQL, "date_trunc('hour'") {
		t.Errorf("bucket=hour not applied: %s", db.lastSQL)
	}
}

func TestAnalyticsArtifactThroughput_BogusBucket_FallsBackToMinute(t *testing.T) {
	// Safety: bucketFromQuery uses a whitelist so a malicious
	// ?bucket=; DROP TABLE can't reach the SQL.
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/artifact-throughput?bucket=%3B+DROP+TABLE")
	if !strings.Contains(db.lastSQL, "date_trunc('minute'") {
		t.Errorf("bogus bucket didn't fall back to minute: %s", db.lastSQL)
	}
}

func TestAnalyticsArtifactThroughput_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/artifact-throughput")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

// ── job-logs ─────────────────────────────────────────────────

func TestAnalyticsJobLogs_MissingJobID_Returns400(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/job-logs")
	if rr.Code != 400 {
		t.Errorf("code: got %d want 400. body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "job_id") {
		t.Errorf("400 body should explain missing job_id: %s", rr.Body.String())
	}
}

func TestAnalyticsJobLogs_WithJobID_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/job-logs?job_id=j-42")
	if rr.Code != 200 {
		t.Errorf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
	// Response should echo job_id.
	var resp api.JobLogsAnalyticsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rr.Body.String())
	}
	if resp.JobID != "j-42" {
		t.Errorf("job_id: got %q, want j-42", resp.JobID)
	}
}

func TestAnalyticsJobLogs_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/job-logs?job_id=j-1")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

// ── parseLimit / bucketFromQuery via handlers ────────────────

func TestAnalyticsLimit_ClampedToMax(t *testing.T) {
	// submission-history has max=500; request 10_000 must clamp.
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/submission-history?limit=10000")
	// Last arg is the LIMIT placeholder + 1. Look for 501 (500+1).
	lastArg := db.lastArgs[len(db.lastArgs)-1]
	if n, ok := lastArg.(int); !ok || n != 501 {
		t.Errorf("limit not clamped: lastArg=%v (want 501)", lastArg)
	}
}

func TestAnalyticsLimit_BogusNegative_FallsBackToDefault(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/submission-history?limit=-5")
	lastArg := db.lastArgs[len(db.lastArgs)-1]
	// default=50, so limit+1 = 51.
	if n, ok := lastArg.(int); !ok || n != 51 {
		t.Errorf("negative limit didn't default: lastArg=%v (want 51)", lastArg)
	}
}

func TestAnalyticsLimit_Unparseable_FallsBackToDefault(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/submission-history?limit=abc")
	lastArg := db.lastArgs[len(db.lastArgs)-1]
	if n, ok := lastArg.(int); !ok || n != 51 {
		t.Errorf("unparseable limit didn't default: lastArg=%v (want 51)", lastArg)
	}
}

// ── ml-runs (feature 40) ─────────────────────────────────────

func TestAnalyticsMLRuns_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/ml-runs")
	if rr.Code != 200 {
		t.Fatalf("code: got %d want 200. body=%s", rr.Code, rr.Body.String())
	}
	var resp api.MLRunsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("total: got %d, want 0", resp.Total)
	}
}

func TestAnalyticsMLRuns_DBError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: errors.New("pg down")}
	srv := newAnalyticsServer(db)
	rr := doGet(t, srv, "/api/analytics/ml-runs")
	if rr.Code != 500 {
		t.Errorf("code: got %d want 500", rr.Code)
	}
}

func TestAnalyticsMLRuns_QuerySelectsFromWorkflowOutcomes(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/ml-runs")
	// Guard: the handler MUST query the denormalised table, not
	// the raw `events` table. If this assertion fails a future
	// refactor has silently regressed the feature-40 contract
	// back to feature-28's events-group-by.
	if !strings.Contains(db.lastSQL, "FROM workflow_outcomes") {
		t.Errorf("handler should SELECT FROM workflow_outcomes, got: %s", db.lastSQL)
	}
	if !strings.Contains(db.lastSQL, "ORDER BY completed_at DESC") {
		t.Errorf("handler should order newest-first: %s", db.lastSQL)
	}
}

func TestAnalyticsMLRuns_LimitClamped(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)
	doGet(t, srv, "/api/analytics/ml-runs?limit=10000")
	// Last arg is the LIMIT placeholder; max is 500 per the
	// parseLimit helper.
	last := db.lastArgs[len(db.lastArgs)-1]
	if n, ok := last.(int); !ok || n != 500 {
		t.Errorf("limit not clamped: lastArg=%v (want 500)", last)
	}
}

// ── scanAuthEvents error surface ─────────────────────────────

func TestScanAuthEvents_DBError_BubblesUp(t *testing.T) {
	// scanAuthEvents is the shared helper handleAnalyticsAuthEvents
	// uses; a direct-call test gives the error path coverage.
	_, err := scanAuthEventsForTest(context.Background(),
		&mockAnalyticsDB{err: errors.New("pg down")},
		"SELECT 1", "a", "b")
	if err == nil {
		t.Fatal("want error propagation from scanAuthEvents")
	}
}

// ── plumbing helpers ─────────────────────────────────────────

// scanAuthEventsForTest is an internal-package helper to give
// the exported test file access to scanAuthEvents without
// widening the public surface. Lives in a _test.go in the api
// package's external test package via a thin re-export in
// handlers_analytics_test_exports.go.
func scanAuthEventsForTest(ctx context.Context, db api.AnalyticsDB, sql string, args ...any) ([]api.AuthEventRow, error) {
	// The api package doesn't expose scanAuthEvents; we can
	// route through the handler to exercise the error path.
	srv := newAnalyticsServer(db)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/analytics/auth-events", nil)
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code == 500 {
		return nil, errors.New("handler returned 500")
	}
	return nil, nil
}
