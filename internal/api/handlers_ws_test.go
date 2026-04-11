// internal/api/handlers_ws_test.go
//
// Real-WebSocket tests for the /ws/jobs/{id}/logs and /ws/metrics handlers.
// These drive the upgrade + write + close path via an httptest.Server and
// a gorilla/websocket client so the handlers run end-to-end.

package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/gorilla/websocket"
)

// wsURL rewrites an http:// base URL from httptest.Server into a ws:// URL.
func wsURL(base, path string) string {
	return "ws" + strings.TrimPrefix(base, "http") + path
}

// ── /ws/jobs/{id}/logs ───────────────────────────────────────────────────────

func TestWSJobLogStream_JobNotFound_Returns404(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Job doesn't exist — the handler writes 404 BEFORE upgrading, so the
	// Dial will fail with a bad-handshake error.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/jobs/missing/logs"), nil)
	if err == nil {
		t.Fatal("expected Dial to fail for missing job, got nil")
	}
	if resp != nil && resp.StatusCode != 404 {
		t.Errorf("want 404 for missing job, got %d", resp.StatusCode)
	}
}

func TestWSJobLogStream_JobExists_ReceivesNotImplementedFrame(t *testing.T) {
	js := newMockJobStore()
	js.jobs["log-job"] = &cpb.Job{ID: "log-job", Command: "echo"}

	srv := newServer(js, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/jobs/log-job/logs"), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Handler should send one JSON error frame then close with code 1001.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if !strings.Contains(string(data), "not yet implemented") {
		t.Errorf("want 'not yet implemented' in frame, got %q", data)
	}
}

// ── /ws/metrics ──────────────────────────────────────────────────────────────

func TestWSMetricsStream_ContextAlreadyCancelled_Returns408(t *testing.T) {
	mp := &mockMetricsProvider{metrics: &api.ClusterMetrics{}}
	srv := newServer(newMockJobStore(), nil, mp)

	// We cannot easily pre-cancel a request context from the client side,
	// but httptest handles the normal path. For the pre-check to fire we
	// need the request context to be done before the handler runs. Use
	// a synthetic request with an already-cancelled context.
	req := httptest.NewRequest("GET", "/ws/metrics", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // done before the handler even sees it
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	// Pre-cancelled context → 408 Request Timeout from the handler.
	if rr.Code != 408 {
		t.Errorf("want 408 for pre-cancelled context, got %d", rr.Code)
	}
}

func TestWSMetricsStream_Connects_ThenCloses(t *testing.T) {
	mp := &mockMetricsProvider{metrics: &api.ClusterMetrics{}}
	srv := newServer(newMockJobStore(), nil, mp)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/metrics"), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// Close immediately — the handler's for-loop will exit via ctx.Done().
	conn.Close()
}
