// internal/events/topics.go
//
// Event topic constants and convenience constructors.

package events

import (
	"time"

	"github.com/google/uuid"
)

// Topic constants for all event types emitted by the system.
const (
	TopicJobSubmitted      = "job.submitted"
	TopicJobTransition     = "job.transition"
	TopicJobCompleted      = "job.completed"
	TopicJobFailed         = "job.failed"
	TopicJobRetrying       = "job.retrying"
	TopicNodeRegistered    = "node.registered"
	TopicNodeStale         = "node.stale"
	TopicNodeRevoked       = "node.revoked"
	TopicWorkflowCompleted = "workflow.completed"
	TopicWorkflowFailed    = "workflow.failed"

	// TopicJobUnschedulable fires when the dispatch loop cannot find
	// a healthy node whose labels satisfy the job's node_selector.
	// The job stays in the pending queue — the operator gets the
	// event as a diagnostic signal, not a retry trigger.
	TopicJobUnschedulable = "job.unschedulable"

	// ML registry lifecycle events. Carry enough metadata for a
	// subscriber (analytics sink, dashboard, external webhook
	// relay) to answer "what got registered, by whom, when?"
	// without a second round-trip to the registry API.
	TopicDatasetRegistered = "dataset.registered"
	TopicDatasetDeleted    = "dataset.deleted"
	TopicModelRegistered   = "model.registered"
	TopicModelDeleted      = "model.deleted"

	// TopicMLResolveFailed fires when the dispatch loop's artifact
	// resolver (ResolveJobInputs) cannot materialise an input URI
	// from an upstream workflow job's ResolvedOutputs. Distinct from
	// the generic job.failed so the dashboard's Pipelines view can
	// surface ML-specific pipeline breakage at a glance.
	TopicMLResolveFailed = "ml.resolve_failed"

	// Feature 28 — unified analytics sink. One topic per event family
	// the coordinator emits for the operational-window (analytics)
	// store. The audit log keeps its own canonical events; analytics
	// carries a time-series-shaped mirror.

	// TopicSubmissionRecorded fires on every POST /jobs + POST
	// /workflows — accepted, rejected, dry-run — so the dashboard
	// can answer "what did actor X submit in the last week?"
	// without scanning the audit log. resource_id ties back to the
	// full audit event when a forensic reviewer needs the body.
	TopicSubmissionRecorded = "submission.recorded"

	// TopicAuthOK / Fail / RateLimit / TokenMint feed the analytics
	// auth-events panel. Reason values for fails are one of the
	// AuthFailReason* constants below.
	TopicAuthOK         = "auth.ok"
	TopicAuthFail       = "auth.fail"
	TopicAuthRateLimit  = "auth.rate_limit"
	TopicAuthTokenMint  = "auth.token_mint"

	// TopicArtifactUploaded / Downloaded fire at artifact-store call
	// sites on completion, carrying bytes + duration + SHA-verify
	// outcome (for downloads that use GetAndVerifyTo). uri is the
	// canonical reference, never a presigned URL — presigned links
	// carry capability and must not land in analytics.
	TopicArtifactUploaded   = "artifact.uploaded"
	TopicArtifactDownloaded = "artifact.downloaded"

	// TopicServiceProbeTransition mirrors feature 17's edge-triggered
	// readiness state machine. One event per ready ↔ unhealthy flip;
	// consecutive_fails carries the streak leading up to the event.
	TopicServiceProbeTransition = "service.probe_transition"

	// TopicJobLog fires per log chunk captured by the log store. The
	// analytics sink persists these into job_log_entries (PostgreSQL)
	// so the operational window can eventually free BadgerDB's log
	// TTL pressure. See feature 28 for the migration story.
	TopicJobLog = "job.log"
)

// Submission sources — string constants kept stable so the analytics
// table's source column can be compared to enumerated values in
// queries. 'unknown' is the default when the handler cannot parse a
// User-Agent.
const (
	SubmissionSourceDashboard = "dashboard"
	SubmissionSourceCLI       = "cli"
	SubmissionSourceCI        = "ci"
	SubmissionSourceUnknown   = "unknown"
)

// AuthFailReason* values carried on TopicAuthFail events. Stable
// wire strings — the dashboard filters on them verbatim.
const (
	AuthFailReasonMissingToken     = "missing_token"
	AuthFailReasonInvalidSignature = "invalid_signature"
	AuthFailReasonExpired          = "expired"
	AuthFailReasonRevoked          = "revoked"
	AuthFailReasonInvalidFormat    = "invalid_format"
)

// Reason values attached to the feature-18 JobUnschedulable event so
// the dashboard can distinguish "no nodes at all" from "wrong
// labels" from "right labels but nodes are stale". The three values
// are stable wire strings — renaming them is a dashboard-breaking
// change.
const (
	UnschedulableReasonNoHealthyNode      = "no_healthy_node"
	UnschedulableReasonNoMatchingLabel    = "no_matching_label"
	UnschedulableReasonAllMatchingStale   = "all_matching_unhealthy"
)

// NewEvent creates an Event with a generated ID and current timestamp.
func NewEvent(topic string, data map[string]any) Event {
	return Event{
		ID:        uuid.NewString(),
		Type:      topic,
		Timestamp: time.Now(),
		Data:      data,
	}
}

// JobSubmitted creates a job.submitted event.
func JobSubmitted(jobID, command string, priority uint32) Event {
	return NewEvent(TopicJobSubmitted, map[string]any{
		"job_id":   jobID,
		"command":  command,
		"priority": priority,
	})
}

// JobTransition creates a job.transition event.
func JobTransition(jobID, from, to, nodeID string) Event {
	return NewEvent(TopicJobTransition, map[string]any{
		"job_id":      jobID,
		"from_status": from,
		"to_status":   to,
		"node_id":     nodeID,
	})
}

// JobCompleted creates a job.completed event.
func JobCompleted(jobID, nodeID string, durationMs int64) Event {
	return NewEvent(TopicJobCompleted, map[string]any{
		"job_id":      jobID,
		"node_id":     nodeID,
		"duration_ms": durationMs,
	})
}

// ArtifactSummary is a minimal artifact view embedded in event
// payloads. Decoupled from cpb.ArtifactOutput to keep the events
// package import-free of coordinator types.
type ArtifactSummary struct {
	Name   string `json:"name"`
	URI    string `json:"uri"`
	SHA256 string `json:"sha256,omitempty"`
}

// JobCompletedWithOutputs is the ML-pipeline variant of JobCompleted —
// it attaches the node's resolved artifact URIs under an "outputs"
// key so analytics subscribers and (step 3) the workflow engine can
// trace data flow across jobs without a separate lookup.
func JobCompletedWithOutputs(jobID, nodeID string, durationMs int64, outputs []ArtifactSummary) Event {
	data := map[string]any{
		"job_id":      jobID,
		"node_id":     nodeID,
		"duration_ms": durationMs,
	}
	if len(outputs) > 0 {
		rows := make([]map[string]any, len(outputs))
		for i, o := range outputs {
			row := map[string]any{"name": o.Name, "uri": o.URI}
			if o.SHA256 != "" {
				row["sha256"] = o.SHA256
			}
			rows[i] = row
		}
		data["outputs"] = rows
	}
	return NewEvent(TopicJobCompleted, data)
}

// JobFailed creates a job.failed event.
func JobFailed(jobID, errMsg string, exitCode int32, attempt uint32) Event {
	return NewEvent(TopicJobFailed, map[string]any{
		"job_id":    jobID,
		"error":     errMsg,
		"exit_code": exitCode,
		"attempt":   attempt,
	})
}

// JobRetrying creates a job.retrying event.
func JobRetrying(jobID string, attempt uint32, nextRetryAt time.Time) Event {
	return NewEvent(TopicJobRetrying, map[string]any{
		"job_id":        jobID,
		"attempt":       attempt,
		"next_retry_at": nextRetryAt.Format(time.RFC3339),
	})
}

// NodeRegistered creates a node.registered event.
func NodeRegistered(nodeID, address string) Event {
	return NewEvent(TopicNodeRegistered, map[string]any{
		"node_id": nodeID,
		"address": address,
	})
}

// NodeRegisteredWithLabels attaches the node's reported labels to the
// event payload so the analytics sink (and step-8 dashboard) can
// answer "which nodes advertise gpu=a100 in our cluster right now?"
// without re-querying the registry. Labels are copied defensively;
// subscribers cannot mutate the source map. An empty/nil labels
// argument produces the same shape as the label-less constructor
// (no "labels" key) to match historical payloads.
func NodeRegisteredWithLabels(nodeID, address string, labels map[string]string) Event {
	data := map[string]any{
		"node_id": nodeID,
		"address": address,
	}
	if len(labels) > 0 {
		cp := make(map[string]string, len(labels))
		for k, v := range labels {
			cp[k] = v
		}
		data["labels"] = cp
	}
	return NewEvent(TopicNodeRegistered, data)
}

// NodeStale creates a node.stale event.
func NodeStale(nodeID string) Event {
	return NewEvent(TopicNodeStale, map[string]any{
		"node_id": nodeID,
	})
}

// NodeRevoked creates a node.revoked event.
func NodeRevoked(nodeID, reason string) Event {
	return NewEvent(TopicNodeRevoked, map[string]any{
		"node_id": nodeID,
		"reason":  reason,
	})
}

// WorkflowCompleted creates a workflow.completed event.
//
// Kept for backwards compatibility with direct callers. The
// coordinator uses WorkflowCompletedWithCounts (below) so the
// feature-40 analytics sink can persist a denormalised
// workflow_outcomes row without querying the job store.
func WorkflowCompleted(workflowID string) Event {
	return NewEvent(TopicWorkflowCompleted, map[string]any{
		"workflow_id": workflowID,
	})
}

// WorkflowCompletedWithCounts is the feature-40 enriched variant.
// The coordinator computes job_count/success_count/failed_count
// when it detects every job is terminal, then emits this event so
// the analytics sink can upsert workflow_outcomes in a single
// pass. Tags + owner carry through from the submission record so
// the dashboard can filter on them without a second lookup.
//
// Feature 40c — startedAt and finishedAt let the sink compute
// duration_ms without a second job-store lookup. A zero
// startedAt (the workflow was submitted but never started, e.g.
// rejected at dispatch) produces an omitted duration_ms on the
// payload; the sink then writes NULL into the column so "ran for
// 0 ms" and "never ran" stay distinguishable downstream.
//
// Defensive copy on `tags`: callers often pass the workflow's
// own map, which a later writer could mutate under us. Copying
// here prevents a subscriber from observing a torn tag set
// mid-mutation.
func WorkflowCompletedWithCounts(
	workflowID, ownerPrincipal string,
	jobCount, successCount, failedCount int,
	tags map[string]string,
	startedAt, finishedAt time.Time,
) Event {
	data := map[string]any{
		"workflow_id":   workflowID,
		"job_count":     jobCount,
		"success_count": successCount,
		"failed_count":  failedCount,
	}
	if ownerPrincipal != "" {
		data["owner_principal"] = ownerPrincipal
	}
	if len(tags) > 0 {
		cp := make(map[string]string, len(tags))
		for k, v := range tags {
			cp[k] = v
		}
		data["tags"] = cp
	}
	if !startedAt.IsZero() {
		data["started_at"] = startedAt.UTC().Format(time.RFC3339Nano)
	}
	if !finishedAt.IsZero() {
		data["finished_at"] = finishedAt.UTC().Format(time.RFC3339Nano)
		if !startedAt.IsZero() && !finishedAt.Before(startedAt) {
			data["duration_ms"] = finishedAt.Sub(startedAt).Milliseconds()
		}
	}
	return NewEvent(TopicWorkflowCompleted, data)
}

// DatasetRegistered creates a dataset.registered event. Payload
// carries name + version + size so a subscriber can aggregate
// storage footprint per registrar without re-querying.
func DatasetRegistered(name, version, uri, actor string, size int64) Event {
	return NewEvent(TopicDatasetRegistered, map[string]any{
		"name":       name,
		"version":    version,
		"uri":        uri,
		"actor":      actor,
		"size_bytes": size,
	})
}

// DatasetDeleted creates a dataset.deleted event.
func DatasetDeleted(name, version, actor string) Event {
	return NewEvent(TopicDatasetDeleted, map[string]any{
		"name":    name,
		"version": version,
		"actor":   actor,
	})
}

// ModelRegistered creates a model.registered event. Lineage fields
// (source_job_id, source_dataset) are included when non-empty so
// subscribers can build the "what trained this model" graph
// without reading the full record.
func ModelRegistered(name, version, uri, actor string, sourceJobID string, sourceDatasetName, sourceDatasetVersion string) Event {
	data := map[string]any{
		"name":    name,
		"version": version,
		"uri":     uri,
		"actor":   actor,
	}
	if sourceJobID != "" {
		data["source_job_id"] = sourceJobID
	}
	if sourceDatasetName != "" {
		data["source_dataset"] = map[string]string{
			"name":    sourceDatasetName,
			"version": sourceDatasetVersion,
		}
	}
	return NewEvent(TopicModelRegistered, data)
}

// ModelDeleted creates a model.deleted event.
func ModelDeleted(name, version, actor string) Event {
	return NewEvent(TopicModelDeleted, map[string]any{
		"name":    name,
		"version": version,
		"actor":   actor,
	})
}

// WorkflowFailed creates a workflow.failed event.
//
// Kept for backwards compatibility. The coordinator uses
// WorkflowFailedWithCounts (below) in feature 40.
func WorkflowFailed(workflowID, failedJob string) Event {
	return NewEvent(TopicWorkflowFailed, map[string]any{
		"workflow_id": workflowID,
		"failed_job":  failedJob,
	})
}

// WorkflowFailedWithCounts is the feature-40 enriched variant.
// Same analytics sink contract as WorkflowCompletedWithCounts —
// the row lands in workflow_outcomes with status='failed', the
// failed-job attribution, counts, owner, tags, and timing.
func WorkflowFailedWithCounts(
	workflowID, failedJob, ownerPrincipal string,
	jobCount, successCount, failedCount int,
	tags map[string]string,
	startedAt, finishedAt time.Time,
) Event {
	data := map[string]any{
		"workflow_id":   workflowID,
		"failed_job":    failedJob,
		"job_count":     jobCount,
		"success_count": successCount,
		"failed_count":  failedCount,
	}
	if ownerPrincipal != "" {
		data["owner_principal"] = ownerPrincipal
	}
	if len(tags) > 0 {
		cp := make(map[string]string, len(tags))
		for k, v := range tags {
			cp[k] = v
		}
		data["tags"] = cp
	}
	if !startedAt.IsZero() {
		data["started_at"] = startedAt.UTC().Format(time.RFC3339Nano)
	}
	if !finishedAt.IsZero() {
		data["finished_at"] = finishedAt.UTC().Format(time.RFC3339Nano)
		if !startedAt.IsZero() && !finishedAt.Before(startedAt) {
			data["duration_ms"] = finishedAt.Sub(startedAt).Milliseconds()
		}
	}
	return NewEvent(TopicWorkflowFailed, data)
}

// JobUnschedulable fires when the dispatch loop has healthy nodes but
// none of them match the job's node_selector. The payload echoes the
// unsatisfied selector so operators can inspect which label set would
// have unblocked the job — no need to grep coordinator logs.
func JobUnschedulable(jobID string, selector map[string]string, reason string) Event {
	// Defensive copy: a consumer mutating the payload map must not
	// bleed into another subscriber's view of the same event.
	sel := make(map[string]string, len(selector))
	for k, v := range selector {
		sel[k] = v
	}
	return NewEvent(TopicJobUnschedulable, map[string]any{
		"job_id":               jobID,
		"unsatisfied_selector": sel,
		// reason distinguishes the three causes for dashboard
		// triage. One of UnschedulableReason* constants above, or
		// empty on code paths that pre-date the field.
		"reason": reason,
	})
}

// MLResolveFailed fires when the dispatch loop's artifact resolver
// cannot satisfy a workflow job's From references. Surfaced on the
// dashboard's Pipelines view (feature 18 step-3 follow-up) so a
// broken ML pipeline is one click away from the operator.
func MLResolveFailed(jobID, workflowID, upstream, outputName, reason string) Event {
	return NewEvent(TopicMLResolveFailed, map[string]any{
		"job_id":      jobID,
		"workflow_id": workflowID,
		"upstream":    upstream,
		"output_name": outputName,
		"reason":      reason,
	})
}

// ── Feature 28 constructors ──────────────────────────────────────────────────

// SubmissionRecorded describes a POST /jobs or POST /workflows outcome.
// Every field is present even on rejection so the retention cron can
// drop mid-request records without keeping orphans.
//
//   actor        — JWT subject (stamped by authMiddleware). 'anonymous' in dev.
//   operatorCN   — feature 27 client-cert CN, empty when mTLS is off.
//   source       — one of SubmissionSource* constants.
//   kind         — 'job' or 'workflow'.
//   resourceID   — job_id / workflow_id. Ties back to the audit record.
//   dryRun       — true when ?dry_run=true. rejected dry-runs still
//                  record here; the operator learns what would have
//                  happened without probing the full submit path.
//   accepted     — true iff the request would persist on the real path.
//   rejectReason — short, validator-returned reason string; empty on accept.
//   userAgent    — truncated by caller to avoid BufferLimit pressure.
func SubmissionRecorded(actor, operatorCN, source, kind, resourceID string,
	dryRun, accepted bool, rejectReason, userAgent string) Event {
	return NewEvent(TopicSubmissionRecorded, map[string]any{
		"actor":         actor,
		"operator_cn":   operatorCN,
		"source":        source,
		"kind":          kind,
		"resource_id":   resourceID,
		"dry_run":       dryRun,
		"accepted":      accepted,
		"reject_reason": rejectReason,
		"user_agent":    userAgent,
	})
}

// AuthOK fires when authMiddleware successfully validates a bearer
// token. actor is the JWT subject; remoteIP is the client's address
// (may be the loopback proxy in Nginx deployments); userAgent is
// truncated by the middleware.
func AuthOK(actor, remoteIP, userAgent string) Event {
	return NewEvent(TopicAuthOK, map[string]any{
		"actor":      actor,
		"remote_ip":  remoteIP,
		"user_agent": userAgent,
	})
}

// AuthFail fires on every auth rejection. reason is one of the
// AuthFailReason* constants above. actor is empty unless the token
// parsed far enough to extract a subject.
func AuthFail(reason, actor, remoteIP, userAgent string) Event {
	return NewEvent(TopicAuthFail, map[string]any{
		"reason":     reason,
		"actor":      actor,
		"remote_ip":  remoteIP,
		"user_agent": userAgent,
	})
}

// AuthRateLimit fires when a per-subject limiter returns 429. actor
// is the subject whose bucket emptied; path distinguishes /admin/*
// limiters from analytics-query limiters when the dashboard panel
// breaks them out.
func AuthRateLimit(actor, path, remoteIP string) Event {
	return NewEvent(TopicAuthRateLimit, map[string]any{
		"actor":     actor,
		"path":      path,
		"remote_ip": remoteIP,
	})
}

// AuthTokenMint fires on POST /admin/tokens after the token is
// issued. issuedBy is the admin's subject; subject + role describe
// the new token; ttlHours lets the dashboard surface "short-lived
// token rate" vs "hour-long tokens" over time.
func AuthTokenMint(issuedBy, subject, role string, ttlHours int) Event {
	return NewEvent(TopicAuthTokenMint, map[string]any{
		"actor":     issuedBy,
		"subject":   subject,
		"role":      role,
		"ttl_hours": ttlHours,
	})
}

// ArtifactUploaded / Downloaded fire at the artifact store's Put /
// GetAndVerifyTo call sites on completion. bytes is the transfer
// size; durationMs is wall-clock from start to completion; sha256OK
// is nil on upload (no verify performed) and *true/*false on
// download when GetAndVerifyTo did a hash check.
//
// uri is the canonical reference — e.g., "artifacts://<hash>" or
// "s3://bucket/key" — NOT a presigned URL. See 005_unified_sink.up.sql
// for the security reasoning.
func ArtifactUploaded(jobID, uri string, bytes int64, durationMs int) Event {
	return NewEvent(TopicArtifactUploaded, map[string]any{
		"job_id":      jobID,
		"uri":         uri,
		"bytes":       bytes,
		"duration_ms": durationMs,
	})
}

func ArtifactDownloaded(jobID, uri string, bytes int64, durationMs int, sha256OK *bool) Event {
	data := map[string]any{
		"job_id":      jobID,
		"uri":         uri,
		"bytes":       bytes,
		"duration_ms": durationMs,
	}
	if sha256OK != nil {
		data["sha256_ok"] = *sha256OK
	}
	return NewEvent(TopicArtifactDownloaded, data)
}

// ServiceProbeTransition fires on the edge of the feature-17
// readiness state machine. newState is 'ready' | 'unhealthy' |
// 'gone'; consecutiveFails carries the streak leading up to the
// event (0 on ready→unhealthy of a never-seen service; >0 on the
// back-and-forth flips).
func ServiceProbeTransition(jobID, newState string, consecutiveFails uint32) Event {
	return NewEvent(TopicServiceProbeTransition, map[string]any{
		"job_id":             jobID,
		"new_state":          newState,
		"consecutive_fails":  consecutiveFails,
	})
}

// JobLog fires per chunk captured by the log store. Emitted by the
// logstore's Append wrapper (feature 28) so analytics persists a
// durable-PG copy of job stdout/stderr; the Badger-side copy is
// still the primary read path until a follow-up slice switches it
// over. seq is the in-job line number from logstore.LogEntry.
//
// data is TEXT-sized by the runtime (stdout/stderr is UTF-8); a
// large chunk is clamped at the logstore layer, so by the time
// this constructor fires the string is already bounded.
func JobLog(jobID string, seq int64, timestamp time.Time, data string) Event {
	return Event{
		ID:        uuid.NewString(),
		Type:      TopicJobLog,
		Timestamp: timestamp,
		Data: map[string]any{
			"job_id": jobID,
			"seq":    seq,
			"data":   data,
		},
	}
}
