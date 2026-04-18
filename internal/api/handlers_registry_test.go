package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

// newRegistryServer returns a test server with auth disabled and the
// registry wired to an in-memory BadgerDB. Mirrors the
// newWorkflowServer helper from the workflows suite.
func newRegistryServer(t *testing.T) *api.Server {
	t.Helper()
	srv := newServer(newMockJobStore(), nil, nil)

	opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv.SetRegistryStore(registry.NewBadgerStore(db))
	return srv
}

func newRegistryServerWithBus(t *testing.T) (*api.Server, *events.Bus) {
	srv := newRegistryServer(t)
	bus := events.NewBus(16, nil)
	srv.SetEventBus(bus)
	return srv, bus
}

// newRegistryServerWithAudit wires a real audit logger + in-memory
// store so tests can assert that register/delete ops produce audit
// entries on the bus. Mirrors newAnalyticsServerWithAudit.
func newRegistryServerWithAudit(t *testing.T) (*api.Server, *inMemoryAuditStore) {
	t.Helper()
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()
	opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv.SetRegistryStore(registry.NewBadgerStore(db))
	return srv, store
}

// ── Dataset ────────────────────────────────────────────────────────────

func TestRegistry_Dataset_RegisterAndGet(t *testing.T) {
	srv := newRegistryServer(t)
	body := `{
		"name": "iris",
		"version": "v1.0.0",
		"uri":  "s3://helion/datasets/iris/v1.0.0.parquet",
		"size_bytes": 1024,
		"sha256": "deadbeef",
		"tags": {"team": "ml"}
	}`
	rr := do(srv, "POST", "/api/datasets", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST got %d: %s", rr.Code, rr.Body.String())
	}

	rr = do(srv, "GET", "/api/datasets/iris/v1.0.0", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET got %d", rr.Code)
	}
	var got api.DatasetResponse
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if got.URI != "s3://helion/datasets/iris/v1.0.0.parquet" || got.Tags["team"] != "ml" {
		t.Fatalf("roundtrip: %+v", got)
	}
	// CreatedBy stamped as "anonymous" since auth is disabled.
	if got.CreatedBy != "anonymous" {
		t.Fatalf("CreatedBy: %q", got.CreatedBy)
	}
	// CreatedAt must be set.
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt zero")
	}
}

func TestRegistry_Dataset_DuplicateVersion_Conflict(t *testing.T) {
	srv := newRegistryServer(t)
	body := `{"name":"d","version":"v1","uri":"s3://b/k"}`
	_ = do(srv, "POST", "/api/datasets", body)
	rr := do(srv, "POST", "/api/datasets", body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRegistry_Dataset_BadScheme_Rejected(t *testing.T) {
	srv := newRegistryServer(t)
	rr := do(srv, "POST", "/api/datasets", `{"name":"d","version":"v1","uri":"http://evil/x"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "file://") {
		t.Fatalf("expected scheme error: %s", rr.Body.String())
	}
}

func TestRegistry_Dataset_NotFound(t *testing.T) {
	srv := newRegistryServer(t)
	rr := do(srv, "GET", "/api/datasets/never/v1", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestRegistry_Dataset_List_Paginated(t *testing.T) {
	srv := newRegistryServer(t)
	for i := 0; i < 3; i++ {
		v := "v" + string(rune('a'+i))
		_ = do(srv, "POST", "/api/datasets",
			`{"name":"d","version":"`+v+`","uri":"s3://b/k"}`)
		time.Sleep(5 * time.Millisecond) // distinct CreatedAt for sort
	}
	rr := do(srv, "GET", "/api/datasets", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	var got api.DatasetListResponse
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if got.Total != 3 || len(got.Datasets) != 3 {
		t.Fatalf("counts: total=%d len=%d", got.Total, len(got.Datasets))
	}
}

func TestRegistry_Dataset_Delete(t *testing.T) {
	srv := newRegistryServer(t)
	_ = do(srv, "POST", "/api/datasets", `{"name":"d","version":"v1","uri":"s3://b/k"}`)
	rr := do(srv, "DELETE", "/api/datasets/d/v1", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	rr = do(srv, "GET", "/api/datasets/d/v1", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rr.Code)
	}
}

func TestRegistry_Dataset_RegisterPublishesEvent(t *testing.T) {
	srv, bus := newRegistryServerWithBus(t)
	sub := bus.Subscribe(events.TopicDatasetRegistered)
	defer sub.Cancel()

	_ = do(srv, "POST", "/api/datasets",
		`{"name":"d","version":"v1","uri":"s3://b/k","size_bytes":42}`)

	select {
	case evt := <-sub.C:
		if evt.Data["name"] != "d" || evt.Data["version"] != "v1" {
			t.Fatalf("event data: %+v", evt.Data)
		}
		if evt.Data["size_bytes"].(int64) != 42 {
			t.Fatalf("size_bytes: %+v", evt.Data["size_bytes"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no dataset.registered event")
	}
}

// ── Model ──────────────────────────────────────────────────────────────

func TestRegistry_Model_RegisterAndGetWithLineage(t *testing.T) {
	srv := newRegistryServer(t)
	body := `{
		"name": "resnet",
		"version": "v0.1",
		"uri": "s3://helion/jobs/train/out/model.pt",
		"framework": "pytorch",
		"source_job_id": "train-1",
		"source_dataset": {"name": "imagenet", "version": "v2"},
		"metrics": {"top1": 0.76, "top5": 0.93}
	}`
	rr := do(srv, "POST", "/api/models", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST: %d %s", rr.Code, rr.Body.String())
	}

	rr = do(srv, "GET", "/api/models/resnet/v0.1", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: %d", rr.Code)
	}
	var got api.ModelResponse
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if got.SourceJobID != "train-1" {
		t.Fatalf("SourceJobID: %q", got.SourceJobID)
	}
	if got.SourceDataset == nil || got.SourceDataset.Name != "imagenet" {
		t.Fatalf("SourceDataset: %+v", got.SourceDataset)
	}
	if got.Metrics["top1"] != 0.76 {
		t.Fatalf("metrics: %+v", got.Metrics)
	}
}

func TestRegistry_Model_PartialLineage_Rejected(t *testing.T) {
	srv := newRegistryServer(t)
	// source_dataset.name without version is a partial lineage pointer.
	rr := do(srv, "POST", "/api/models", `{
		"name":"r","version":"v1","uri":"s3://b/k",
		"source_dataset":{"name":"imagenet"}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRegistry_Model_Latest_ReturnsMostRecent(t *testing.T) {
	srv := newRegistryServer(t)
	// Register v1 then v2; /latest returns the most recent by
	// wall-clock (not semantic order).
	_ = do(srv, "POST", "/api/models", `{"name":"m","version":"v1","uri":"s3://b/k"}`)
	time.Sleep(10 * time.Millisecond)
	_ = do(srv, "POST", "/api/models", `{"name":"m","version":"v2","uri":"s3://b/k"}`)

	rr := do(srv, "GET", "/api/models/m/latest", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("latest: %d %s", rr.Code, rr.Body.String())
	}
	var got api.ModelResponse
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if got.Version != "v2" {
		t.Fatalf("latest version: %q (want v2)", got.Version)
	}
}

func TestRegistry_Model_Latest_NotFound(t *testing.T) {
	srv := newRegistryServer(t)
	rr := do(srv, "GET", "/api/models/nothing/latest", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestRegistry_Model_Metrics_NaNRejected(t *testing.T) {
	srv := newRegistryServer(t)
	// JSON encoder won't let us ship NaN literally, but a malicious
	// request could still include "metrics": {"x": 1e400} which
	// parses as +Inf. Validator catches it.
	rr := do(srv, "POST", "/api/models", `{
		"name":"r","version":"v1","uri":"s3://b/k",
		"metrics":{"bad":1e400}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRegistry_Model_DeletePublishesEvent(t *testing.T) {
	srv, bus := newRegistryServerWithBus(t)
	_ = do(srv, "POST", "/api/models", `{"name":"m","version":"v1","uri":"s3://b/k"}`)
	sub := bus.Subscribe(events.TopicModelDeleted)
	defer sub.Cancel()

	rr := do(srv, "DELETE", "/api/models/m/v1", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	select {
	case evt := <-sub.C:
		if evt.Data["name"] != "m" || evt.Data["version"] != "v1" {
			t.Fatalf("event data: %+v", evt.Data)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no model.deleted event")
	}
}

// TestRegistry_Model_RegisterPublishesEvent pins the
// `model.registered` event payload, which carries the lineage
// fields (source_job_id + source_dataset) that the analytics
// pipeline uses to build the training graph. The companion
// dataset-register event test asserts only name/version/size; the
// model-register event was entirely unpinned before this one, so a
// refactor that dropped the lineage half of ModelRegistered (the
// `if sourceJobID != ""` block) would leave the graph silently
// blank while every existing registry test stayed green. Same
// silent-regression class as audits 2026-04-15-07 (ml.resolve_failed
// observer) and 2026-04-18-01 (job.unschedulable reason).
func TestRegistry_Model_RegisterPublishesEvent(t *testing.T) {
	srv, bus := newRegistryServerWithBus(t)
	sub := bus.Subscribe(events.TopicModelRegistered)
	defer sub.Cancel()

	_ = do(srv, "POST", "/api/models", `{
		"name": "resnet",
		"version": "v0.1",
		"uri": "s3://helion/jobs/train/out/model.pt",
		"framework": "pytorch",
		"source_job_id": "train-1",
		"source_dataset": {"name": "imagenet", "version": "v2"}
	}`)

	select {
	case evt := <-sub.C:
		if evt.Data["name"] != "resnet" || evt.Data["version"] != "v0.1" {
			t.Fatalf("event name/version: %+v", evt.Data)
		}
		if evt.Data["uri"] != "s3://helion/jobs/train/out/model.pt" {
			t.Errorf("event uri: %v", evt.Data["uri"])
		}
		// Auth is disabled in the test server; actor is "anonymous".
		if evt.Data["actor"] != "anonymous" {
			t.Errorf("event actor: %v", evt.Data["actor"])
		}
		if evt.Data["source_job_id"] != "train-1" {
			t.Errorf("event source_job_id: %v", evt.Data["source_job_id"])
		}
		ds, _ := evt.Data["source_dataset"].(map[string]string)
		if ds["name"] != "imagenet" || ds["version"] != "v2" {
			t.Errorf("event source_dataset: %+v", evt.Data["source_dataset"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no model.registered event")
	}
}

// TestRegistry_OversizedBody_Returns400 pins the 1 MiB POST cap
// shared with job submit. Without a test, a caller could stream
// multi-MiB free-form tags before validation runs. Matches the
// job-submit oversize test shape so regressions in
// maxSubmitBodyBytes wiring get caught uniformly across all
// registry POST handlers. Datasets and models share the same cap
// via the handlers' `http.MaxBytesReader` calls at lines 83 +
// 234; the dataset path is representative.
func TestRegistry_OversizedBody_Returns400(t *testing.T) {
	srv := newRegistryServer(t)

	prefix := `{"name":"big","version":"v1","uri":"s3://b/k","tags":{"x":"`
	suffix := `"}}`
	padding := strings.Repeat("x", 1<<20+1) // just over 1 MiB
	body := prefix + padding + suffix

	rr := do(srv, "POST", "/api/datasets", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("oversized body: want 400, got %d", rr.Code)
	}
}

// TestRegistry_RateLimit_ExcessReturns429 pins the per-subject
// registryQueryAllow limiter (2 rps burst 30). Without this test,
// a refactor that broke the preflight ordering (limiter check
// before handler logic) would expose registry writes to
// unlimited hammer requests — and every existing success test
// stays under the burst so nothing would trip. Mirrors the
// analytics `TestAnalytics_RateLimit_ExcessReturns429` shape.
func TestRegistry_RateLimit_ExcessReturns429(t *testing.T) {
	srv := newRegistryServer(t)

	// Burst is 30; send 100 GETs to exhaust and see at least one
	// 429. Use GET /api/datasets (list endpoint) because POST
	// would also spend cycles through validation and the body
	// cap — the limiter sits before both.
	seen429 := false
	for i := 0; i < 100; i++ {
		rr := do(srv, "GET", "/api/datasets", "")
		if rr.Code == http.StatusTooManyRequests {
			seen429 = true
			break
		}
	}
	if !seen429 {
		t.Error("expected at least one 429 after exhausting registryQueryAllow burst")
	}
}

// TestRegistry_AuditLog_RegisterAndDelete_BothResources pins the
// audit-log emission path, independent of the event bus. The
// handler emits BOTH a bus event (covered by
// `TestRegistry_Dataset_RegisterPublishesEvent` + peers) AND an
// audit-log entry via `s.audit.Log` (at handlers_registry.go:122,
// 210, 280, 391). Audit 2026-04-18-03 pinned the event-bus side;
// the audit-log side was still unpinned. A refactor that extracted
// the audit call into a helper and forgot to wire it from one
// endpoint, or renamed `dataset.registered` to something else,
// would leave every existing registry test green while silently
// breaking the compliance / audit-query path.
//
// One integration test covering all four emissions (register +
// delete on both resources) is sufficient — they all use the same
// `s.audit.Log` pattern, so a refactor breaking one is likely to
// break all of them. Decoding each scan result as a JSON map
// (audit.Event shape) confirms the event_type key is present on
// each entry.
func TestRegistry_AuditLog_RegisterAndDelete_BothResources(t *testing.T) {
	srv, store := newRegistryServerWithAudit(t)

	if rr := do(srv, "POST", "/api/datasets",
		`{"name":"d","version":"v1","uri":"s3://b/k"}`); rr.Code != http.StatusCreated {
		t.Fatalf("register dataset: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(srv, "POST", "/api/models",
		`{"name":"m","version":"v1","uri":"s3://b/k"}`); rr.Code != http.StatusCreated {
		t.Fatalf("register model: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(srv, "DELETE", "/api/datasets/d/v1", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete dataset: %d", rr.Code)
	}
	if rr := do(srv, "DELETE", "/api/models/m/v1", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete model: %d", rr.Code)
	}

	// Scan every audit entry; each is a JSON `audit.Event`.
	raws, err := store.Scan(nil, "audit:", 0)
	if err != nil {
		t.Fatalf("audit scan: %v", err)
	}
	want := map[string]bool{
		"dataset.registered": false,
		"dataset.deleted":    false,
		"model.registered":   false,
		"model.deleted":      false,
	}
	for _, raw := range raws {
		var evt struct {
			Type  string `json:"type"`
			Actor string `json:"actor"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("unmarshal audit entry: %v", err)
		}
		if _, tracked := want[evt.Type]; tracked {
			want[evt.Type] = true
			// Auth is disabled; actor stamped as "anonymous".
			if evt.Actor != "anonymous" {
				t.Errorf("event %q: actor %q, want %q", evt.Type, evt.Actor, "anonymous")
			}
		}
	}
	for topic, seen := range want {
		if !seen {
			t.Errorf("audit entry for %q missing", topic)
		}
	}
}

// ── Gating ─────────────────────────────────────────────────────────────

func TestRegistry_Gated_WhenStoreNotWired(t *testing.T) {
	// Server without SetRegistryStore — the routes aren't registered,
	// so the HTTP mux returns 404 from its own not-found handler
	// (not our handler's 404). Either outcome is a clean "registry
	// not available on this coordinator."
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/api/datasets", `{"name":"d","version":"v1","uri":"s3://b/k"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}
