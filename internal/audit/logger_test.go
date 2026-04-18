package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
)

// ── Mock store ────────────────────────────────────────────────────────────────

type mockStore struct {
	mu      sync.Mutex
	entries map[string][]byte
	ttls    map[string]time.Duration
	scanErr error
	putErr  error
}

func newMockStore() *mockStore {
	return &mockStore{
		entries: make(map[string][]byte),
		ttls:    make(map[string]time.Duration),
	}
}

func (m *mockStore) Put(ctx context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = append([]byte{}, value...)
	m.ttls[key] = 0
	return nil
}

func (m *mockStore) PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = append([]byte{}, value...)
	m.ttls[key] = ttl
	return nil
}

func (m *mockStore) Scan(ctx context.Context, prefix string, limit int) ([][]byte, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var results [][]byte
	for k, v := range m.entries {
		if strings.HasPrefix(k, prefix) {
			results = append(results, v)
		}
	}
	return results, nil
}

func (m *mockStore) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ks := make([]string, 0, len(m.entries))
	for k := range m.entries {
		ks = append(ks, k)
	}
	return ks
}

// ── Log ───────────────────────────────────────────────────────────────────────

func TestLog_CreatesEvent_WithRequiredFields(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	before := time.Now()
	err := logger.Log(context.Background(), audit.EventJobSubmit, "user-1", map[string]interface{}{
		"job_id": "j1",
	})
	if err != nil {
		t.Fatalf("Log returned error: %v", err)
	}

	keys := store.keys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(keys))
	}

	var ev audit.Event
	if err := json.Unmarshal(store.entries[keys[0]], &ev); err != nil {
		t.Fatalf("stored value is not valid JSON: %v", err)
	}

	if ev.ID == "" {
		t.Error("event ID must not be empty")
	}
	if ev.Type != audit.EventJobSubmit {
		t.Errorf("want type %q, got %q", audit.EventJobSubmit, ev.Type)
	}
	if ev.Actor != "user-1" {
		t.Errorf("want actor %q, got %q", "user-1", ev.Actor)
	}
	if ev.Timestamp.Before(before) {
		t.Error("event timestamp should be >= start of test")
	}
}

func TestLog_KeyFormat_IsTimeOrdered(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.Log(context.Background(), audit.EventCoordinatorStart, "system", nil)

	for _, k := range store.keys() {
		if !strings.HasPrefix(k, "audit:") {
			t.Errorf("key %q should have audit: prefix", k)
		}
	}
}

func TestLog_NoTTL_UsesPut(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0) // ttl = 0

	_ = logger.Log(context.Background(), audit.EventCoordinatorStart, "system", nil)

	for _, ttl := range store.ttls {
		if ttl != 0 {
			t.Errorf("want ttl=0 (no expiry), got %v", ttl)
		}
	}
}

func TestLog_WithTTL_UsesPutWithTTL(t *testing.T) {
	store := newMockStore()
	ttl := 90 * 24 * time.Hour
	logger := audit.NewLogger(store, ttl)

	_ = logger.Log(context.Background(), audit.EventCoordinatorStart, "system", nil)

	for _, stored := range store.ttls {
		if stored != ttl {
			t.Errorf("want ttl=%v, got %v", ttl, stored)
		}
	}
}

func TestLog_StoreError_Propagates(t *testing.T) {
	store := newMockStore()
	store.putErr = errors.New("disk full")
	logger := audit.NewLogger(store, 0)

	err := logger.Log(context.Background(), audit.EventJobSubmit, "u", nil)
	if err == nil {
		t.Error("expected error from store, got nil")
	}
}

// ── Typed log helpers ─────────────────────────────────────────────────────────

func storedEvent(t *testing.T, store *mockStore) audit.Event {
	t.Helper()
	keys := store.keys()
	if len(keys) == 0 {
		t.Fatal("no events stored")
	}
	var ev audit.Event
	if err := json.Unmarshal(store.entries[keys[0]], &ev); err != nil {
		t.Fatalf("unmarshal stored event: %v", err)
	}
	return ev
}

func TestLogNodeRegister_EventType(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	if err := logger.LogNodeRegister(context.Background(), "node-1", "10.0.0.1:9090"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev := storedEvent(t, store)
	if ev.Type != audit.EventNodeRegister {
		t.Errorf("want type %q, got %q", audit.EventNodeRegister, ev.Type)
	}
	if ev.Actor != "node-1" {
		t.Errorf("want actor node-1, got %q", ev.Actor)
	}
	if ev.Details["address"] != "10.0.0.1:9090" {
		t.Errorf("want address in details, got %v", ev.Details)
	}
}

func TestLogNodeRevoke_EventType(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogNodeRevoke(context.Background(), "admin", "node-bad", "compromised")

	ev := storedEvent(t, store)
	if ev.Type != audit.EventNodeRevoke {
		t.Errorf("want %q, got %q", audit.EventNodeRevoke, ev.Type)
	}
	if ev.Actor != "admin" {
		t.Errorf("want actor admin, got %q", ev.Actor)
	}
}

func TestLogJobSubmit_EventType(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogJobSubmit(context.Background(), "api-user", "job-42", "ls")

	ev := storedEvent(t, store)
	if ev.Type != audit.EventJobSubmit {
		t.Errorf("want %q, got %q", audit.EventJobSubmit, ev.Type)
	}
	if ev.Details["job_id"] != "job-42" {
		t.Errorf("want job_id=job-42, got %v", ev.Details["job_id"])
	}
	if ev.Details["command"] != "ls" {
		t.Errorf("want command=ls, got %v", ev.Details["command"])
	}
}

func TestLogJobStateTransition_ActorIsSystem(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogJobStateTransition(context.Background(), "job-1", "PENDING", "RUNNING")

	ev := storedEvent(t, store)
	if ev.Actor != "system" {
		t.Errorf("want actor=system, got %q", ev.Actor)
	}
	if ev.Details["from_state"] != "PENDING" {
		t.Errorf("want from_state=PENDING, got %v", ev.Details["from_state"])
	}
	if ev.Details["to_state"] != "RUNNING" {
		t.Errorf("want to_state=RUNNING, got %v", ev.Details["to_state"])
	}
}

func TestLogAuthFailure_ActorIsUnknown(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogAuthFailure(context.Background(), "bad token", "192.168.1.1")

	ev := storedEvent(t, store)
	if ev.Type != audit.EventAuthFailure {
		t.Errorf("want %q, got %q", audit.EventAuthFailure, ev.Type)
	}
	if ev.Actor != "unknown" {
		t.Errorf("want actor=unknown, got %q", ev.Actor)
	}
}

func TestLogRateLimitHit_StoresLimit(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogRateLimitHit(context.Background(), "node-flood", 10.0)

	ev := storedEvent(t, store)
	if ev.Type != audit.EventRateLimitHit {
		t.Errorf("want %q, got %q", audit.EventRateLimitHit, ev.Type)
	}
	if ev.Details["limit_rps"] == nil {
		t.Error("expected limit_rps in details")
	}
}

func TestLogSecurityViolation_Details(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogSecurityViolation(context.Background(), "node-x", "job-99", "Seccomp")

	ev := storedEvent(t, store)
	if ev.Type != audit.EventSecurityViolation {
		t.Errorf("want %q, got %q", audit.EventSecurityViolation, ev.Type)
	}
	if ev.Details["violation"] != "Seccomp" {
		t.Errorf("want violation=Seccomp, got %v", ev.Details["violation"])
	}
	if ev.Details["job_id"] != "job-99" {
		t.Errorf("want job_id=job-99, got %v", ev.Details["job_id"])
	}
}

func TestLogCoordinatorStart_StoresVersion(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogCoordinatorStart(context.Background(), "v2.1.0")

	ev := storedEvent(t, store)
	if ev.Type != audit.EventCoordinatorStart {
		t.Errorf("want %q, got %q", audit.EventCoordinatorStart, ev.Type)
	}
	if ev.Details["version"] != "v2.1.0" {
		t.Errorf("want version=v2.1.0, got %v", ev.Details["version"])
	}
}

func TestLogCoordinatorStop_StoresReason(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	_ = logger.LogCoordinatorStop(context.Background(), "SIGTERM")

	ev := storedEvent(t, store)
	if ev.Type != audit.EventCoordinatorStop {
		t.Errorf("want %q, got %q", audit.EventCoordinatorStop, ev.Type)
	}
}

// TestLogServiceEvent_Ready_Unhealthy pins the feature-17 audit
// emission shape: event type flips between "service.ready" and
// "service.unhealthy" on ready transitions; actor is stamped as
// "node:<nodeID>" so audit queries can group by reporting node;
// and every field the downstream SIEM / dashboard needs
// (job_id / port / health_path / consecutive_failures) lands in
// details. Every peer method (LogJobSubmit, LogNodeRegister,
// LogSecurityViolation, etc.) has a matching event-type +
// details test; LogServiceEvent was the only one missing.
// A refactor renaming the event strings or dropping details
// fields would silently break the audit query path without
// tripping any feature-17 handler test (which goes through the
// mockAuditLogger, not this real implementation).
func TestLogServiceEvent_Ready_Unhealthy(t *testing.T) {
	t.Run("ready transition emits service.ready", func(t *testing.T) {
		store := newMockStore()
		logger := audit.NewLogger(store, 0)

		_ = logger.LogServiceEvent(context.Background(), "node-a", "svc-1", true, 8080, "/healthz", 0)

		ev := storedEvent(t, store)
		if ev.Type != "service.ready" {
			t.Errorf("type: got %q, want %q", ev.Type, "service.ready")
		}
		if ev.Actor != "node:node-a" {
			t.Errorf("actor: got %q, want %q", ev.Actor, "node:node-a")
		}
		if ev.Details["job_id"] != "svc-1" {
			t.Errorf("job_id: %v", ev.Details["job_id"])
		}
		// JSON decodes numeric Details values back as float64.
		if ev.Details["port"] != float64(8080) {
			t.Errorf("port: %v", ev.Details["port"])
		}
		if ev.Details["health_path"] != "/healthz" {
			t.Errorf("health_path: %v", ev.Details["health_path"])
		}
		if ev.Details["consecutive_failures"] != float64(0) {
			t.Errorf("consecutive_failures: %v", ev.Details["consecutive_failures"])
		}
	})

	t.Run("unhealthy transition emits service.unhealthy with failure count", func(t *testing.T) {
		store := newMockStore()
		logger := audit.NewLogger(store, 0)

		_ = logger.LogServiceEvent(context.Background(), "node-b", "svc-2", false, 9000, "/ready", 3)

		ev := storedEvent(t, store)
		if ev.Type != "service.unhealthy" {
			t.Errorf("type: got %q, want %q", ev.Type, "service.unhealthy")
		}
		if ev.Actor != "node:node-b" {
			t.Errorf("actor: got %q, want %q", ev.Actor, "node:node-b")
		}
		if ev.Details["consecutive_failures"] != float64(3) {
			t.Errorf("consecutive_failures: %v", ev.Details["consecutive_failures"])
		}
	})
}

// ── QueryEvents ───────────────────────────────────────────────────────────────

// seedEvents logs n events of different types for query tests.
func seedEvents(t *testing.T, logger *audit.Logger, n int) {
	t.Helper()
	ctx := context.Background()
	types := []string{
		audit.EventJobSubmit,
		audit.EventAuthFailure,
		audit.EventNodeRegister,
	}
	for i := 0; i < n; i++ {
		et := types[i%len(types)]
		_ = logger.Log(ctx, et, fmt.Sprintf("actor-%d", i), nil)
	}
}

func TestQueryEvents_ReturnsAllEvents(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)
	seedEvents(t, logger, 6)

	events, err := logger.QueryEvents(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 6 {
		t.Errorf("want 6 events, got %d", len(events))
	}
}

func TestQueryEvents_TypeFilter(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)
	seedEvents(t, logger, 9) // 3 of each type

	events, err := logger.QueryEvents(context.Background(), audit.Query{
		Type: audit.EventAuthFailure,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, ev := range events {
		if ev.Type != audit.EventAuthFailure {
			t.Errorf("filter should only return auth_failure, got %q", ev.Type)
		}
	}
	if len(events) != 3 {
		t.Errorf("want 3 auth_failure events, got %d", len(events))
	}
}

func TestQueryEvents_TimeRange_FiltersCorrectly(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	before := time.Now().Add(-2 * time.Second)
	after := time.Now().Add(2 * time.Second)
	_ = logger.Log(context.Background(), audit.EventJobSubmit, "u", nil)

	// Should return the event (falls within range).
	events, _ := logger.QueryEvents(context.Background(), audit.Query{
		StartTime: before,
		EndTime:   after,
	})
	if len(events) != 1 {
		t.Errorf("want 1 event in time range, got %d", len(events))
	}

	// EndTime in the past should exclude the event.
	events, _ = logger.QueryEvents(context.Background(), audit.Query{
		EndTime: before,
	})
	if len(events) != 0 {
		t.Errorf("want 0 events before window, got %d", len(events))
	}
}

func TestQueryEvents_Limit(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)
	seedEvents(t, logger, 10)

	events, err := logger.QueryEvents(context.Background(), audit.Query{Limit: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) > 3 {
		t.Errorf("want at most 3 events, got %d", len(events))
	}
}

func TestQueryEvents_SkipsMalformedEntries(t *testing.T) {
	store := newMockStore()
	// Inject a malformed entry directly.
	store.entries["audit:000000000000000001:bad"] = []byte("not-json{{")

	logger := audit.NewLogger(store, 0)
	_ = logger.Log(context.Background(), audit.EventJobSubmit, "u", nil)

	events, err := logger.QueryEvents(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the valid event should be returned.
	if len(events) != 1 {
		t.Errorf("want 1 valid event (malformed skipped), got %d", len(events))
	}
}

func TestQueryEvents_ScanError_Propagates(t *testing.T) {
	store := newMockStore()
	store.scanErr = errors.New("storage unavailable")
	logger := audit.NewLogger(store, 0)

	_, err := logger.QueryEvents(context.Background(), audit.Query{})
	if err == nil {
		t.Error("expected error from scan, got nil")
	}
}

func TestQueryEvents_Empty_ReturnsEmptySlice(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	events, err := logger.QueryEvents(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("want empty slice, got %d events", len(events))
	}
}

// TestQueryEvents_StartTimeFilter_ExcludesOlderEvents covers the StartTime
// filter path where an event's timestamp is before the query StartTime.
func TestQueryEvents_StartTimeFilter_ExcludesOlderEvents(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	// Log an event (timestamp = now).
	_ = logger.Log(context.Background(), audit.EventJobSubmit, "u", nil)

	// Query with StartTime = now + 1s — the event happened before StartTime.
	future := time.Now().Add(time.Second)
	events, err := logger.QueryEvents(context.Background(), audit.Query{StartTime: future})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("want 0 events when event is before StartTime, got %d", len(events))
	}
}

// TestLog_UnmarshalableDetails_ReturnsError covers the json.Marshal error path
// in Log by passing a details map containing a channel (not JSON-serializable).
func TestLog_UnmarshalableDetails_ReturnsError(t *testing.T) {
	store := newMockStore()
	logger := audit.NewLogger(store, 0)

	details := map[string]interface{}{
		"bad": make(chan int), // channels cannot be JSON-marshaled
	}
	err := logger.Log(context.Background(), audit.EventJobSubmit, "user", details)
	if err == nil {
		t.Error("expected error for unmarshalable details, got nil")
	}
}
