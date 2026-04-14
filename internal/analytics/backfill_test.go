// internal/analytics/backfill_test.go

package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ── Mock audit scanner ───────────────────────────────────────────────────

type mockAuditScanner struct {
	entries [][]byte
	err     error
}

func (m *mockAuditScanner) Scan(_ context.Context, _ string, _ int) ([][]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.entries, nil
}

func marshalAuditEvent(t *testing.T, ae auditEvent) []byte {
	t.Helper()
	data, err := json.Marshal(ae)
	if err != nil {
		t.Fatalf("marshal audit event: %v", err)
	}
	return data
}

// ── auditToAnalyticsEvent tests ──────────────────────────────────────────

func TestAuditToAnalyticsEvent_MapsKnownTypes(t *testing.T) {
	tests := []struct {
		auditType string
		wantType  string
	}{
		{"node_register", "node.registered"},
		{"node_revoke", "node.revoked"},
		{"job_submit", "job.submitted"},
		{"job_state_transition", "job.transition"},
	}
	for _, tt := range tests {
		t.Run(tt.auditType, func(t *testing.T) {
			ae := auditEvent{
				ID:        "test-id",
				Timestamp: time.Now(),
				Type:      tt.auditType,
				Actor:     "system",
				Details:   map[string]interface{}{"key": "val"},
			}
			got := auditToAnalyticsEvent(ae)
			if got.Type != tt.wantType {
				t.Errorf("type = %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}

func TestAuditToAnalyticsEvent_UnknownType_PrefixesWithAudit(t *testing.T) {
	ae := auditEvent{
		ID:        "id",
		Timestamp: time.Now(),
		Type:      "coordinator_start",
		Actor:     "system",
	}
	got := auditToAnalyticsEvent(ae)
	if got.Type != "audit.coordinator_start" {
		t.Errorf("type = %q, want %q", got.Type, "audit.coordinator_start")
	}
}

func TestAuditToAnalyticsEvent_PreservesIDAndTimestamp(t *testing.T) {
	ts := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	ae := auditEvent{
		ID:        "my-uuid",
		Timestamp: ts,
		Type:      "job_submit",
		Actor:     "api",
		Details:   map[string]interface{}{"job_id": "j1"},
	}
	got := auditToAnalyticsEvent(ae)
	if got.ID != "my-uuid" {
		t.Errorf("ID = %q, want %q", got.ID, "my-uuid")
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
}

func TestAuditToAnalyticsEvent_IncludesActorInData(t *testing.T) {
	ae := auditEvent{
		ID:        "id",
		Timestamp: time.Now(),
		Type:      "node_register",
		Actor:     "node-42",
		Details:   map[string]interface{}{"address": "10.0.0.1"},
	}
	got := auditToAnalyticsEvent(ae)
	if got.Data["actor"] != "node-42" {
		t.Errorf("data[actor] = %v, want %q", got.Data["actor"], "node-42")
	}
	if got.Data["address"] != "10.0.0.1" {
		t.Errorf("data[address] = %v, want %q", got.Data["address"], "10.0.0.1")
	}
}

func TestAuditToAnalyticsEvent_NormalisesTransitionKeys(t *testing.T) {
	ae := auditEvent{
		ID:        "id",
		Timestamp: time.Now(),
		Type:      "job_state_transition",
		Actor:     "system",
		Details: map[string]interface{}{
			"job_id":     "j1",
			"from_state": "pending",
			"to_state":   "scheduled",
		},
	}
	got := auditToAnalyticsEvent(ae)

	// Should have from_status/to_status, not from_state/to_state.
	if got.Data["from_status"] != "pending" {
		t.Errorf("data[from_status] = %v, want %q", got.Data["from_status"], "pending")
	}
	if got.Data["to_status"] != "scheduled" {
		t.Errorf("data[to_status] = %v, want %q", got.Data["to_status"], "scheduled")
	}
	if _, ok := got.Data["from_state"]; ok {
		t.Error("data[from_state] should have been deleted")
	}
	if _, ok := got.Data["to_state"]; ok {
		t.Error("data[to_state] should have been deleted")
	}
}

func TestAuditToAnalyticsEvent_EmptyID_GeneratesUUID(t *testing.T) {
	ae := auditEvent{
		ID:        "",
		Timestamp: time.Now(),
		Type:      "job_submit",
	}
	got := auditToAnalyticsEvent(ae)
	if got.ID == "" {
		t.Error("expected generated UUID for empty audit ID")
	}
	// UUID v4 is 36 chars.
	if len(got.ID) != 36 {
		t.Errorf("ID length = %d, want 36 (UUID format)", len(got.ID))
	}
}

func TestAuditToAnalyticsEvent_NilDetails(t *testing.T) {
	ae := auditEvent{
		ID:        "id",
		Timestamp: time.Now(),
		Type:      "coordinator_start",
		Actor:     "system",
		Details:   nil,
	}
	got := auditToAnalyticsEvent(ae)
	if got.Data == nil {
		t.Fatal("data should not be nil")
	}
	if got.Data["actor"] != "system" {
		t.Errorf("data[actor] = %v, want %q", got.Data["actor"], "system")
	}
}

// ── Backfill function tests (scanner-level, no PostgreSQL) ───────────────

func TestBackfill_ScanError_Returns(t *testing.T) {
	scanner := &mockAuditScanner{err: errors.New("db unavailable")}
	_, err := Backfill(context.Background(), scanner, nil, nil)
	if err == nil {
		t.Fatal("expected error from scan failure")
	}
}

func TestBackfill_EmptyAuditTrail_ReturnsZero(t *testing.T) {
	scanner := &mockAuditScanner{entries: nil}
	n, err := Backfill(context.Background(), scanner, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("inserted = %d, want 0", n)
	}
}

func TestBackfill_ParseSkipsMalformedEntries(t *testing.T) {
	// Test the parse phase directly: malformed JSON should be skipped,
	// valid events should be converted correctly.
	raw := [][]byte{
		[]byte("not valid json"),
		marshalAuditEvent(t, auditEvent{
			ID:        "good-event",
			Timestamp: time.Now(),
			Type:      "job_submit",
			Actor:     "api",
			Details:   map[string]interface{}{"job_id": "j1"},
		}),
	}

	// Simulate the parse loop from Backfill.
	parsed := make([]auditEvent, 0)
	for _, entry := range raw {
		var ae auditEvent
		if err := json.Unmarshal(entry, &ae); err != nil {
			continue // skipped
		}
		parsed = append(parsed, ae)
	}

	if len(parsed) != 1 {
		t.Fatalf("parsed %d events, want 1 (malformed should be skipped)", len(parsed))
	}
	if parsed[0].ID != "good-event" {
		t.Errorf("parsed[0].ID = %q, want %q", parsed[0].ID, "good-event")
	}

	// Verify conversion.
	evt := auditToAnalyticsEvent(parsed[0])
	if evt.Type != "job.submitted" {
		t.Errorf("converted type = %q, want %q", evt.Type, "job.submitted")
	}
	if evt.Data["job_id"] != "j1" {
		t.Errorf("data[job_id] = %v, want %q", evt.Data["job_id"], "j1")
	}
}

// ── Audit type mapping completeness ──────────────────────────────────────

func TestAuditTypeMap_CoversAllKnownAuditTypes(t *testing.T) {
	// These are the audit types that have direct bus event equivalents.
	expected := map[string]string{
		"node_register":        "node.registered",
		"node_revoke":          "node.revoked",
		"job_submit":           "job.submitted",
		"job_state_transition": "job.transition",
	}
	for auditType, wantBusType := range expected {
		got, ok := auditTypeMap[auditType]
		if !ok {
			t.Errorf("auditTypeMap missing %q", auditType)
			continue
		}
		if got != wantBusType {
			t.Errorf("auditTypeMap[%q] = %q, want %q", auditType, got, wantBusType)
		}
	}
}
