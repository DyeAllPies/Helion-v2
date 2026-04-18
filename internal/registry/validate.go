// internal/registry/validate.go
//
// Submit-time validation for dataset and model registration. The REST
// handler runs these before persistence; keeping them in the registry
// package (rather than the api package) means a library-level caller
// who bypasses HTTP still gets the same guards.

package registry

import (
	"fmt"
	"math"
	"strings"
)

// Shape caps. Match the k8s-compatible bounds used elsewhere in the
// project (node labels, node selectors) so a tag or metric survived
// from one place lands valid in another.
const (
	maxNameLen     = 253
	maxVersionLen  = 64
	maxURILen      = 2048
	maxFrameworkLen = 32
	maxTagEntries  = 32
	maxTagKeyLen   = 63
	maxTagValLen   = 253
	maxMetricEntries = 64
	maxMetricKeyLen  = 63
)

// nameSegment is the charset allowed inside a dataset / model name.
// Matches k8s DNS label rules (lowercase alnum + '-' + '.') so names
// stay shell-safe, URL-safe, and BadgerDB-key-safe without any
// escaping at any layer.
func isNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
	case b >= '0' && b <= '9':
	case b == '-', b == '.', b == '_':
	default:
		return false
	}
	return true
}

// ValidateName runs the charset + length check a registered name
// must pass. Empty names are rejected. Returned error is a
// human-readable reason suitable for a 400 body.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("name must not exceed %d bytes", maxNameLen)
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return fmt.Errorf("name must match [a-z0-9._-] (got byte 0x%02x)", name[i])
		}
	}
	return nil
}

// ValidateVersion accepts a broad shape (semver-ish, date tags,
// hash-like strings) rather than strict SemVer because ML pipelines
// commonly stamp versions from commit SHAs or ISO timestamps. The
// rule is "URL-safe, shell-safe, printable ASCII" — same intent as
// ValidateName but with a couple of extra characters allowed
// (namely '+' for build metadata).
func ValidateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("version is required")
	}
	if len(v) > maxVersionLen {
		return fmt.Errorf("version must not exceed %d bytes", maxVersionLen)
	}
	for i := 0; i < len(v); i++ {
		b := v[i]
		switch {
		case b >= 'a' && b <= 'z':
		case b >= 'A' && b <= 'Z':
		case b >= '0' && b <= '9':
		case b == '-', b == '.', b == '_', b == '+':
		default:
			return fmt.Errorf("version must match [A-Za-z0-9._+-] (got byte 0x%02x)", b)
		}
	}
	return nil
}

// ValidateURI checks the URI is under one of the artifact-store
// schemes Helion understands. Stricter schemes (e.g. full URL parsing
// + bucket-pin) live on the node-side attestOutputs; here we just
// prevent an obvious misconfiguration (registering a raw HTTP URL
// that the stager can't read).
func ValidateURI(uri string) error {
	if uri == "" {
		return fmt.Errorf("uri is required")
	}
	if len(uri) > maxURILen {
		return fmt.Errorf("uri must not exceed %d bytes", maxURILen)
	}
	for i := 0; i < len(uri); i++ {
		if b := uri[i]; b == 0 || b < 0x20 || b == 0x7f {
			return fmt.Errorf("uri must not contain NUL or control bytes")
		}
	}
	if !strings.HasPrefix(uri, "file://") && !strings.HasPrefix(uri, "s3://") {
		return fmt.Errorf("uri scheme must be file:// or s3://")
	}
	return nil
}

// ValidateFramework is a light check — we accept any string in the
// framework field so users can register ONNX / custom formats /
// future frameworks without code changes. Reject only NUL/control
// bytes and oversize to keep the log / audit stream clean.
func ValidateFramework(f string) error {
	if len(f) > maxFrameworkLen {
		return fmt.Errorf("framework must not exceed %d bytes", maxFrameworkLen)
	}
	for i := 0; i < len(f); i++ {
		if b := f[i]; b == 0 || b < 0x20 || b == 0x7f {
			return fmt.Errorf("framework must not contain NUL or control bytes")
		}
	}
	return nil
}

// ValidateTags enforces k8s-label-shaped bounds on the free-form
// tag map. Same rule as the node-selector validator in
// internal/api/handlers_jobs.go — consistent so a user's mental
// model is "Helion tags behave like k8s labels."
func ValidateTags(tags map[string]string) error {
	if len(tags) > maxTagEntries {
		return fmt.Errorf("tags must not exceed %d entries", maxTagEntries)
	}
	for k, v := range tags {
		if k == "" || len(k) > maxTagKeyLen {
			return fmt.Errorf("tag keys must be 1-%d bytes", maxTagKeyLen)
		}
		if strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("tag keys must not contain '=' or NUL")
		}
		if len(v) > maxTagValLen {
			return fmt.Errorf("tag values must not exceed %d bytes", maxTagValLen)
		}
		if strings.ContainsRune(v, '\x00') {
			return fmt.Errorf("tag values must not contain NUL")
		}
	}
	return nil
}

// ValidateMetrics caps the Metrics map size and key shape. Values
// are float64 (well-bounded by type), so we only check for NaN /
// infinity which would break JSON serialisation and mislead
// analytics consumers.
func ValidateMetrics(metrics map[string]float64) error {
	if len(metrics) > maxMetricEntries {
		return fmt.Errorf("metrics must not exceed %d entries", maxMetricEntries)
	}
	for k, v := range metrics {
		if k == "" || len(k) > maxMetricKeyLen {
			return fmt.Errorf("metric keys must be 1-%d bytes", maxMetricKeyLen)
		}
		if strings.ContainsRune(k, '\x00') {
			return fmt.Errorf("metric keys must not contain NUL")
		}
		// NaN and ±Inf can't round-trip through the JSON encoder
		// (encoding/json rejects them). Catch here with a clearer
		// error message than the handler would otherwise surface.
		// Use math.IsNaN / math.IsInf explicitly so finite values
		// near MaxFloat64 (≈1.8e308) are accepted — a prior check
		// used `v > 1e308 || v < -1e308` which spuriously rejected
		// any finite metric above 1e308.
		if math.IsNaN(v) {
			return fmt.Errorf("metric %q is NaN", k)
		}
		if math.IsInf(v, 0) {
			return fmt.Errorf("metric %q is infinite", k)
		}
	}
	return nil
}

// ValidateDataset runs the full submit-time check for a dataset. The
// handler passes the caller-populated struct; CreatedAt / CreatedBy
// are stamped after validation by the handler from the JWT context.
func ValidateDataset(d *Dataset) error {
	if err := ValidateName(d.Name); err != nil {
		return fmt.Errorf("dataset: %w", err)
	}
	if err := ValidateVersion(d.Version); err != nil {
		return fmt.Errorf("dataset: %w", err)
	}
	if err := ValidateURI(d.URI); err != nil {
		return fmt.Errorf("dataset: %w", err)
	}
	if err := ValidateTags(d.Tags); err != nil {
		return fmt.Errorf("dataset: %w", err)
	}
	return nil
}

// ValidateModel runs the full submit-time check for a model,
// including the optional lineage pointer. An empty SourceDataset is
// accepted (lineage not recorded); a partial one (name set, version
// empty, or vice versa) is rejected — if you're recording lineage,
// both halves must be there or the pointer is noise.
func ValidateModel(m *Model) error {
	if err := ValidateName(m.Name); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := ValidateVersion(m.Version); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := ValidateURI(m.URI); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := ValidateFramework(m.Framework); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := ValidateTags(m.Tags); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := ValidateMetrics(m.Metrics); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	// Lineage: both or neither. A model claiming to come from a
	// dataset without saying which version is unusable for the
	// "what did we train on?" audit story.
	if m.SourceDataset.Name != "" || m.SourceDataset.Version != "" {
		if err := ValidateName(m.SourceDataset.Name); err != nil {
			return fmt.Errorf("model.source_dataset: %w", err)
		}
		if err := ValidateVersion(m.SourceDataset.Version); err != nil {
			return fmt.Errorf("model.source_dataset: %w", err)
		}
	}
	return nil
}
