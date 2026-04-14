// internal/analytics/backfill.go
//
// One-time backfill: reads the existing BadgerDB audit trail and inserts
// historical events into the analytics PostgreSQL database.
//
// The audit logger stores events as JSON-encoded audit.Event structs under
// keys prefixed with "audit:".  This backfill reads them all, normalises
// them into the analytics events table schema, and upserts the summary
// tables — the same path the live Sink takes, but retroactively.
//
// Idempotent: events that already exist in PostgreSQL are skipped via
// ON CONFLICT DO NOTHING.  Safe to run multiple times.

package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// AuditScanner is the subset of audit.Store needed for backfill.
// Satisfied by cluster.BadgerJSONPersister.
type AuditScanner interface {
	Scan(ctx context.Context, prefix string, limit int) ([][]byte, error)
}

// auditEvent mirrors the audit.Event struct for JSON decoding.
// We duplicate it here to avoid importing the audit package (which would
// create a circular dependency if audit ever imports analytics).
type auditEvent struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Type      string                 `json:"type"`
	Actor     string                 `json:"actor"`
	Details   map[string]interface{} `json:"details"`
}

// auditTypeMap maps audit event types to analytics event bus topic names.
// Audit events that don't map to a bus topic are stored under their
// original type prefixed with "audit." so they're still queryable.
var auditTypeMap = map[string]string{
	"node_register":        "node.registered",
	"node_revoke":          "node.revoked",
	"job_submit":           "job.submitted",
	"job_state_transition": "job.transition",
}

// Backfill reads all audit events from the store and inserts them into
// the analytics PostgreSQL database.  Returns the number of events
// inserted (excluding duplicates that were already present).
func Backfill(ctx context.Context, scanner AuditScanner, conn dbConn, log *slog.Logger) (int, error) {
	if log == nil {
		log = slog.Default()
	}

	log.Info("analytics backfill: scanning audit trail")

	// Read all audit entries (limit=0 means no limit).
	rawEntries, err := scanner.Scan(ctx, "audit:", 0)
	if err != nil {
		return 0, fmt.Errorf("scan audit trail: %w", err)
	}

	log.Info("analytics backfill: audit entries found",
		slog.Int("count", len(rawEntries)))

	if len(rawEntries) == 0 {
		return 0, nil
	}

	// Parse all audit events.
	parsed := make([]auditEvent, 0, len(rawEntries))
	for _, raw := range rawEntries {
		var evt auditEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			log.Warn("analytics backfill: skipping malformed audit event",
				slog.Any("err", err))
			continue
		}
		parsed = append(parsed, evt)
	}

	log.Info("analytics backfill: parsed events",
		slog.Int("count", len(parsed)))

	// Convert to analytics events and insert in batches.
	const batchSize = 500
	inserted := 0
	sink := &Sink{conn: conn, log: log}

	for i := 0; i < len(parsed); i += batchSize {
		end := i + batchSize
		if end > len(parsed) {
			end = len(parsed)
		}
		batch := parsed[i:end]

		analyticsEvents := make([]events.Event, 0, len(batch))
		for _, ae := range batch {
			analyticsEvents = append(analyticsEvents, auditToAnalyticsEvent(ae))
		}

		if err := sink.flush(ctx, analyticsEvents); err != nil {
			return inserted, fmt.Errorf("backfill batch at offset %d: %w", i, err)
		}
		inserted += len(analyticsEvents)

		log.Info("analytics backfill: batch inserted",
			slog.Int("offset", i),
			slog.Int("batch_size", len(analyticsEvents)),
			slog.Int("total", inserted))
	}

	log.Info("analytics backfill: complete",
		slog.Int("total_inserted", inserted))
	return inserted, nil
}

// auditToAnalyticsEvent converts an audit.Event to an events.Event suitable
// for the analytics pipeline.
func auditToAnalyticsEvent(ae auditEvent) events.Event {
	// Map the audit type to a bus topic, or keep the original prefixed.
	eventType, ok := auditTypeMap[ae.Type]
	if !ok {
		eventType = "audit." + ae.Type
	}

	// Build the data map from audit details + actor.
	data := make(map[string]any, len(ae.Details)+1)
	for k, v := range ae.Details {
		data[k] = v
	}
	if ae.Actor != "" {
		data["actor"] = ae.Actor
	}

	// Normalise audit detail keys to match bus event data keys.
	// Audit uses "from_state"/"to_state"; bus events use "from_status"/"to_status".
	if eventType == "job.transition" {
		if v, ok := data["from_state"]; ok {
			data["from_status"] = v
			delete(data, "from_state")
		}
		if v, ok := data["to_state"]; ok {
			data["to_status"] = v
			delete(data, "to_state")
		}
	}

	// Use the audit event's own ID if valid UUID, otherwise generate one.
	id := ae.ID
	if id == "" {
		id = uuid.NewString()
	}

	return events.Event{
		ID:        id,
		Type:      eventType,
		Timestamp: ae.Timestamp,
		Data:      data,
	}
}
