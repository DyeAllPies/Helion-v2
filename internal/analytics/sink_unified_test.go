// internal/analytics/sink_unified_test.go
//
// Feature 28 — tests for the new sink upserts and PII hashing.
// Uses the existing mockConn/mockTx scaffolding from mock_db_test.go.

package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// helper — run one event through the sink's transactional flush
// and return the mock's captured tx exec calls. Each call is
// asserted by the test.
func flushOne(t *testing.T, cfg SinkConfig, evt events.Event) []execCall {
	t.Helper()
	mc := newMockConn()
	s := NewSink(mc, nil /*bus*/, cfg, nil /*log*/)
	if err := s.flush(context.Background(), []events.Event{evt}); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return mc.tx.ExecCalls()
}

// ── hashActorIfEnabled ─────────────────────────────────────────────────────

func TestHashActorIfEnabled_OffModeLeavesRaw(t *testing.T) {
	s := &Sink{cfg: SinkConfig{PIIMode: "", PIISalt: "s"}}
	if got := s.hashActorIfEnabled("alice"); got != "alice" {
		t.Errorf("off: want raw, got %q", got)
	}
}

func TestHashActorIfEnabled_HashMode(t *testing.T) {
	s := &Sink{cfg: SinkConfig{PIIMode: PIIModeHashActor, PIISalt: "pepper"}}
	got := s.hashActorIfEnabled("alice")
	if got == "alice" {
		t.Error("hash_actor: value should differ from raw")
	}
	// Deterministic: same input → same hash.
	if got != s.hashActorIfEnabled("alice") {
		t.Error("hash_actor: not deterministic")
	}
	// Verify shape: sha256-hex is 64 chars.
	if len(got) != 64 {
		t.Errorf("hash length: want 64, got %d", len(got))
	}
	// Cross-check against manual sha256.
	h := sha256.New()
	h.Write([]byte("pepper"))
	h.Write([]byte("alice"))
	want := hex.EncodeToString(h.Sum(nil))
	if got != want {
		t.Errorf("hash bytes differ from manual compute")
	}
}

func TestHashActorIfEnabled_EmptyActorReturnsEmpty(t *testing.T) {
	// Empty actor stays empty (not a fake hash of empty). Keeps the
	// dashboard's "no actor" case distinguishable from "hashed actor".
	s := &Sink{cfg: SinkConfig{PIIMode: PIIModeHashActor, PIISalt: "salt"}}
	if got := s.hashActorIfEnabled(""); got != "" {
		t.Errorf("empty: want empty, got %q", got)
	}
}

func TestHashActorIfEnabled_SaltMatters(t *testing.T) {
	// Different salts must produce different hashes so operators
	// changing the salt post-deployment get distinguishable rows.
	a := &Sink{cfg: SinkConfig{PIIMode: PIIModeHashActor, PIISalt: "salt-a"}}
	b := &Sink{cfg: SinkConfig{PIIMode: PIIModeHashActor, PIISalt: "salt-b"}}
	if a.hashActorIfEnabled("alice") == b.hashActorIfEnabled("alice") {
		t.Error("different salts produced same hash")
	}
}

// ── upsertSubmissionHistory ────────────────────────────────────────────────

func TestUpsertSubmissionHistory_WritesRow(t *testing.T) {
	evt := events.SubmissionRecorded(
		"alice", "alice@ops",
		events.SubmissionSourceDashboard, "job", "j-1",
		false, true, "", "Mozilla/5.0",
	)
	evt.Timestamp = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := flushOne(t, SinkConfig{}, evt)
	// First call is the batch-level events INSERT, subsequent calls
	// are the upserts. Look for the submission_history INSERT.
	found := false
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO submission_history") {
			found = true
			// Args: id, submitted_at, actor, operator_cn, source, kind, resource_id, dry_run, accepted, reject_reason, user_agent
			if c.Args[2] != "alice" {
				t.Errorf("actor arg: want alice, got %v", c.Args[2])
			}
			if c.Args[5] != "job" {
				t.Errorf("kind arg: want job, got %v", c.Args[5])
			}
			if c.Args[6] != "j-1" {
				t.Errorf("resource_id arg: want j-1, got %v", c.Args[6])
			}
		}
	}
	if !found {
		t.Error("expected submission_history INSERT in exec calls")
	}
}

func TestUpsertSubmissionHistory_PIIHashesActor(t *testing.T) {
	cfg := SinkConfig{PIIMode: PIIModeHashActor, PIISalt: "pepper"}
	evt := events.SubmissionRecorded(
		"alice", "alice@ops",
		events.SubmissionSourceDashboard, "job", "j-1",
		false, true, "", "",
	)
	calls := flushOne(t, cfg, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO submission_history") {
			actor, _ := c.Args[2].(string)
			if actor == "alice" {
				t.Error("PII leak: submission_history.actor should be hashed")
			}
			if len(actor) != 64 {
				t.Errorf("actor hash shape: want 64 hex, got %q", actor)
			}
			return
		}
	}
	t.Fatal("no submission_history INSERT")
}

// ── upsertUnschedulable ────────────────────────────────────────────────────

func TestUpsertUnschedulable_WritesRow(t *testing.T) {
	evt := events.JobUnschedulable("j-2", map[string]string{"runtime": "unicorn"}, events.UnschedulableReasonNoMatchingLabel)
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO unschedulable_events") {
			if c.Args[1] != "j-2" {
				t.Errorf("job_id arg: want j-2, got %v", c.Args[1])
			}
			// selector arg is []byte json
			b, ok := c.Args[2].([]byte)
			if !ok {
				t.Fatalf("selector arg: want []byte, got %T", c.Args[2])
			}
			if !strings.Contains(string(b), "unicorn") {
				t.Errorf("selector json: want unicorn, got %s", b)
			}
			return
		}
	}
	t.Error("no unschedulable_events INSERT")
}

// ── upsertRegistryMutation ─────────────────────────────────────────────────

func TestUpsertRegistryMutation_Dataset(t *testing.T) {
	evt := events.DatasetRegistered("iris", "v1", "s3://bucket/iris", "alice", 1024)
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO registry_mutations") {
			if c.Args[1] != "dataset" {
				t.Errorf("kind arg: want dataset, got %v", c.Args[1])
			}
			if c.Args[2] != "registered" {
				t.Errorf("action arg: want registered, got %v", c.Args[2])
			}
			return
		}
	}
	t.Error("no registry_mutations INSERT")
}

// ── upsertAuthEvent ────────────────────────────────────────────────────────

func TestUpsertAuthEvent_Fail_RecordsReason(t *testing.T) {
	evt := events.AuthFail(events.AuthFailReasonExpired, "alice", "192.0.2.1:12345", "curl/8")
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO auth_events") {
			if c.Args[1] != "auth_fail" {
				t.Errorf("event_type arg: want auth_fail, got %v", c.Args[1])
			}
			// Reason is arg 5.
			if c.Args[5] != events.AuthFailReasonExpired {
				t.Errorf("reason arg: want expired, got %v", c.Args[5])
			}
			return
		}
	}
	t.Error("no auth_events INSERT")
}

func TestUpsertAuthEvent_MalformedIPDroppedToNil(t *testing.T) {
	// Regression guard: a malformed IP must not fail the whole batch.
	evt := events.AuthFail(events.AuthFailReasonMissingToken, "", "not-an-ip", "")
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO auth_events") {
			// remote_ip is arg 3; sanitiseIP should return "" → nil.
			if c.Args[3] != nil {
				t.Errorf("malformed IP: want nil arg, got %v (%T)", c.Args[3], c.Args[3])
			}
			return
		}
	}
	t.Error("no auth_events INSERT")
}

// ── upsertServiceProbeEvent ────────────────────────────────────────────────

func TestUpsertServiceProbeEvent_WritesRow(t *testing.T) {
	evt := events.ServiceProbeTransition("j-svc", "ready", 3)
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO service_probe_events") {
			if c.Args[1] != "j-svc" {
				t.Errorf("job_id arg: want j-svc, got %v", c.Args[1])
			}
			if c.Args[2] != "ready" {
				t.Errorf("new_state arg: want ready, got %v", c.Args[2])
			}
			return
		}
	}
	t.Error("no service_probe_events INSERT")
}

// ── upsertJobLog ───────────────────────────────────────────────────────────

func TestUpsertJobLog_WritesChunk(t *testing.T) {
	evt := events.JobLog("j-logs", 42, time.Now(), "epoch 1: loss=0.25")
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.Contains(c.SQL, "INSERT INTO job_log_entries") {
			if c.Args[1] != "j-logs" {
				t.Errorf("job_id arg: want j-logs, got %v", c.Args[1])
			}
			if c.Args[2] != int64(42) {
				t.Errorf("seq arg: want 42 (int64), got %v (%T)", c.Args[2], c.Args[2])
			}
			if c.Args[3] != "epoch 1: loss=0.25" {
				t.Errorf("data arg: want log line, got %v", c.Args[3])
			}
			return
		}
	}
	t.Error("no job_log_entries INSERT")
}

// ── unknown event type ─────────────────────────────────────────────────────

func TestSink_UnknownEventType_NoUpsert(t *testing.T) {
	// An event type the sink doesn't know about must not crash and
	// must not produce an upsert. The raw `events` INSERT still fires.
	evt := events.NewEvent("totally.made.up", map[string]any{"x": 1})
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if strings.HasPrefix(c.SQL, "INSERT INTO ") && !strings.Contains(c.SQL, "INSERT INTO events ") {
			t.Errorf("unknown event produced an upsert: %s", c.SQL)
		}
	}
}

// ── sanitiseIP ─────────────────────────────────────────────────────────────

func TestSanitiseIP(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"1.2.3.4":         "1.2.3.4",
		"1.2.3.4:5678":    "1.2.3.4",
		"[::1]:8080":      "::1",
		"::1":             "::1",
		"2001:db8::1":     "2001:db8::1",
		"not-an-ip":       "",
		"[::1":            "",
		"999.999.999.999": "",
	}
	for in, want := range cases {
		if got := sanitiseIP(in); got != want {
			t.Errorf("sanitiseIP(%q) = %q, want %q", in, got, want)
		}
	}
}
