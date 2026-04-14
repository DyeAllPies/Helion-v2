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

// WorkflowFailed creates a workflow.failed event.
func WorkflowFailed(workflowID, failedJob string) Event {
	return NewEvent(TopicWorkflowFailed, map[string]any{
		"workflow_id": workflowID,
		"failed_job":  failedJob,
	})
}
