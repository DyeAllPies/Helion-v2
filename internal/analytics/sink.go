// internal/analytics/sink.go
//
// Sink subscribes to the coordinator's in-memory event bus and writes
// events to PostgreSQL in batches.  It is the bridge between the
// operational event stream and the analytical store.
//
// Design
// ──────
//   - Subscribes to "*" (all topics) on the event bus.
//   - Buffers events in memory up to BatchSize or FlushInterval, whichever
//     comes first.
//   - Each flush inserts the raw events and upserts the summary tables in
//     a single transaction.
//   - If PostgreSQL is unreachable the buffer grows up to BufferLimit, then
//     drops the oldest events. Analytics is best-effort and never blocks
//     the coordinator's hot path.
//   - Start() launches the background goroutine; Stop() cancels and drains.

package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/jackc/pgx/v5"
)

// SinkConfig holds tunable parameters for the analytics sink.
type SinkConfig struct {
	BatchSize     int           // events per flush (default 100)
	FlushInterval time.Duration // max time between flushes (default 500ms)
	BufferLimit   int           // max in-memory buffer before dropping (default 10000)

	// Feature 28 — PII hashing. When PIIMode == "hash_actor" the
	// sink writes sha256(PIISalt || raw_actor) into the `actor`
	// columns of feature-28 tables (submission_history,
	// registry_mutations, auth_events). An empty PIIMode keeps
	// raw values — the default.
	//
	// PIISalt should be set when PIIMode is hash_actor; an empty
	// salt produces unsalted hashes (still bound-same-actor for
	// trend graphs, but trivially brute-forceable against a short
	// subject list). The coordinator logs a WARN on empty salt +
	// hash_actor so the operator sees the tradeoff at boot.
	PIIMode string
	PIISalt string
}

func (c SinkConfig) withDefaults() SinkConfig {
	out := c
	if out.BatchSize <= 0 {
		out.BatchSize = 100
	}
	if out.FlushInterval <= 0 {
		out.FlushInterval = 500 * time.Millisecond
	}
	if out.BufferLimit <= 0 {
		out.BufferLimit = 10_000
	}
	return out
}

// dbConn is the subset of *pgx.Conn that the Sink needs.  Extracted as an
// interface so the flush path can be tested with a mock (no PostgreSQL).
type dbConn interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Sink consumes events from an event bus and persists them to PostgreSQL.
type Sink struct {
	conn   dbConn
	bus    *events.Bus
	sub    *events.Subscription
	cfg    SinkConfig
	log    *slog.Logger

	mu     sync.Mutex
	buf    []events.Event

	cancel context.CancelFunc
	done   chan struct{}
}

// NewSink creates a new analytics sink.  Call Start to begin consuming.
func NewSink(conn dbConn, bus *events.Bus, cfg SinkConfig, log *slog.Logger) *Sink {
	if log == nil {
		log = slog.Default()
	}
	cfg = cfg.withDefaults()
	return &Sink{
		conn: conn,
		bus:  bus,
		cfg:  cfg,
		log:  log,
		buf:  make([]events.Event, 0, cfg.BatchSize),
		done: make(chan struct{}),
	}
}

// Start subscribes to all events and begins the background flush loop.
func (s *Sink) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.sub = s.bus.Subscribe("*")

	go s.loop(ctx)
	s.log.Info("analytics sink started",
		slog.Int("batch_size", s.cfg.BatchSize),
		slog.Duration("flush_interval", s.cfg.FlushInterval),
		slog.Int("buffer_limit", s.cfg.BufferLimit))
}

// Stop cancels the background loop, flushes remaining events, and
// unsubscribes from the event bus.
func (s *Sink) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done

	if s.sub != nil {
		s.sub.Cancel()
	}

	// Final flush of any remaining buffered events.
	s.mu.Lock()
	remaining := make([]events.Event, len(s.buf))
	copy(remaining, s.buf)
	s.buf = s.buf[:0]
	s.mu.Unlock()

	if len(remaining) > 0 && s.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.flush(ctx, remaining); err != nil {
			s.log.Warn("analytics sink: final flush failed",
				slog.Int("dropped", len(remaining)),
				slog.Any("err", err))
		}
	}
}

// loop is the main goroutine that drains the subscription channel and
// periodically flushes accumulated events to PostgreSQL.
func (s *Sink) loop(ctx context.Context) {
	defer close(s.done)

	ticker := time.NewTicker(s.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case evt, ok := <-s.sub.C:
			if !ok {
				return
			}
			s.append(evt)

			// Flush immediately if batch is full.
			s.mu.Lock()
			shouldFlush := len(s.buf) >= s.cfg.BatchSize
			var batch []events.Event
			if shouldFlush {
				batch = make([]events.Event, len(s.buf))
				copy(batch, s.buf)
				s.buf = s.buf[:0]
			}
			s.mu.Unlock()

			if shouldFlush {
				if err := s.flush(ctx, batch); err != nil {
					s.log.Warn("analytics sink: batch flush failed",
						slog.Int("events", len(batch)),
						slog.Any("err", err))
					// Re-buffer the failed batch (up to limit).
					s.rebuffer(batch)
				}
			}

		case <-ticker.C:
			s.mu.Lock()
			if len(s.buf) == 0 {
				s.mu.Unlock()
				continue
			}
			batch := make([]events.Event, len(s.buf))
			copy(batch, s.buf)
			s.buf = s.buf[:0]
			s.mu.Unlock()

			if err := s.flush(ctx, batch); err != nil {
				s.log.Warn("analytics sink: timed flush failed",
					slog.Int("events", len(batch)),
					slog.Any("err", err))
				s.rebuffer(batch)
			}
		}
	}
}

// append adds an event to the buffer, dropping the oldest if at limit.
func (s *Sink) append(evt events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.buf) >= s.cfg.BufferLimit {
		// Drop oldest event.
		copy(s.buf, s.buf[1:])
		s.buf[len(s.buf)-1] = evt
		s.log.Warn("analytics sink: buffer full, dropping oldest event")
		return
	}
	s.buf = append(s.buf, evt)
}

// rebuffer puts unflushed events back into the buffer (best-effort).
func (s *Sink) rebuffer(batch []events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	space := s.cfg.BufferLimit - len(s.buf)
	if space <= 0 {
		return
	}
	if len(batch) > space {
		batch = batch[:space]
	}
	// Prepend the failed batch so they retry first.
	s.buf = append(batch, s.buf...)
}

// flush writes a batch of events to PostgreSQL in a single transaction.
// It inserts into the events table and upserts the summary tables.
func (s *Sink) flush(ctx context.Context, batch []events.Event) error {
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.insertEvents(ctx, tx, batch); err != nil {
		return err
	}
	if err := s.upsertSummaries(ctx, tx, batch); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// insertEvents batch-inserts rows into the events table.
func (s *Sink) insertEvents(ctx context.Context, tx pgx.Tx, batch []events.Event) error {
	if len(batch) == 0 {
		return nil
	}

	// Build a single multi-row INSERT.
	var sb strings.Builder
	sb.WriteString("INSERT INTO events (id, event_type, timestamp, data, job_id, node_id, workflow_id) VALUES ")

	args := make([]any, 0, len(batch)*7)
	for i, evt := range batch {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 7
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7)

		dataJSON, err := json.Marshal(evt.Data)
		if err != nil {
			dataJSON = []byte("{}")
		}

		args = append(args,
			evt.ID,
			evt.Type,
			evt.Timestamp,
			dataJSON,
			extractString(evt.Data, "job_id"),
			extractString(evt.Data, "node_id"),
			extractString(evt.Data, "workflow_id"),
		)
	}
	sb.WriteString(" ON CONFLICT (id) DO NOTHING")

	_, err := tx.Exec(ctx, sb.String(), args...)
	if err != nil {
		return fmt.Errorf("insert events: %w", err)
	}
	return nil
}

// upsertSummaries processes each event and updates the appropriate summary table.
func (s *Sink) upsertSummaries(ctx context.Context, tx pgx.Tx, batch []events.Event) error {
	for _, evt := range batch {
		var err error
		switch evt.Type {
		case "job.submitted":
			err = s.upsertJobSubmitted(ctx, tx, evt)
		case "job.transition":
			err = s.upsertJobTransition(ctx, tx, evt)
		case "job.completed":
			err = s.upsertJobCompleted(ctx, tx, evt)
		case "job.failed":
			err = s.upsertJobFailed(ctx, tx, evt)
		case "job.retrying":
			err = s.upsertJobRetrying(ctx, tx, evt)
		case "node.registered":
			err = s.upsertNodeRegistered(ctx, tx, evt)
		case "node.stale":
			err = s.upsertNodeStale(ctx, tx, evt)
		case "node.revoked":
			err = s.upsertNodeRevoked(ctx, tx, evt)

		// ── Feature 28 — previously-dropped events now persisted ──
		case events.TopicWorkflowCompleted:
			err = s.upsertWorkflowOutcome(ctx, tx, evt, "completed")
		case events.TopicWorkflowFailed:
			err = s.upsertWorkflowOutcome(ctx, tx, evt, "failed")
		case events.TopicJobUnschedulable:
			err = s.upsertUnschedulable(ctx, tx, evt)
		case events.TopicMLResolveFailed:
			err = s.upsertMLResolveFailed(ctx, tx, evt)
		case events.TopicDatasetRegistered:
			err = s.upsertRegistryMutation(ctx, tx, evt, "dataset", "registered")
		case events.TopicDatasetDeleted:
			err = s.upsertRegistryMutation(ctx, tx, evt, "dataset", "deleted")
		case events.TopicModelRegistered:
			err = s.upsertRegistryMutation(ctx, tx, evt, "model", "registered")
		case events.TopicModelDeleted:
			err = s.upsertRegistryMutation(ctx, tx, evt, "model", "deleted")

		// ── Feature 28 — new event families ───────────────────────
		case events.TopicSubmissionRecorded:
			err = s.upsertSubmissionHistory(ctx, tx, evt)
		case events.TopicAuthOK:
			err = s.upsertAuthEvent(ctx, tx, evt, "login")
		case events.TopicAuthFail:
			err = s.upsertAuthEvent(ctx, tx, evt, "auth_fail")
		case events.TopicAuthRateLimit:
			err = s.upsertAuthEvent(ctx, tx, evt, "rate_limit")
		case events.TopicAuthTokenMint:
			err = s.upsertAuthEvent(ctx, tx, evt, "token_mint")
		case events.TopicArtifactUploaded:
			err = s.upsertArtifactTransfer(ctx, tx, evt, "upload")
		case events.TopicArtifactDownloaded:
			err = s.upsertArtifactTransfer(ctx, tx, evt, "download")
		case events.TopicServiceProbeTransition:
			err = s.upsertServiceProbeEvent(ctx, tx, evt)
		case events.TopicJobLog:
			err = s.upsertJobLog(ctx, tx, evt)
		}
		if err != nil {
			return fmt.Errorf("upsert summary for %s: %w", evt.Type, err)
		}
	}
	return nil
}

// ── Job summary upserts ──────────────────────────────────────────────────

func (s *Sink) upsertJobSubmitted(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	if jobID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO job_summary (job_id, command, priority, submitted_at, final_status)
		VALUES ($1, $2, $3, $4, 'pending')
		ON CONFLICT (job_id) DO UPDATE SET
			command      = COALESCE(EXCLUDED.command, job_summary.command),
			priority     = COALESCE(EXCLUDED.priority, job_summary.priority),
			submitted_at = COALESCE(EXCLUDED.submitted_at, job_summary.submitted_at)
	`,
		jobID,
		extractString(evt.Data, "command"),
		extractInt(evt.Data, "priority"),
		evt.Timestamp,
	)
	return err
}

func (s *Sink) upsertJobTransition(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	if jobID == "" {
		return nil
	}
	toStatus := extractString(evt.Data, "to_status")
	nodeID := extractString(evt.Data, "node_id")

	// Set started_at on first transition to "running".
	if toStatus == "running" {
		_, err := tx.Exec(ctx, `
			INSERT INTO job_summary (job_id, final_status, node_id, started_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (job_id) DO UPDATE SET
				final_status = EXCLUDED.final_status,
				node_id      = COALESCE(EXCLUDED.node_id, job_summary.node_id),
				started_at   = COALESCE(job_summary.started_at, EXCLUDED.started_at)
		`, jobID, toStatus, nilIfEmpty(nodeID), evt.Timestamp)
		return err
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO job_summary (job_id, final_status, node_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (job_id) DO UPDATE SET
			final_status = EXCLUDED.final_status,
			node_id      = COALESCE(EXCLUDED.node_id, job_summary.node_id)
	`, jobID, toStatus, nilIfEmpty(nodeID))
	return err
}

func (s *Sink) upsertJobCompleted(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	if jobID == "" {
		return nil
	}
	nodeID := extractString(evt.Data, "node_id")

	_, err := tx.Exec(ctx, `
		INSERT INTO job_summary (job_id, final_status, node_id, completed_at, duration_ms)
		VALUES ($1, 'completed', $2, $3, $4)
		ON CONFLICT (job_id) DO UPDATE SET
			final_status = 'completed',
			node_id      = COALESCE(EXCLUDED.node_id, job_summary.node_id),
			completed_at = EXCLUDED.completed_at,
			duration_ms  = EXCLUDED.duration_ms
	`,
		jobID,
		nilIfEmpty(nodeID),
		evt.Timestamp,
		extractInt64(evt.Data, "duration_ms"),
	)
	if err != nil {
		return err
	}

	// Increment the node's completed-job counter.
	if nodeID != "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO node_summary (node_id, first_seen, last_seen, jobs_completed)
			VALUES ($1, $2, $2, 1)
			ON CONFLICT (node_id) DO UPDATE SET
				last_seen      = EXCLUDED.last_seen,
				jobs_completed = node_summary.jobs_completed + 1
		`, nodeID, evt.Timestamp)
	}
	return err
}

func (s *Sink) upsertJobFailed(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	if jobID == "" {
		return nil
	}
	nodeID := extractString(evt.Data, "node_id")

	_, err := tx.Exec(ctx, `
		INSERT INTO job_summary (job_id, final_status, completed_at, last_error, last_exit_code, attempts)
		VALUES ($1, 'failed', $2, $3, $4, $5)
		ON CONFLICT (job_id) DO UPDATE SET
			final_status   = 'failed',
			completed_at   = EXCLUDED.completed_at,
			last_error     = EXCLUDED.last_error,
			last_exit_code = EXCLUDED.last_exit_code,
			attempts       = EXCLUDED.attempts
	`,
		jobID,
		evt.Timestamp,
		extractString(evt.Data, "error"),
		extractInt(evt.Data, "exit_code"),
		extractInt(evt.Data, "attempt"),
	)
	if err != nil {
		return err
	}

	// Increment the node's failed-job counter (if node_id is present).
	if nodeID != "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO node_summary (node_id, first_seen, last_seen, jobs_failed)
			VALUES ($1, $2, $2, 1)
			ON CONFLICT (node_id) DO UPDATE SET
				last_seen   = EXCLUDED.last_seen,
				jobs_failed = node_summary.jobs_failed + 1
		`, nodeID, evt.Timestamp)
	}
	return err
}

func (s *Sink) upsertJobRetrying(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	if jobID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO job_summary (job_id, final_status, attempts)
		VALUES ($1, 'retrying', $2)
		ON CONFLICT (job_id) DO UPDATE SET
			final_status = 'retrying',
			attempts     = EXCLUDED.attempts
	`,
		jobID,
		extractInt(evt.Data, "attempt"),
	)
	return err
}

// ── Node summary upserts ─────────────────────────────────────────────────

func (s *Sink) upsertNodeRegistered(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	nodeID := extractString(evt.Data, "node_id")
	if nodeID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO node_summary (node_id, address, first_seen, last_seen)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (node_id) DO UPDATE SET
			address   = COALESCE(EXCLUDED.address, node_summary.address),
			last_seen = EXCLUDED.last_seen
	`,
		nodeID,
		extractString(evt.Data, "address"),
		evt.Timestamp,
	)
	return err
}

func (s *Sink) upsertNodeStale(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	nodeID := extractString(evt.Data, "node_id")
	if nodeID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO node_summary (node_id, first_seen, last_seen, times_stale)
		VALUES ($1, $2, $2, 1)
		ON CONFLICT (node_id) DO UPDATE SET
			last_seen   = EXCLUDED.last_seen,
			times_stale = node_summary.times_stale + 1
	`,
		nodeID,
		evt.Timestamp,
	)
	return err
}

func (s *Sink) upsertNodeRevoked(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	nodeID := extractString(evt.Data, "node_id")
	if nodeID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO node_summary (node_id, first_seen, last_seen, times_revoked)
		VALUES ($1, $2, $2, 1)
		ON CONFLICT (node_id) DO UPDATE SET
			last_seen     = EXCLUDED.last_seen,
			times_revoked = node_summary.times_revoked + 1
	`,
		nodeID,
		evt.Timestamp,
	)
	return err
}

// ── Helpers ──────────────────────────────────────────────────────────────

// extractString pulls a string value from the event's Data map.
func extractString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// extractInt pulls an integer value from the event's Data map.
// Handles both int and float64 (JSON numbers are decoded as float64).
func extractInt(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	v, ok := data[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	case uint32:
		return int(n)
	default:
		return 0
	}
}

// extractInt64 pulls an int64 value from the event's Data map.
func extractInt64(data map[string]any, key string) int64 {
	if data == nil {
		return 0
	}
	v, ok := data[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case uint32:
		return int64(n)
	default:
		return 0
	}
}

// nilIfEmpty returns nil if s is empty, otherwise a pointer-like value
// that pgx will encode as a non-NULL text parameter.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// extractBool pulls a bool value from the event's Data map. Tolerates
// either a native bool or JSON-round-tripped forms ("true"/"false"
// strings, 0/1 ints); anything else is false.
func extractBool(data map[string]any, key string) bool {
	if data == nil {
		return false
	}
	v, ok := data[key]
	if !ok {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	case int:
		return b != 0
	case float64:
		return b != 0
	default:
		return false
	}
}
