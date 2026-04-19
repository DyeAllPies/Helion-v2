// internal/api/handlers_analytics_test.go

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ── Mock analytics DB ────────────────────────────────────────────────────

// mockAnalyticsDB implements api.AnalyticsDB for handler tests.
// It captures the SQL and args from each Query call and returns
// configurable rows or errors.
type mockAnalyticsDB struct {
	lastSQL  string
	lastArgs []any
	rows     *mockRows
	err      error
}

func (m *mockAnalyticsDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	m.lastSQL = sql
	m.lastArgs = args
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

// mockRows implements pgx.Rows with zero rows (empty result set).
type mockRows struct {
	closed bool
}

func (r *mockRows) Close()                                         { r.closed = true }
func (r *mockRows) Err() error                                     { return nil }
func (r *mockRows) CommandTag() pgconn.CommandTag                  { return pgconn.NewCommandTag("SELECT 0") }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription   { return nil }
func (r *mockRows) Next() bool                                     { return false }
func (r *mockRows) Scan(_ ...any) error                            { return nil }
func (r *mockRows) Values() ([]any, error)                         { return nil, nil }
func (r *mockRows) RawValues() [][]byte                            { return nil }
func (r *mockRows) Conn() *pgx.Conn                                { return nil }

// ── Test helpers ─────────────────────────────────────────────────────────

func newAnalyticsServer(db api.AnalyticsDB) *api.Server {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)
	srv.DisableAuth()
	srv.SetAnalyticsDB(db)
	return srv
}

func doGet(t *testing.T, srv *api.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// ── Handler tests ────────────────────────────────────────────────────────

func TestAnalyticsThroughput_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/throughput")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["data"] != nil {
		t.Logf("data = %v (nil slice encodes as null, which is fine)", resp["data"])
	}
}

func TestAnalyticsThroughput_QueryError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: fmt.Errorf("pg down")}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/throughput")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestAnalyticsThroughput_TimeRangeParams(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	doGet(t, srv, "/api/analytics/throughput?from=2026-04-01T00:00:00Z&to=2026-04-13T00:00:00Z")

	if len(db.lastArgs) < 2 {
		t.Fatalf("expected at least 2 args, got %d", len(db.lastArgs))
	}
}

// TestAnalyticsThroughput_BucketParam_HonouredBySQL asserts that the
// `bucket` query param drives the date_trunc width on both analytics
// time-series endpoints. The parameter is allow-listed — any other
// value silently falls back to "hour" — so this pair of assertions
// is the injection guard we rely on.
func TestAnalyticsThroughput_BucketParam_HonouredBySQL(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		expect string
	}{
		{"default is hour", "/api/analytics/throughput", "date_trunc('hour'"},
		{"minute accepted", "/api/analytics/throughput?bucket=minute", "date_trunc('minute'"},
		{"second accepted", "/api/analytics/throughput?bucket=second", "date_trunc('second'"},
		// Rejected values fall back to the default — this is the
		// SQL-injection guard. A caller passing `hour'); DROP...`
		// must land on the safe `hour` branch, not reach the query.
		{"bogus falls back to hour", "/api/analytics/throughput?bucket=nanosecond", "date_trunc('hour'"},
		{"injection attempt blocked", "/api/analytics/throughput?bucket=hour'%3B+DROP+TABLE+job_summary--", "date_trunc('hour'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &mockAnalyticsDB{rows: &mockRows{}}
			srv := newAnalyticsServer(db)
			doGet(t, srv, tc.url)
			if !strings.Contains(db.lastSQL, tc.expect) {
				t.Errorf("SQL did not contain %q; got: %s", tc.expect, db.lastSQL)
			}
			// Neither "DROP" nor the injection tail should ever
			// reach the query no matter what the caller sent.
			if strings.Contains(strings.ToUpper(db.lastSQL), "DROP") {
				t.Errorf("SQL contained DROP — injection guard leaked: %s", db.lastSQL)
			}
		})
	}
}

// TestAnalyticsQueueWait_BucketParam_HonouredBySQL mirrors the
// throughput test for the queue-wait endpoint (same allowlist
// wiring, same injection concern).
func TestAnalyticsQueueWait_BucketParam_HonouredBySQL(t *testing.T) {
	cases := []struct {
		url    string
		expect string
	}{
		{"/api/analytics/queue-wait", "date_trunc('hour'"},
		{"/api/analytics/queue-wait?bucket=minute", "date_trunc('minute'"},
		{"/api/analytics/queue-wait?bucket=second", "date_trunc('second'"},
		{"/api/analytics/queue-wait?bucket=bogus", "date_trunc('hour'"},
	}
	for _, tc := range cases {
		db := &mockAnalyticsDB{rows: &mockRows{}}
		srv := newAnalyticsServer(db)
		doGet(t, srv, tc.url)
		if !strings.Contains(db.lastSQL, tc.expect) {
			t.Errorf("%s: SQL did not contain %q; got: %s", tc.url, tc.expect, db.lastSQL)
		}
	}
}

func TestAnalyticsNodeReliability_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/node-reliability")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsNodeReliability_QueryError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: fmt.Errorf("pg down")}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/node-reliability")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestAnalyticsRetryEffectiveness_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/retry-effectiveness")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsRetryEffectiveness_QueryError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: fmt.Errorf("pg down")}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/retry-effectiveness")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestAnalyticsQueueWait_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/queue-wait")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsQueueWait_QueryError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: fmt.Errorf("pg down")}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/queue-wait")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestAnalyticsWorkflowOutcomes_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/workflow-outcomes")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsWorkflowOutcomes_QueryError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: fmt.Errorf("pg down")}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/workflow-outcomes")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestAnalyticsEvents_EmptyResult_Returns200(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/events")
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAnalyticsEvents_WithTypeFilter(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	doGet(t, srv, "/api/analytics/events?type=job.failed&limit=50&offset=10")

	// Should have 5 args: type, from, to, limit, offset.
	if len(db.lastArgs) != 5 {
		t.Fatalf("expected 5 args (with type filter), got %d", len(db.lastArgs))
	}
	if db.lastArgs[0] != "job.failed" {
		t.Errorf("arg[0] = %v, want %q", db.lastArgs[0], "job.failed")
	}
}

func TestAnalyticsEvents_WithoutTypeFilter(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	doGet(t, srv, "/api/analytics/events?limit=20")

	// Should have 4 args: from, to, limit, offset.
	if len(db.lastArgs) != 4 {
		t.Fatalf("expected 4 args (no type filter), got %d", len(db.lastArgs))
	}
}

func TestAnalyticsEvents_QueryError_Returns500(t *testing.T) {
	db := &mockAnalyticsDB{err: fmt.Errorf("pg down")}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/events")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestAnalyticsEvents_DefaultLimitAndOffset(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	doGet(t, srv, "/api/analytics/events")

	// No type filter: args are from, to, limit, offset.
	if len(db.lastArgs) != 4 {
		t.Fatalf("expected 4 args, got %d", len(db.lastArgs))
	}
	// limit default=100, offset default=0.
	if db.lastArgs[2] != 100 {
		t.Errorf("limit = %v, want 100", db.lastArgs[2])
	}
	if db.lastArgs[3] != 0 {
		t.Errorf("offset = %v, want 0", db.lastArgs[3])
	}
}

// ── Security tests ───────────────────────────────────────────────────────
//
// These cover the controls added alongside the analytics feature:
//   - per-subject rate limiting
//   - audit logging
//   - input bounds on time range + limit/offset

// newAnalyticsServerWithAudit wires in an in-memory audit store so tests
// can assert that analytics.query events are recorded.
func newAnalyticsServerWithAudit(db api.AnalyticsDB) (*api.Server, *inMemoryAuditStore) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()
	srv.SetAnalyticsDB(db)
	return srv, store
}

// ── Rate limiting ────────────────────────────────────────────────────────

func TestAnalytics_RateLimit_BurstAllowed(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	// Burst is 30 — the first 30 requests must succeed without 429.
	for i := 0; i < 30; i++ {
		rr := doGet(t, srv, "/api/analytics/throughput")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200; body: %s", i, rr.Code, rr.Body.String())
		}
	}
}

func TestAnalytics_RateLimit_ExcessReturns429(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	// Exhaust the burst (30), then 50 more should rate-limit.
	seen429 := false
	for i := 0; i < 100; i++ {
		rr := doGet(t, srv, "/api/analytics/throughput")
		if rr.Code == http.StatusTooManyRequests {
			seen429 = true
			if !strings.Contains(rr.Body.String(), "rate limit") {
				t.Errorf("429 body = %q, want rate-limit message", rr.Body.String())
			}
			break
		}
	}
	if !seen429 {
		t.Error("expected at least one 429 response after exhausting burst")
	}
}

// ── Input bounds ─────────────────────────────────────────────────────────

func TestAnalytics_TimeRange_InvertedReturns400(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/throughput?from=2026-04-13T00:00:00Z&to=2026-04-01T00:00:00Z")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "after") {
		t.Errorf("body = %q, want 'after'", rr.Body.String())
	}
}

func TestAnalytics_TimeRange_ExceedsMaxReturns400(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	// 400 days > 365-day cap.
	from := time.Now().UTC().AddDate(-2, 0, 0).Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)

	rr := doGet(t, srv, "/api/analytics/throughput?from="+from+"&to="+to)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "365") {
		t.Errorf("body = %q, want '365'", rr.Body.String())
	}
}

func TestAnalytics_TimeRange_MalformedReturns400(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	rr := doGet(t, srv, "/api/analytics/throughput?from=not-a-date")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "RFC3339") {
		t.Errorf("body = %q, want 'RFC3339'", rr.Body.String())
	}
}

func TestAnalytics_Events_LimitClampedToMax(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	doGet(t, srv, "/api/analytics/events?limit=999999999")

	// limit arg should be clamped to 1000 (the max). For no-type-filter,
	// args are [from, to, limit, offset].
	if db.lastArgs[2] != 1000 {
		t.Errorf("limit = %v, want clamped to 1000", db.lastArgs[2])
	}
}

func TestAnalytics_Events_NegativeLimitUsesDefault(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv := newAnalyticsServer(db)

	doGet(t, srv, "/api/analytics/events?limit=-50")
	if db.lastArgs[2] != 100 {
		t.Errorf("limit = %v, want default 100 for negative input", db.lastArgs[2])
	}
}

// ── Audit logging ────────────────────────────────────────────────────────

func TestAnalytics_AuditLog_RecordsQuery(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv, auditStore := newAnalyticsServerWithAudit(db)

	rr := doGet(t, srv, "/api/analytics/throughput")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// At least one audit entry must be recorded.
	entries, err := auditStore.Scan(context.Background(), "audit:", 0)
	if err != nil {
		t.Fatalf("audit scan: %v", err)
	}
	found := false
	for _, raw := range entries {
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		if evt["type"] == "analytics.query" {
			found = true
			details, _ := evt["details"].(map[string]any)
			if details["endpoint"] != "throughput" {
				t.Errorf("endpoint = %v, want 'throughput'", details["endpoint"])
			}
		}
	}
	if !found {
		t.Error("no analytics.query audit entry was recorded")
	}
}

func TestAnalytics_AuditLog_NodeReliability(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv, auditStore := newAnalyticsServerWithAudit(db)

	doGet(t, srv, "/api/analytics/node-reliability")

	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	found := false
	for _, raw := range entries {
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		details, _ := evt["details"].(map[string]any)
		if evt["type"] == "analytics.query" && details["endpoint"] == "node-reliability" {
			found = true
		}
	}
	if !found {
		t.Error("no analytics.query entry for node-reliability")
	}
}

func TestAnalytics_AuditLog_RetryEffectiveness(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv, auditStore := newAnalyticsServerWithAudit(db)

	doGet(t, srv, "/api/analytics/retry-effectiveness")

	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	found := false
	for _, raw := range entries {
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		details, _ := evt["details"].(map[string]any)
		if evt["type"] == "analytics.query" && details["endpoint"] == "retry-effectiveness" {
			found = true
		}
	}
	if !found {
		t.Error("no analytics.query entry for retry-effectiveness")
	}
}

func TestAnalytics_AuditLog_RateLimitDoesNotRecordQuery(t *testing.T) {
	db := &mockAnalyticsDB{rows: &mockRows{}}
	srv, auditStore := newAnalyticsServerWithAudit(db)

	// Blast enough requests to exhaust the burst and get at least one 429.
	successCount := 0
	for i := 0; i < 100; i++ {
		rr := doGet(t, srv, "/api/analytics/throughput")
		if rr.Code == http.StatusOK {
			successCount++
		}
	}

	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	queryCount := 0
	for _, raw := range entries {
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		if evt["type"] == "analytics.query" {
			queryCount++
		}
	}
	// Audit count must equal success count — rate-limited requests must NOT
	// be audited (they were rejected before reaching the audit step).
	if queryCount != successCount {
		t.Errorf("audited %d queries, want %d (= successful requests)", queryCount, successCount)
	}
}
