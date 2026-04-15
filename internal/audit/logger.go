// internal/audit/logger.go
//
// Audit logging for security and operational events.
//
// Phase 4 requirements:
// ────────────────────
// Audit events for:
//   - Node register / revoke
//   - Job submit / state transitions
//   - Auth failures
//   - Rate limit hits
//   - Coordinator start / stop
//
// Storage:
// ───────
// Events are stored in BadgerDB with time-ordered keys:
//   audit:<timestamp_nanos>:<event_id>
//
// This allows efficient range scans for time-based queries and pagination.
//
// TTL:
// ───
// Audit events have a configurable TTL (default 90 days) to prevent
// unbounded growth. This can be disabled by setting TTL to 0.

package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Event types (matches design document Phase 4 requirements)
const (
	EventNodeRegister       = "node_register"
	EventNodeRevoke         = "node_revoke"
	EventJobSubmit          = "job_submit"
	EventJobStateTransition = "job_state_transition"
	EventAuthFailure        = "auth_failure"
	EventRateLimitHit       = "rate_limit_hit"
	EventCoordinatorStart   = "coordinator_start"
	EventCoordinatorStop    = "coordinator_stop"
	EventSecurityViolation  = "security_violation"
)

// Event represents a single audit log entry.
type Event struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Type      string                 `json:"type"`
	Actor     string                 `json:"actor"` // Node ID, user ID, or "system"
	Details   map[string]interface{} `json:"details"`
}

// Store is the interface for persisting audit events.
type Store interface {
	Put(ctx context.Context, key string, value []byte) error
	PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Scan(ctx context.Context, prefix string, limit int) ([][]byte, error)
}

// Logger writes audit events to persistent storage.
type Logger struct {
	store   Store
	ttl     time.Duration // TTL for audit events (0 = no expiry)
	seq     atomic.Int64  // monotonic counter; tiebreaker for same-nanosecond keys
}

// NewLogger creates an audit logger with the given store and TTL.
// A TTL of 0 means events never expire.
func NewLogger(store Store, ttl time.Duration) *Logger {
	return &Logger{
		store: store,
		ttl:   ttl,
	}
}

// Log records an audit event.
// Returns error if the event could not be persisted.
func (l *Logger) Log(ctx context.Context, eventType, actor string, details map[string]interface{}) error {
	event := Event{
		ID:        uuid.New().String(),
		Timestamp: time.Now(),
		Type:      eventType,
		Actor:     actor,
		Details:   details,
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}

	// Key format: "audit:<timestamp_nanos>:<seq>:<event_id>"
	// The monotonic sequence number breaks ties between events with the same
	// nanosecond timestamp, ensuring keys are unique even under high write rates.
	seq := l.seq.Add(1)
	key := fmt.Sprintf("audit:%019d:%016d:%s", event.Timestamp.UnixNano(), seq, event.ID)

	if l.ttl > 0 {
		return l.store.PutWithTTL(ctx, key, data, l.ttl)
	}
	return l.store.Put(ctx, key, data)
}

// LogNodeRegister logs a node registration event.
func (l *Logger) LogNodeRegister(ctx context.Context, nodeID, address string) error {
	return l.Log(ctx, EventNodeRegister, nodeID, map[string]interface{}{
		"node_id": nodeID,
		"address": address,
	})
}

// LogNodeRevoke logs a node certificate revocation event.
func (l *Logger) LogNodeRevoke(ctx context.Context, revokedBy, nodeID, reason string) error {
	return l.Log(ctx, EventNodeRevoke, revokedBy, map[string]interface{}{
		"node_id": nodeID,
		"reason":  reason,
	})
}

// LogJobSubmit logs a job submission event.
func (l *Logger) LogJobSubmit(ctx context.Context, actor, jobID, command string) error {
	return l.Log(ctx, EventJobSubmit, actor, map[string]interface{}{
		"job_id":  jobID,
		"command": command,
	})
}

// LogJobStateTransition logs a job state change.
func (l *Logger) LogJobStateTransition(ctx context.Context, jobID, fromState, toState string) error {
	return l.Log(ctx, EventJobStateTransition, "system", map[string]interface{}{
		"job_id":     jobID,
		"from_state": fromState,
		"to_state":   toState,
	})
}

// LogAuthFailure logs an authentication failure.
func (l *Logger) LogAuthFailure(ctx context.Context, reason, remoteAddr string) error {
	return l.Log(ctx, EventAuthFailure, "unknown", map[string]interface{}{
		"reason":      reason,
		"remote_addr": remoteAddr,
	})
}

// LogRateLimitHit logs a rate limit violation.
func (l *Logger) LogRateLimitHit(ctx context.Context, nodeID string, limit float64) error {
	return l.Log(ctx, EventRateLimitHit, nodeID, map[string]interface{}{
		"limit_rps": limit,
	})
}

// LogCoordinatorStart logs coordinator startup.
func (l *Logger) LogCoordinatorStart(ctx context.Context, version string) error {
	return l.Log(ctx, EventCoordinatorStart, "system", map[string]interface{}{
		"version": version,
	})
}

// LogCoordinatorStop logs coordinator shutdown.
func (l *Logger) LogCoordinatorStop(ctx context.Context, reason string) error {
	return l.Log(ctx, EventCoordinatorStop, "system", map[string]interface{}{
		"reason": reason,
	})
}

// LogSecurityViolation logs a security event such as a seccomp violation or
// OOM kill that is attributed to a specific job and node.
func (l *Logger) LogSecurityViolation(ctx context.Context, nodeID, jobID, violation string) error {
	return l.Log(ctx, EventSecurityViolation, nodeID, map[string]interface{}{
		"job_id":    jobID,
		"violation": violation,
	})
}

// LogServiceEvent records a feature-17 inference-service readiness
// transition. Edge-triggered: one audit row per ready ↔ unhealthy
// flip, not one per probe tick. The event type is either
// "service.ready" or "service.unhealthy" so queries can filter
// on transition direction without parsing details.
func (l *Logger) LogServiceEvent(
	ctx context.Context,
	nodeID, jobID string,
	ready bool,
	port uint32,
	healthPath string,
	consecutiveFailures uint32,
) error {
	eventType := "service.ready"
	if !ready {
		eventType = "service.unhealthy"
	}
	return l.Log(ctx, eventType, "node:"+nodeID, map[string]interface{}{
		"job_id":               jobID,
		"port":                 port,
		"health_path":          healthPath,
		"consecutive_failures": consecutiveFailures,
	})
}

// Query retrieves audit events matching the given criteria.
type Query struct {
	StartTime time.Time // Inclusive
	EndTime   time.Time // Exclusive
	Type      string    // Event type filter (empty = all types)
	Limit     int       // Max events to return (0 = no limit)
}

// QueryEvents retrieves audit events matching the query.
// Returns events in reverse chronological order (newest first).
func (l *Logger) QueryEvents(ctx context.Context, q Query) ([]Event, error) {
	// For now, we'll do a simple prefix scan and filter in memory.
	// A production implementation would use BadgerDB's iterator with
	// time-based key prefixes for efficient range queries.
	
	prefix := "audit:"
	values, err := l.store.Scan(ctx, prefix, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("scan audit events: %w", err)
	}

	events := make([]Event, 0, len(values))
	for _, data := range values {
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			continue // Skip malformed events
		}

		// Apply filters
		if !q.StartTime.IsZero() && event.Timestamp.Before(q.StartTime) {
			continue
		}
		if !q.EndTime.IsZero() && !event.Timestamp.Before(q.EndTime) {
			continue
		}
		if q.Type != "" && event.Type != q.Type {
			continue
		}

		events = append(events, event)
	}

	// Reverse to get newest first
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	// Apply limit after filtering
	if q.Limit > 0 && len(events) > q.Limit {
		events = events[:q.Limit]
	}

	return events, nil
}
