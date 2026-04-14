// Package registry holds the dataset and model metadata registries.
//
// Design: metadata only. Registered entries carry a URI pointing at the
// actual bytes in the artifact store (internal/artifacts). Storing the
// artifact bytes here would duplicate what the artifact store already
// does, and BadgerDB is the wrong tool for multi-GB model checkpoints.
// The registry answers "what does this named model look like?"; the
// artifact store answers "how do I fetch its bytes?"
//
// Entries are immutable once registered — version bumps create a new
// entry rather than mutating an existing one. This keeps the audit
// story simple ("v3.2.1 was the one trained on dataset v0.5 by alice
// on 2026-04-14") and matches how MLflow / W&B / similar tools behave.
package registry

import (
	"time"
)

// Dataset is a registered metadata record pointing at artifact bytes
// that represent a training / evaluation / inference dataset. Size
// and SHA256 are copies of what the artifact store reports so a
// downstream caller can verify bytes without a second Stat round-trip.
type Dataset struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	URI       string            `json:"uri"`
	SizeBytes int64             `json:"size_bytes,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	CreatedBy string            `json:"created_by"` // JWT subject of the caller
}

// Model is a registered metadata record pointing at artifact bytes
// that represent a trained model. Lineage is recorded (SourceJobID +
// SourceDataset) but not enforced — the registrar is trusted to
// stamp the right pointers. That matches the broader trust model
// where node-reported outputs are gated by attestOutputs but user-
// supplied metadata at the REST boundary rides on the JWT subject.
type Model struct {
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	URI           string            `json:"uri"`
	Framework     string            `json:"framework,omitempty"` // pytorch | onnx | tensorflow | other
	SourceJobID   string            `json:"source_job_id,omitempty"`
	SourceDataset DatasetRef        `json:"source_dataset,omitempty"`
	Metrics       map[string]float64 `json:"metrics,omitempty"`
	SizeBytes     int64             `json:"size_bytes,omitempty"`
	SHA256        string            `json:"sha256,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	CreatedBy     string            `json:"created_by"`
}

// DatasetRef is a lineage pointer a Model carries back to the
// dataset it was trained on. Both fields empty means "lineage not
// recorded"; a registrar is free to stamp either one or both.
type DatasetRef struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}
