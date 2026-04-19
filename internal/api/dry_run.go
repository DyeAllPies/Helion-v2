// internal/api/dry_run.go
//
// Feature 24 — dry-run preflight support shared across every submit
// endpoint (POST /jobs, POST /workflows, POST /api/datasets, POST
// /api/models). A `?dry_run=true` query parameter on any of those
// paths runs the full validator stack without persisting state,
// enqueuing dispatch, or publishing bus events.
//
// Invariants callers must uphold:
//
//   1. Dry-run goes through the SAME middleware chain as the real
//      path (auth → rate limit → body cap → validators). Never
//      short-circuit any of those — a dry-run that skipped auth
//      would be a probe oracle; one that skipped rate limit would
//      be a flood vector.
//   2. Dry-run returns the SAME response shape as a successful
//      submit, with an added "dry_run": true boolean at the top
//      level so a client can tell which flavour it got without
//      parsing the URL again.
//   3. Dry-run emits a DISTINCT audit event type (e.g.
//      `job_dry_run` vs `job_submit`) so reviewers can filter the
//      audit log by kind without regex-matching event details.
//   4. An unparseable `dry_run` value (e.g. `?dry_run=maybe`) is
//      a 400. Silent fallback to the real path would turn a typo
//      into an unintended submission — we reject instead.

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// dryRunResponse wraps any successful-submit response body with a
// top-level `"dry_run": true` marker so the client can tell at a
// glance which flavour it got. Callers use it like:
//
//	writeJSON(w, "...dry_run", dryRunResponse(jobToResponse(job)))
//
// Implementation: Go's encoding/json does NOT support field inlining
// for non-embedded interface{} fields (tried ",inline" tag — renders
// a nested "Body" key instead of flattening). So we round-trip the
// body through JSON to get a map, then splice the `dry_run` flag in
// at the top level. A dry-run response therefore carries the exact
// same keys as the real response plus one extra `dry_run` boolean.
//
// The `body` argument must marshal into a JSON object. If a handler
// ever passes a non-object (array, primitive), we fall back to
// wrapping it under a `body` key so the response is still valid JSON
// — never return a nil body or drop the dry_run flag.
func dryRunResponse(body interface{}) interface{} {
	data, err := json.Marshal(body)
	if err != nil {
		// Should not happen for well-formed response structs; guard anyway.
		return map[string]interface{}{"dry_run": true, "marshal_error": err.Error()}
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		// Body wasn't a JSON object — wrap it so the response is
		// still structurally valid and the dry_run flag survives.
		return map[string]interface{}{"dry_run": true, "body": body}
	}
	m["dry_run"] = true
	return m
}

// ParseDryRunParam returns (true, nil) when the request carries a
// recognised "true" value on the `dry_run` query parameter,
// (false, nil) when the parameter is absent or a recognised "false"
// value, and (false, error) when the value is present but
// unrecognised. The caller treats the error case as 400.
//
// Accepted truthy values: `1`, `true`, `yes` (case-insensitive).
// Accepted falsy values:  `0`, `false`, `no`, empty string.
// Everything else returns an error.
//
// Exported (capital P) so handler tests in external packages can
// reuse the same parser.
func ParseDryRunParam(r *http.Request) (bool, error) {
	raw := r.URL.Query().Get("dry_run")
	switch strings.ToLower(raw) {
	case "":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	case "0", "false", "no":
		return false, nil
	default:
		return false, fmt.Errorf("dry_run: expected 1/true/yes/0/false/no, got %q", raw)
	}
}
