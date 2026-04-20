// internal/events/topics_test.go
//
// Event-constructor invariants. Each test guards a single public
// constructor, asserting topic + payload keys. These are tiny
// functions but the shape is load-bearing — analytics subscribers
// and retention sweeps key off every field.

package events_test

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// ── Helpers ──────────────────────────────────────────────────

func dataMap(t *testing.T, e events.Event) map[string]any {
	t.Helper()
	if e.Data == nil {
		t.Fatalf("Event.Data is nil")
	}
	return e.Data
}

func assertTopic(t *testing.T, e events.Event, want string) {
	t.Helper()
	if e.Type != want {
		t.Errorf("topic: got %q, want %q", e.Type, want)
	}
}

func assertString(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	got, ok := m[key].(string)
	if !ok {
		t.Errorf("%s: missing or not a string (%T)", key, m[key])
		return
	}
	if got != want {
		t.Errorf("%s: got %q, want %q", key, got, want)
	}
}

// ── JobCompletedWithOutputs ──────────────────────────────────

func TestJobCompletedWithOutputs_WithArtifacts(t *testing.T) {
	e := events.JobCompletedWithOutputs("j1", "node-a", 1234, []events.ArtifactSummary{
		{Name: "out1", URI: "artifacts://abc", SHA256: "deadbeef"},
		{Name: "out2", URI: "artifacts://xyz"}, // no sha256
	})
	assertTopic(t, e, events.TopicJobCompleted)
	d := dataMap(t, e)
	assertString(t, d, "job_id", "j1")
	rows, ok := d["outputs"].([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("outputs: got %T len %d, want 2-element rows", d["outputs"], len(rows))
	}
	if rows[0]["sha256"] != "deadbeef" {
		t.Errorf("out1 sha256 missing: %v", rows[0])
	}
	if _, present := rows[1]["sha256"]; present {
		t.Errorf("out2 sha256 must be absent: %v", rows[1])
	}
}

func TestJobCompletedWithOutputs_NoOutputs_NoOutputsKey(t *testing.T) {
	e := events.JobCompletedWithOutputs("j1", "node-a", 99, nil)
	d := dataMap(t, e)
	if _, present := d["outputs"]; present {
		t.Error("empty outputs must not add 'outputs' key")
	}
}

// ── NodeRegisteredWithLabels ─────────────────────────────────

func TestNodeRegisteredWithLabels_DefensiveCopy(t *testing.T) {
	src := map[string]string{"gpu": "a100"}
	e := events.NodeRegisteredWithLabels("n1", "10.0.0.1:7001", src)
	d := dataMap(t, e)

	// Mutate the source after construction — the event payload must
	// be unaffected (defensive copy invariant).
	src["gpu"] = "MUTATED"
	labels, ok := d["labels"].(map[string]string)
	if !ok {
		t.Fatalf("labels missing or wrong type: %T", d["labels"])
	}
	if labels["gpu"] != "a100" {
		t.Errorf("defensive copy broken: %q", labels["gpu"])
	}
}

func TestNodeRegisteredWithLabels_EmptyLabels_NoKey(t *testing.T) {
	e := events.NodeRegisteredWithLabels("n1", "10.0.0.1:7001", nil)
	d := dataMap(t, e)
	if _, present := d["labels"]; present {
		t.Error("empty labels must not add 'labels' key")
	}
}

// ── Dataset / Model ──────────────────────────────────────────

func TestDatasetRegistered_ShapeAndTopic(t *testing.T) {
	e := events.DatasetRegistered("iris", "v1", "artifacts://ds", "alice", 4096)
	assertTopic(t, e, events.TopicDatasetRegistered)
	d := dataMap(t, e)
	assertString(t, d, "name", "iris")
	assertString(t, d, "version", "v1")
	assertString(t, d, "actor", "alice")
	if d["size_bytes"].(int64) != 4096 {
		t.Errorf("size_bytes: got %v", d["size_bytes"])
	}
}

func TestDatasetDeleted_Shape(t *testing.T) {
	e := events.DatasetDeleted("iris", "v1", "alice")
	assertTopic(t, e, events.TopicDatasetDeleted)
	d := dataMap(t, e)
	assertString(t, d, "actor", "alice")
}

func TestModelRegistered_WithLineage(t *testing.T) {
	e := events.ModelRegistered("mnist", "v2", "artifacts://m", "alice", "job-42", "mnist-ds", "v3")
	d := dataMap(t, e)
	assertString(t, d, "source_job_id", "job-42")
	ds, ok := d["source_dataset"].(map[string]string)
	if !ok {
		t.Fatalf("source_dataset wrong type: %T", d["source_dataset"])
	}
	if ds["name"] != "mnist-ds" || ds["version"] != "v3" {
		t.Errorf("source_dataset: %+v", ds)
	}
}

func TestModelRegistered_NoLineage_OmitsKeys(t *testing.T) {
	e := events.ModelRegistered("mnist", "v2", "artifacts://m", "alice", "", "", "")
	d := dataMap(t, e)
	if _, present := d["source_job_id"]; present {
		t.Error("empty source_job_id must be absent")
	}
	if _, present := d["source_dataset"]; present {
		t.Error("empty source_dataset must be absent")
	}
}

func TestModelDeleted_Shape(t *testing.T) {
	e := events.ModelDeleted("mnist", "v2", "alice")
	assertTopic(t, e, events.TopicModelDeleted)
	d := dataMap(t, e)
	assertString(t, d, "name", "mnist")
}

// ── JobUnschedulable (defensive copy) ────────────────────────

func TestJobUnschedulable_DefensiveSelectorCopy(t *testing.T) {
	sel := map[string]string{"gpu": "a100"}
	e := events.JobUnschedulable("j1", sel, events.UnschedulableReasonNoMatchingLabel)
	d := dataMap(t, e)
	sel["gpu"] = "MUTATED"
	got := d["unsatisfied_selector"].(map[string]string)
	if got["gpu"] != "a100" {
		t.Errorf("defensive copy broken: %q", got["gpu"])
	}
	if d["reason"].(string) != events.UnschedulableReasonNoMatchingLabel {
		t.Errorf("reason: got %v", d["reason"])
	}
}

// ── ML resolve ───────────────────────────────────────────────

func TestMLResolveFailed_Shape(t *testing.T) {
	e := events.MLResolveFailed("j1", "w1", "upstream-job", "model", "output not produced")
	assertTopic(t, e, events.TopicMLResolveFailed)
	d := dataMap(t, e)
	assertString(t, d, "workflow_id", "w1")
	assertString(t, d, "upstream", "upstream-job")
}

// ── Feature 28 constructors ──────────────────────────────────

func TestSubmissionRecorded_Shape(t *testing.T) {
	e := events.SubmissionRecorded("alice", "alice@ops", "rest",
		"job", "j-1", false, true, "", "curl/8.0")
	assertTopic(t, e, events.TopicSubmissionRecorded)
	d := dataMap(t, e)
	assertString(t, d, "actor", "alice")
	assertString(t, d, "operator_cn", "alice@ops")
	if d["dry_run"].(bool) {
		t.Error("dry_run mismatched")
	}
	if !d["accepted"].(bool) {
		t.Error("accepted mismatched")
	}
}

func TestAuthOK_Shape(t *testing.T) {
	e := events.AuthOK("alice", "127.0.0.1", "curl/8.0")
	assertTopic(t, e, events.TopicAuthOK)
	d := dataMap(t, e)
	assertString(t, d, "actor", "alice")
	assertString(t, d, "remote_ip", "127.0.0.1")
}

func TestAuthFail_Shape(t *testing.T) {
	e := events.AuthFail(events.AuthFailReasonInvalidSignature, "alice", "127.0.0.1", "curl/8.0")
	assertTopic(t, e, events.TopicAuthFail)
	d := dataMap(t, e)
	assertString(t, d, "reason", events.AuthFailReasonInvalidSignature)
}

func TestAuthRateLimit_Shape(t *testing.T) {
	e := events.AuthRateLimit("alice", "/admin/tokens", "127.0.0.1")
	assertTopic(t, e, events.TopicAuthRateLimit)
	d := dataMap(t, e)
	assertString(t, d, "path", "/admin/tokens")
}

func TestAuthTokenMint_Shape(t *testing.T) {
	e := events.AuthTokenMint("admin", "bob", "user", 1)
	assertTopic(t, e, events.TopicAuthTokenMint)
	d := dataMap(t, e)
	assertString(t, d, "actor", "admin")
	assertString(t, d, "subject", "bob")
	if d["ttl_hours"].(int) != 1 {
		t.Errorf("ttl_hours: got %v", d["ttl_hours"])
	}
}

// ── Artifact transfer ────────────────────────────────────────

func TestArtifactUploaded_Shape(t *testing.T) {
	e := events.ArtifactUploaded("j-1", "artifacts://abc", 4096, 12)
	assertTopic(t, e, events.TopicArtifactUploaded)
	d := dataMap(t, e)
	if d["bytes"].(int64) != 4096 {
		t.Errorf("bytes: got %v", d["bytes"])
	}
	if d["duration_ms"].(int) != 12 {
		t.Errorf("duration_ms: got %v", d["duration_ms"])
	}
}

func TestArtifactDownloaded_WithVerifyResult(t *testing.T) {
	ok := true
	e := events.ArtifactDownloaded("j-1", "artifacts://abc", 4096, 7, &ok)
	d := dataMap(t, e)
	if d["sha256_ok"].(bool) != true {
		t.Errorf("sha256_ok: got %v", d["sha256_ok"])
	}
}

func TestArtifactDownloaded_NoVerify_OmitsKey(t *testing.T) {
	e := events.ArtifactDownloaded("j-1", "artifacts://abc", 4096, 7, nil)
	d := dataMap(t, e)
	if _, present := d["sha256_ok"]; present {
		t.Error("nil sha256OK must omit key")
	}
}

// ── Service probe ────────────────────────────────────────────

func TestServiceProbeTransition_Shape(t *testing.T) {
	e := events.ServiceProbeTransition("j-svc", "unhealthy", 3)
	assertTopic(t, e, events.TopicServiceProbeTransition)
	d := dataMap(t, e)
	assertString(t, d, "new_state", "unhealthy")
	if d["consecutive_fails"].(uint32) != 3 {
		t.Errorf("consecutive_fails: got %v", d["consecutive_fails"])
	}
}

// ── JobLog (uses NewEvent directly) ──────────────────────────

func TestJobLog_UsesCallerTimestamp(t *testing.T) {
	ts := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	e := events.JobLog("j-1", 42, ts, "line of log")
	if !e.Timestamp.Equal(ts) {
		t.Errorf("timestamp: got %v, want %v", e.Timestamp, ts)
	}
	d := dataMap(t, e)
	if d["seq"].(int64) != 42 {
		t.Errorf("seq: got %v", d["seq"])
	}
	assertString(t, d, "data", "line of log")
}
