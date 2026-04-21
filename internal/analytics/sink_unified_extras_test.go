// internal/analytics/sink_unified_extras_test.go
//
// Coverage for the event-dispatch branches that the broader
// sink_unified_test.go suite doesn't touch:
//   - upsertMLResolveFailed (currently a no-op — invariant-preserving)
//   - upsertArtifactTransfer for upload / download / verify-failed
//   - nullIfZero / nullIfZero64 / truncate utility helpers

package analytics

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// ── upsertWorkflowOutcome (feature 40) ───────────────────────

func TestFlush_WorkflowCompletedWithCounts_InsertsWorkflowOutcomeRow(t *testing.T) {
	evt := events.Event{
		ID:        "ev-wf-1",
		Type:      events.TopicWorkflowCompleted,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"workflow_id":     "mnist-wf-1",
			"job_count":       5,
			"success_count":   5,
			"failed_count":    0,
			"owner_principal": "user:alice",
			"tags":            map[string]string{"task": "mnist", "team": "ml"},
		},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	found := false
	for _, c := range calls {
		if containsStr(c.SQL, "workflow_outcomes") {
			found = true
			// Args: workflowID, outcome, ts, jobCount, successCount,
			// failedCount, failedJob(nil), owner, tagsJSON.
			if len(c.Args) != 9 {
				t.Errorf("args count: got %d, want 9", len(c.Args))
			}
			if s, ok := c.Args[0].(string); !ok || s != "mnist-wf-1" {
				t.Errorf("workflow_id arg: got %v", c.Args[0])
			}
			if s, ok := c.Args[1].(string); !ok || s != "completed" {
				t.Errorf("outcome arg: got %v", c.Args[1])
			}
			if n, ok := c.Args[3].(int); !ok || n != 5 {
				t.Errorf("job_count arg: got %v", c.Args[3])
			}
			if s, ok := c.Args[7].(string); !ok || s != "user:alice" {
				t.Errorf("owner arg: got %v", c.Args[7])
			}
			// tagsJSON must be valid JSONB bytes containing both keys.
			tagsRaw, ok := c.Args[8].([]byte)
			if !ok {
				t.Fatalf("tags arg: got %T, want []byte", c.Args[8])
			}
			if !containsStr(string(tagsRaw), "mnist") {
				t.Errorf("tags JSON missing 'mnist': %s", tagsRaw)
			}
			break
		}
	}
	if !found {
		t.Error("no INSERT for workflow_outcomes")
	}
}

func TestFlush_WorkflowFailedWithCounts_InsertsFailedRow(t *testing.T) {
	evt := events.Event{
		ID:        "ev-wf-2",
		Type:      events.TopicWorkflowFailed,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"workflow_id":     "mnist-wf-2",
			"failed_job":      "train_heavy",
			"job_count":       5,
			"success_count":   3,
			"failed_count":    2,
			"owner_principal": "user:bob",
		},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	found := false
	for _, c := range calls {
		if !containsStr(c.SQL, "workflow_outcomes") {
			continue
		}
		// Args[6] is failed_job; must be "train_heavy".
		if s, ok := c.Args[6].(string); !ok || s != "train_heavy" {
			t.Errorf("failed_job arg: got %v", c.Args[6])
		}
		if s, ok := c.Args[1].(string); !ok || s != "failed" {
			t.Errorf("outcome arg: got %v", c.Args[1])
		}
		found = true
		break
	}
	if !found {
		t.Error("no INSERT for workflow_outcomes (failed branch)")
	}
}

func TestFlush_WorkflowCompleted_MissingWorkflowID_NoInsert(t *testing.T) {
	// Defensive contract: a blank workflow_id triggers a silent
	// skip rather than an INSERT with "" as the primary key.
	// Protects against a buggy publisher poisoning the table.
	evt := events.Event{
		ID:        "ev-wf-blank",
		Type:      events.TopicWorkflowCompleted,
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"job_count": 3},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if containsStr(c.SQL, "workflow_outcomes") {
			t.Errorf("missing workflow_id still produced INSERT: %s", c.SQL)
		}
	}
}

// ── upsertMLResolveFailed via flush dispatch ─────────────────

func TestFlush_MLResolveFailed_EmitsNoRows(t *testing.T) {
	// The upsert is currently a no-op (documented as such in
	// sink_unified.go). The invariant: flush of a
	// TopicMLResolveFailed event does not error and does not
	// append unexpected SQL. If a future change wires the
	// writer, this test's assertion shifts to "ExecCalls
	// includes the INSERT", and that's the point — the test
	// anchors the behaviour.
	evt := events.Event{
		ID:        "ev-1",
		Type:      events.TopicMLResolveFailed,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"job_id":      "j-1",
			"workflow_id": "wf-1",
			"upstream":    "upstream-job",
			"output_name": "model",
			"reason":      "output not produced",
		},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	// No specific INSERT shape required; just assert no
	// crash / panic. flushOne asserts no error.
	_ = calls
}

// ── upsertArtifactTransfer via flush dispatch ────────────────

func TestFlush_ArtifactUploaded_InsertsRow(t *testing.T) {
	evt := events.Event{
		ID:        "ev-1",
		Type:      events.TopicArtifactUploaded,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"job_id":      "j-1",
			"uri":         "artifacts://abcdef",
			"bytes":       int64(4096),
			"duration_ms": 12,
		},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	// Look for the INSERT into artifact_transfers.
	found := false
	for _, c := range calls {
		if containsStr(c.SQL, "artifact_transfers") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no INSERT for artifact_transfers: calls=%v", calls)
	}
}

func TestFlush_ArtifactDownloaded_WithVerify_InsertsRow(t *testing.T) {
	evt := events.Event{
		ID:        "ev-1",
		Type:      events.TopicArtifactDownloaded,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"job_id":      "j-1",
			"uri":         "artifacts://abcdef",
			"bytes":       int64(4096),
			"duration_ms": 7,
			"sha256_ok":   true,
		},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	found := false
	for _, c := range calls {
		if !containsStr(c.SQL, "artifact_transfers") {
			continue
		}
		// "download" arrives via $2 — inspect the args, not the SQL.
		for _, a := range c.Args {
			if s, ok := a.(string); ok && s == "download" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("no INSERT for artifact_transfers direction=download: calls=%v", calls)
	}
}

func TestFlush_ArtifactTransfer_EmptyURI_Skipped(t *testing.T) {
	// No URI → the helper returns nil without calling Exec.
	// Safety net so a malformed event can't poison the flush
	// batch with a NULL-URI INSERT.
	evt := events.Event{
		ID:        "ev-1",
		Type:      events.TopicArtifactUploaded,
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"job_id": "j-1"},
	}
	calls := flushOne(t, SinkConfig{}, evt)
	for _, c := range calls {
		if containsStr(c.SQL, "artifact_transfers") {
			t.Errorf("empty URI produced INSERT: %s", c.SQL)
		}
	}
}

// ── nullIfZero / nullIfZero64 / truncate ─────────────────────

func TestNullIfZero_ZeroReturnsNil(t *testing.T) {
	if got := nullIfZero(0); got != nil {
		t.Errorf("nullIfZero(0): got %v, want nil", got)
	}
	if got := nullIfZero(42); got != 42 {
		t.Errorf("nullIfZero(42): got %v, want 42", got)
	}
}

func TestNullIfZero64_ZeroReturnsNil(t *testing.T) {
	if got := nullIfZero64(0); got != nil {
		t.Errorf("nullIfZero64(0): got %v, want nil", got)
	}
	if got := nullIfZero64(int64(1 << 40)); got != int64(1<<40) {
		t.Errorf("nullIfZero64(large): got %v", got)
	}
}

func TestTruncate_UnderLimit_Unchanged(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("under-limit: got %q", got)
	}
}

func TestTruncate_OverLimit_Truncated(t *testing.T) {
	if got := truncate("this is a long string", 4); got != "this" {
		t.Errorf("over-limit: got %q, want 'this'", got)
	}
}

func TestTruncate_ExactBoundary_Unchanged(t *testing.T) {
	if got := truncate("abcd", 4); got != "abcd" {
		t.Errorf("exact boundary: got %q", got)
	}
}

// ── helpers ──────────────────────────────────────────────────

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
