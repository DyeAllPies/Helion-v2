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
func WorkflowCompleted(workflowID string) Event {
	return NewEvent(TopicWorkflowCompleted, map[string]any{
		"workflow_id": workflowID,
	})
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
func WorkflowFailed(workflowID, failedJob string) Event {
	return NewEvent(TopicWorkflowFailed, map[string]any{
		"workflow_id": workflowID,
		"failed_job":  failedJob,
	})
}

// JobUnschedulable fires when the dispatch loop has healthy nodes but
// none of them match the job's node_selector. The payload echoes the
// unsatisfied selector so operators can inspect which label set would
// have unblocked the job — no need to grep coordinator logs.
func JobUnschedulable(jobID string, selector map[string]string) Event {
	// Defensive copy: a consumer mutating the payload map must not
	// bleed into another subscriber's view of the same event.
	sel := make(map[string]string, len(selector))
	for k, v := range selector {
		sel[k] = v
	}
	return NewEvent(TopicJobUnschedulable, map[string]any{
		"job_id":              jobID,
		"unsatisfied_selector": sel,
	})
}
