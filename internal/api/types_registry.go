// internal/api/types_registry.go
//
// REST request / response shapes for the dataset + model registry.
// Separated from types.go because the registry surface is large
// enough to deserve its own file and will expand (tags-search,
// lineage-graph, signed-URL fetch) in follow-up slices.

package api

import (
	"time"
)

// DatasetRegisterRequest is the JSON body for POST /api/datasets.
// Name + version + uri are required; tags optional.
type DatasetRegisterRequest struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	URI       string            `json:"uri"`
	SizeBytes int64             `json:"size_bytes,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// DatasetResponse is the JSON body returned by register / get /
// list. Matches the persisted registry.Dataset shape but stays in
// the api package so a future internal field change doesn't leak
// into the public HTTP contract.
type DatasetResponse struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	URI       string            `json:"uri"`
	SizeBytes int64             `json:"size_bytes,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	CreatedBy string            `json:"created_by"`
}

// DatasetListResponse paginates GET /api/datasets.
type DatasetListResponse struct {
	Datasets []DatasetResponse `json:"datasets"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	Size     int               `json:"size"`
}

// ModelRegisterRequest is the JSON body for POST /api/models.
type ModelRegisterRequest struct {
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	URI           string              `json:"uri"`
	Framework     string              `json:"framework,omitempty"`
	SourceJobID   string              `json:"source_job_id,omitempty"`
	SourceDataset *DatasetRefRequest  `json:"source_dataset,omitempty"`
	Metrics       map[string]float64  `json:"metrics,omitempty"`
	SizeBytes     int64               `json:"size_bytes,omitempty"`
	SHA256        string              `json:"sha256,omitempty"`
	Tags          map[string]string   `json:"tags,omitempty"`
}

// DatasetRefRequest is the lineage pointer on a Model register. Both
// fields together or both empty — partial pointers are rejected at
// validate time (see registry.ValidateModel).
type DatasetRefRequest struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ModelResponse is the JSON body for register / get / latest / list.
type ModelResponse struct {
	Name          string             `json:"name"`
	Version       string             `json:"version"`
	URI           string             `json:"uri"`
	Framework     string             `json:"framework,omitempty"`
	SourceJobID   string             `json:"source_job_id,omitempty"`
	SourceDataset *DatasetRefRequest `json:"source_dataset,omitempty"`
	Metrics       map[string]float64 `json:"metrics,omitempty"`
	SizeBytes     int64              `json:"size_bytes,omitempty"`
	SHA256        string             `json:"sha256,omitempty"`
	Tags          map[string]string  `json:"tags,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	CreatedBy     string             `json:"created_by"`
}

// ModelListResponse paginates GET /api/models.
type ModelListResponse struct {
	Models []ModelResponse `json:"models"`
	Total  int             `json:"total"`
	Page   int             `json:"page"`
	Size   int             `json:"size"`
}
