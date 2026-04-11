// internal/api/handlers_ws.go
//
// WebSocket handlers:
//   GET /ws/jobs/{id}/logs  — real-time job log streaming (not yet implemented)
//   GET /ws/metrics         — server-push cluster metrics stream

package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func (s *Server) handleJobLogStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		jobID = strings.TrimPrefix(r.URL.Path, "/ws/jobs/")
		jobID = strings.TrimSuffix(jobID, "/logs")
	}

	// Verify the job exists before upgrading to WebSocket.
	if _, err := s.jobs.Get(jobID); err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// AUDIT C2 (fixed): real-time log streaming is not yet implemented (requires a
	// gRPC server-streaming back-channel from node agents). Previously this handler
	// contained a 30-second placeholder sleep that held the connection open
	// indefinitely. Now it returns a clear error frame and closes cleanly with
	// WebSocket close code 1001 (Going Away).
	_ = conn.WriteJSON(map[string]string{
		"type":    "error",
		"message": "log streaming is not yet implemented",
	})
	// WS close code 1001 = "Going Away" — server is terminating the connection.
	_ = conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseGoingAway, "not implemented"),
	)
}

func (s *Server) handleMetricsStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// AUDIT M3 (fixed): check context cancellation before upgrading to WebSocket
	// so a cancelled request never leaves an orphaned connection open.
	if err := ctx.Err(); err != nil {
		http.Error(w, "request context already cancelled", http.StatusRequestTimeout)
		return
	}

	// Upgrade to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			metrics, err := s.metrics.GetClusterMetrics(ctx)
			if err != nil {
				return
			}
			if err := conn.WriteJSON(metrics); err != nil {
				return // Client disconnected
			}

		case <-ctx.Done():
			return
		}
	}
}
