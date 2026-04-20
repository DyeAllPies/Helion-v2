// internal/api/registry_delete_extras_test.go
//
// Happy-path + authz-denied tests for handleDeleteDataset /
// handleDeleteModel. The error-only tests in coverage_extras
// catch 404s; these cover the delete-succeeds + audit +
// event-publish + owner-vs-non-owner branches.

package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// ── Dataset ──────────────────────────────────────────────────

func TestRegistry_DeleteDataset_Owner_204(t *testing.T) {
	// Authenticated owner can delete their own dataset; audit
	// + event-publish branches fire.
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")

	body := `{
		"name": "iris", "version": "v1.0.0",
		"uri":  "s3://helion/datasets/iris/v1.0.0.parquet"
	}`
	rr := doWithToken(srv, "POST", "/api/datasets", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}

	// Owner deletes → 204, audit event + event-bus publish fire.
	rr = doWithToken(srv, "DELETE", "/api/datasets/iris/v1.0.0", "", aliceTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}

	// Confirm gone.
	rr = doWithToken(srv, "GET", "/api/datasets/iris/v1.0.0", "", aliceTok)
	if rr.Code != http.StatusNotFound {
		t.Errorf("after delete: want 404, got %d", rr.Code)
	}
}

func TestRegistry_DeleteDataset_NonOwner_Forbidden(t *testing.T) {
	// Bob tries to delete Alice's dataset without share → deny.
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	bobTok := mkTokenRS(t, tm, "bob", "user")

	_ = doWithToken(srv, "POST", "/api/datasets",
		`{"name":"iris","version":"v1","uri":"s3://d"}`, aliceTok)

	rr := doWithToken(srv, "DELETE", "/api/datasets/iris/v1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("non-owner delete: want 403, got %d %s", rr.Code, rr.Body.String())
	}

	// Record survives the forbidden attempt.
	rr = doWithToken(srv, "GET", "/api/datasets/iris/v1", "", aliceTok)
	if rr.Code != 200 {
		t.Errorf("record removed by failed delete: got %d", rr.Code)
	}
}

// ── Model ────────────────────────────────────────────────────

func TestRegistry_DeleteModel_Owner_204(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")

	rr := doWithToken(srv, "POST", "/api/models",
		`{"name":"mnist","version":"v1","uri":"s3://m","framework":"pytorch"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "DELETE", "/api/models/mnist/v1", "", aliceTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "GET", "/api/models/mnist/v1", "", aliceTok)
	if rr.Code != http.StatusNotFound {
		t.Errorf("after delete: want 404, got %d", rr.Code)
	}
}

func TestRegistry_DeleteModel_NonOwner_Forbidden(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	bobTok := mkTokenRS(t, tm, "bob", "user")

	_ = doWithToken(srv, "POST", "/api/models",
		`{"name":"mnist","version":"v1","uri":"s3://m","framework":"pytorch"}`, aliceTok)

	rr := doWithToken(srv, "DELETE", "/api/models/mnist/v1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("non-owner delete: want 403, got %d", rr.Code)
	}
}

// ── Admin can override ownership ─────────────────────────────

func TestRegistry_DeleteDataset_Admin_204(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	adminTok := mkTokenRS(t, tm, "root", "admin")

	_ = doWithToken(srv, "POST", "/api/datasets",
		`{"name":"ds","version":"v1","uri":"s3://d"}`, aliceTok)

	rr := doWithToken(srv, "DELETE", "/api/datasets/ds/v1", "", adminTok)
	if rr.Code != http.StatusNoContent {
		t.Errorf("admin delete: want 204, got %d %s", rr.Code, rr.Body.String())
	}
}

// ── Event emission via dataset + model delete ───────────────

func TestRegistry_DeleteDataset_PublishesEvent(t *testing.T) {
	srv, bus := newRegistryServerWithBus(t)
	sub := bus.Subscribe(events.TopicDatasetDeleted)
	defer sub.Cancel()

	rr := do(srv, "POST", "/api/datasets",
		`{"name":"iris","version":"v1","uri":"s3://d"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(srv, "DELETE", "/api/datasets/iris/v1", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}

	select {
	case ev := <-sub.C:
		if ev.Type != events.TopicDatasetDeleted {
			t.Errorf("event type: got %q, want %q", ev.Type, events.TopicDatasetDeleted)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("dataset.deleted event not published within 500ms")
	}
}

func TestRegistry_DeleteModel_PublishesEvent(t *testing.T) {
	srv, bus := newRegistryServerWithBus(t)
	sub := bus.Subscribe(events.TopicModelDeleted)
	defer sub.Cancel()

	rr := do(srv, "POST", "/api/models",
		`{"name":"mnist","version":"v1","uri":"s3://m","framework":"pytorch"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(srv, "DELETE", "/api/models/mnist/v1", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}

	select {
	case ev := <-sub.C:
		if ev.Type != events.TopicModelDeleted {
			t.Errorf("event type: got %q", ev.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("model.deleted event not published within 500ms")
	}
}

