// internal/analytics/sink_unified.go
//
// Feature 28 — per-event-family upsert functions for the unified
// analytics sink. Split out of sink.go to keep the core dispatch
// file focused on the subscribe/buffer/flush loop; every new
// upsert here follows the same shape as the original
// upsertJobSubmitted / upsertNodeRegistered functions.
//
// Each upsert:
//
//   1. Extracts the fields it needs from evt.Data using the
//      defensive extract* helpers (nil-safe, typed-safe).
//   2. Returns early (nil) when a required key is missing — the
//      raw event still landed in the `events` table, so we don't
//      lose the row; we just don't populate the denormalised
//      summary table. A later debug session can still query it
//      from the raw table.
//   3. Uses the same `tx pgx.Tx` the caller's transaction carries
//      — every upsert is part of the same batch-flush transaction.
//      A failed upsert rolls back the whole batch and the Sink
//      retries on the next flush.
//   4. Applies PII hashing at the point of write: any column that
//      stores an operator identity (actor, operator_cn, remote_ip)
//      goes through hashActorIfEnabled / maskIPIfEnabled so a
//      hash_actor-mode dump never exposes raw JWT subjects.

package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"

	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/jackc/pgx/v5"
)

// ── PII-mode constants ──────────────────────────────────────────────────────

// PIIModeHashActor is the canonical string for the hash-actor PII
// mode. Matched verbatim against SinkConfig.PIIMode; anything else
// leaves raw values in place.
const PIIModeHashActor = "hash_actor"

// hashActorIfEnabled returns the sha256-hex of (salt||actor) when
// the sink's PIIMode is hash_actor. Empty actor returns empty (no
// fake hash synthesised for "anonymous" — the empty column is
// meaningful and the dashboard filters on it).
//
// The SinkConfig value carries the policy; all call sites read it
// here in one place so a future "encrypt" mode lands without
// churning every upsert.
func (s *Sink) hashActorIfEnabled(raw string) string {
	if raw == "" || s.cfg.PIIMode != PIIModeHashActor {
		return raw
	}
	h := sha256.New()
	h.Write([]byte(s.cfg.PIISalt))
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

// ── workflow outcomes ──────────────────────────────────────────────────────

// upsertWorkflowOutcome writes one row per workflow.completed /
// workflow.failed event into the feature-40 workflow_outcomes
// denormalised table (see migration 006).
//
// The `events` table already captures the raw event in the
// earlier insertEvents pass; this upsert adds the rollup row the
// dashboard's ML runs panel queries. Contract:
//
//   - outcome ∈ {"completed", "failed"} — the sink's dispatch
//     switch picks which of the two topic branches fires.
//   - primary key = workflow_id. Re-submitting a workflow with
//     the same ID UPDATE-replaces the prior row so the rollup
//     reflects the latest run. The events table keeps the full
//     history so forensic reviewers can still reconstruct the
//     prior runs.
//   - feature-40 constructors stamp job_count / success_count /
//     failed_count / owner_principal on the event payload; if a
//     legacy caller used the short WorkflowCompleted /
//     WorkflowFailed constructors, the counts default to 0 and
//     we still write the row (better a partial row than a
//     missing one).
//
// Safety properties:
//
//   1. Does not block the flush on missing payload fields. Every
//      extractor defaults to a zero/empty value rather than
//      erroring, so a malformed event writes a degraded row
//      instead of failing the entire tx batch.
//
//   2. Tags are inserted as JSONB so the dashboard's filter by
//      `tags->>'task' = 'image-classification'` works without
//      a client-side parse pass.
//
//   3. JSONB marshalling errors are swallowed and replaced with
//      '{}'::JSONB — a corrupt tag map should never fail the
//      entire analytics batch.
func (s *Sink) upsertWorkflowOutcome(ctx context.Context, tx pgx.Tx, evt events.Event, outcome string) error {
	workflowID := extractString(evt.Data, "workflow_id")
	if workflowID == "" {
		// Defensive — the constructor always sets it, but if a
		// buggy publisher sends a blank workflow_id we'd otherwise
		// UPSERT into a phantom primary key that masks real rows.
		return nil
	}
	jobCount := extractInt(evt.Data, "job_count")
	successCount := extractInt(evt.Data, "success_count")
	failedCount := extractInt(evt.Data, "failed_count")
	ownerPrincipal := extractString(evt.Data, "owner_principal")
	failedJob := extractString(evt.Data, "failed_job")

	// Tags marshal: accept map[string]string OR map[string]any
	// (since the event bus JSON round-trip can rehydrate strings
	// as `any`). Empty / malformed → "{}" so the JSONB column
	// stays valid.
	tagsJSON := []byte(`{}`)
	if rawTags, ok := evt.Data["tags"]; ok && rawTags != nil {
		if b, err := json.Marshal(rawTags); err == nil {
			tagsJSON = b
		}
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO workflow_outcomes (
			workflow_id, status, completed_at,
			job_count, success_count, failed_count,
			failed_job, owner_principal, tags
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (workflow_id) DO UPDATE SET
			status          = EXCLUDED.status,
			completed_at    = EXCLUDED.completed_at,
			job_count       = EXCLUDED.job_count,
			success_count   = EXCLUDED.success_count,
			failed_count    = EXCLUDED.failed_count,
			failed_job      = EXCLUDED.failed_job,
			owner_principal = EXCLUDED.owner_principal,
			tags            = EXCLUDED.tags
	`,
		workflowID, outcome, evt.Timestamp,
		jobCount, successCount, failedCount,
		nilIfEmpty(failedJob), nilIfEmpty(ownerPrincipal), tagsJSON,
	)
	return err
}

// upsertMLResolveFailed mirrors upsertWorkflowOutcome: the raw
// event is enough for the Pipelines view today. A future
// ml_resolve_failed_summary rollup would plug in here.
func (s *Sink) upsertMLResolveFailed(_ context.Context, _ pgx.Tx, _ events.Event) error {
	return nil
}

// ── unschedulable_events ───────────────────────────────────────────────────

func (s *Sink) upsertUnschedulable(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	if jobID == "" {
		return nil
	}
	reason := extractString(evt.Data, "reason")
	// selector may arrive under either `selector` or the publisher-
	// canonical `unsatisfied_selector` key. Marshal whichever we find
	// into JSONB-compatible bytes.
	selectorBytes := []byte("{}")
	for _, key := range []string{"unsatisfied_selector", "selector"} {
		if raw, ok := evt.Data[key]; ok && raw != nil {
			if b, err := json.Marshal(raw); err == nil {
				selectorBytes = b
				break
			}
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO unschedulable_events (occurred_at, job_id, selector, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (job_id, occurred_at) DO NOTHING
	`, evt.Timestamp, jobID, selectorBytes, reason)
	return err
}

// ── registry_mutations ─────────────────────────────────────────────────────

func (s *Sink) upsertRegistryMutation(ctx context.Context, tx pgx.Tx, evt events.Event, kind, action string) error {
	name := extractString(evt.Data, "name")
	version := extractString(evt.Data, "version")
	if name == "" || version == "" {
		return nil
	}
	uri := extractString(evt.Data, "uri")
	actor := s.hashActorIfEnabled(extractString(evt.Data, "actor"))
	sizeBytes := extractInt64(evt.Data, "size_bytes")

	_, err := tx.Exec(ctx, `
		INSERT INTO registry_mutations (occurred_at, kind, action, name, version, uri, actor, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (kind, name, version, action, occurred_at) DO NOTHING
	`, evt.Timestamp, kind, action, name, version,
		nilIfEmpty(uri), nilIfEmpty(actor), nullIfZero64(sizeBytes))
	return err
}

// ── submission_history ─────────────────────────────────────────────────────

func (s *Sink) upsertSubmissionHistory(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	resourceID := extractString(evt.Data, "resource_id")
	kind := extractString(evt.Data, "kind")
	if resourceID == "" || kind == "" {
		return nil
	}
	actor := s.hashActorIfEnabled(extractString(evt.Data, "actor"))
	operatorCN := s.hashActorIfEnabled(extractString(evt.Data, "operator_cn"))
	source := extractString(evt.Data, "source")
	if source == "" {
		source = events.SubmissionSourceUnknown
	}
	dryRun := extractBool(evt.Data, "dry_run")
	accepted := extractBool(evt.Data, "accepted")
	rejectReason := extractString(evt.Data, "reject_reason")
	userAgent := truncate(extractString(evt.Data, "user_agent"), 256)

	_, err := tx.Exec(ctx, `
		INSERT INTO submission_history
			(id, submitted_at, actor, operator_cn, source, kind, resource_id,
			 dry_run, accepted, reject_reason, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO NOTHING
	`, evt.ID, evt.Timestamp,
		actor,
		nilIfEmpty(operatorCN),
		source, kind, resourceID,
		dryRun, accepted,
		nilIfEmpty(rejectReason),
		nilIfEmpty(userAgent))
	return err
}

// ── auth_events ────────────────────────────────────────────────────────────

func (s *Sink) upsertAuthEvent(ctx context.Context, tx pgx.Tx, evt events.Event, eventType string) error {
	actor := s.hashActorIfEnabled(extractString(evt.Data, "actor"))
	remoteIP := extractString(evt.Data, "remote_ip")
	userAgent := truncate(extractString(evt.Data, "user_agent"), 256)
	reason := extractString(evt.Data, "reason")

	// A malformed IP string is dropped to NULL (the schema's INET
	// type will reject a bad string, failing the whole batch). We
	// do the validation here so one bad record can't poison the
	// flush.
	ipParam := sanitiseIP(remoteIP)

	_, err := tx.Exec(ctx, `
		INSERT INTO auth_events (occurred_at, event_type, actor, remote_ip, user_agent, reason)
		VALUES ($1, $2, $3, $4::inet, $5, $6)
	`, evt.Timestamp, eventType,
		nilIfEmpty(actor),
		nilIfEmpty(ipParam),
		nilIfEmpty(userAgent),
		nilIfEmpty(reason))
	return err
}

// ── artifact_transfers ─────────────────────────────────────────────────────

func (s *Sink) upsertArtifactTransfer(ctx context.Context, tx pgx.Tx, evt events.Event, direction string) error {
	uri := extractString(evt.Data, "uri")
	if uri == "" {
		return nil
	}
	jobID := extractString(evt.Data, "job_id")
	bytes := extractInt64(evt.Data, "bytes")
	durationMs := extractInt(evt.Data, "duration_ms")

	// sha256_ok may be absent (upload) or present (download with
	// verify). Encode NULL for absent so the BOOLEAN column
	// distinguishes "not checked" from "checked and failed".
	var shaOKParam any
	if raw, ok := evt.Data["sha256_ok"]; ok {
		if b, ok := raw.(bool); ok {
			shaOKParam = b
		}
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO artifact_transfers
			(occurred_at, direction, job_id, uri, bytes, sha256_ok, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (occurred_at, job_id, direction, uri) DO NOTHING
	`, evt.Timestamp, direction,
		nilIfEmpty(jobID),
		uri, bytes,
		shaOKParam,
		nullIfZero(durationMs))
	return err
}

// ── service_probe_events ───────────────────────────────────────────────────

func (s *Sink) upsertServiceProbeEvent(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	newState := extractString(evt.Data, "new_state")
	if jobID == "" || newState == "" {
		return nil
	}
	consecutiveFails := extractInt(evt.Data, "consecutive_fails")

	_, err := tx.Exec(ctx, `
		INSERT INTO service_probe_events (occurred_at, job_id, new_state, consecutive_fails)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (job_id, occurred_at) DO NOTHING
	`, evt.Timestamp, jobID, newState, nullIfZero(consecutiveFails))
	return err
}

// ── job_log_entries ────────────────────────────────────────────────────────

func (s *Sink) upsertJobLog(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	jobID := extractString(evt.Data, "job_id")
	seq := extractInt64(evt.Data, "seq")
	if jobID == "" {
		return nil
	}
	data := extractString(evt.Data, "data")

	_, err := tx.Exec(ctx, `
		INSERT INTO job_log_entries (occurred_at, job_id, seq, data)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (job_id, seq) DO NOTHING
	`, evt.Timestamp, jobID, seq, data)
	return err
}

// ── shared tiny helpers ─────────────────────────────────────────────────────

// nullIfZero returns NULL for 0 / an int for anything else. Pairs
// with schema columns that use NULL to mean "not recorded" and 0
// to mean "zero-valued but recorded".
func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullIfZero64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// truncate caps s at max bytes. Used for user_agent to bound row
// size; the dashboard doesn't need the full UA string.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// sanitiseIP returns raw if it parses as a valid IP, empty string
// otherwise. Prevents PostgreSQL's INET cast from failing the whole
// batch on a malformed X-Forwarded-For value. http.Request.RemoteAddr
// arrives in `host:port` form; we split that first, then validate.
func sanitiseIP(raw string) string {
	if raw == "" {
		return ""
	}
	// Strip `:port` and `[ipv6]:port` forms via stdlib's SplitHostPort;
	// if it errors, fall back to treating raw as a bare host.
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = raw[1 : len(raw)-1]
	}
	if net.ParseIP(host) != nil {
		return host
	}
	return ""
}
