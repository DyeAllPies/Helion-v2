// internal/api/handlers_ws.go
//
// WebSocket handlers:
//   GET /ws/jobs/{id}/logs  — real-time job log streaming (not yet implemented)
//   GET /ws/metrics         — server-push cluster metrics stream
//
// AUDIT 2026-04-12-01/H2 (fixed): WebSocket endpoints use first-message auth.
// The connection is upgraded without authentication. The client must send
// {"type":"auth","token":"<jwt>"} as the first frame. The server validates
// the token and replies with {"type":"auth_ok"} on success or closes with
// 4001 on failure. This keeps JWTs out of URLs, server logs, and browser
// history.

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// wsAuthMsg is the first-message auth frame from the client.
type wsAuthMsg struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// wsAuthenticateConn reads the first frame from conn, validates the JWT,
// and sends back {"type":"auth_ok"} or closes with 4001.
// ctx is the HTTP request context — used for token validation and audit logging.
// Returns nil on success, non-nil on auth failure (connection already closed).
func (s *Server) wsAuthenticateConn(ctx context.Context, conn *websocket.Conn) error {
	if s.disableAuth {
		return nil
	}
	if s.tokenManager == nil {
		slog.Error("ws auth: tokenManager is nil and DisableAuth not set")
		_ = conn.WriteJSON(map[string]string{"type": "auth_error", "message": "authentication not configured"})
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4001, "auth not configured"))
		return http.ErrAbortHandler
	}

	// Read first frame (5 s deadline for the auth handshake).
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Time{}) // clear deadline

	var msg wsAuthMsg
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "auth" || msg.Token == "" {
		if s.audit != nil {
			_ = s.audit.LogAuthFailure(ctx, "invalid ws auth frame", "")
		}
		_ = conn.WriteJSON(map[string]string{"type": "auth_error", "message": "invalid auth frame"})
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4001, "invalid auth frame"))
		return http.ErrAbortHandler
	}

	if _, err := s.tokenManager.ValidateToken(ctx, msg.Token); err != nil {
		if s.audit != nil {
			_ = s.audit.LogAuthFailure(ctx, err.Error(), "")
		}
		slog.Error("ws token validation failed", slog.Any("err", err))
		_ = conn.WriteJSON(map[string]string{"type": "auth_error", "message": "authentication failed"})
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4001, "authentication failed"))
		return http.ErrAbortHandler
	}

	_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
	return nil
}

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

	// First-message auth.
	if err := s.wsAuthenticateConn(r.Context(), conn); err != nil {
		return
	}

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

// wsSubscribeMsg is a subscription request from the client on /ws/events.
type wsSubscribeMsg struct {
	Subscribe []string `json:"subscribe"` // topic patterns (e.g. "job.*", "node.stale")
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.eventBus == nil {
		http.Error(w, "event system not enabled", http.StatusNotImplemented)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if err := s.wsAuthenticateConn(ctx, conn); err != nil {
		return
	}

	// Read the subscription message from the client.
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var subMsg wsSubscribeMsg
	if err := json.Unmarshal(raw, &subMsg); err != nil || len(subMsg.Subscribe) == 0 {
		_ = conn.WriteJSON(map[string]string{"type": "error", "message": "send {\"subscribe\":[\"topic.*\"]}"})
		return
	}

	// Subscribe to the requested topics.
	sub := s.eventBus.Subscribe(subMsg.Subscribe...)
	defer sub.Cancel()

	// Send confirmation.
	_ = conn.WriteJSON(map[string]string{"type": "subscribed"})

	// Stream events to the client until disconnect or context cancel.
	for {
		select {
		case event, ok := <-sub.C:
			if !ok {
				return
			}
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
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

	// First-message auth.
	if err := s.wsAuthenticateConn(ctx, conn); err != nil {
		return
	}

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
