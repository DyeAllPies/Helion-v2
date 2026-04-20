// internal/api/handlers_logs_test.go
//
// Feature 29 — HTTP-layer integration tests for secret log
// scrubbing. The scrubber's unit tests live in
// internal/logstore/scrub_test.go; these tests verify the
// full end-to-end flow: a job is submitted with a secret env
// var, a log chunk carrying that secret lands in the
// logstore, and GET /jobs/{id}/logs returns the chunk with
// the secret value replaced by [REDACTED].
//
// Two layers are under test:
//   1. Write-path scrubbing via the ScrubbingStore decorator.
//   2. Response-path redactor in handleGetJobLogs (belt-and-
//      braces against chunks that landed before the decorator
//      was wired — see the redactor's doc).
//
// Also covers the RBAC gate on /jobs/{id}/logs (non-owner
// cannot fetch another user's logs).

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/logstore"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// logsFixture stands up an auth-enabled server with a real
// JobStore and a log store wrapped in ScrubbingStore. Returns
// everything the tests need to touch each layer directly.
func logsFixture(t *testing.T) (srv *api.Server, tm *auth.TokenManager, cs *cluster.JobStore, rawLog *logstore.MemLogStore, scrubLog *logstore.ScrubbingStore) {
	t.Helper()
	store := newTokenStore()
	tmgr, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	aStore := newAuditStore()
	aLog := audit.NewLogger(aStore, 0)

	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(js)
	s := api.NewServer(adapter, nil, nil, aLog, tmgr, nil, nil, nil)

	raw := logstore.NewMemLogStore()
	// Same secretsLookup as main.go: materialises Env[k] for
	// each SecretKey on the job.
	lookup := func(jobID string) ([]string, bool) {
		j, err := js.Get(jobID)
		if err != nil || len(j.SecretKeys) == 0 {
			return nil, false
		}
		out := make([]string, 0, len(j.SecretKeys))
		for _, k := range j.SecretKeys {
			if v, ok := j.Env[k]; ok && v != "" {
				out = append(out, v)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}
	scrub := logstore.NewScrubbingStore(raw, lookup)
	s.SetLogStore(scrub)

	return s, tmgr, js, raw, scrub
}

func tokenForLogs(t *testing.T, tm *auth.TokenManager, subject, role string) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), subject, role, time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return tok
}

// ── Write-path scrubbing ────────────────────────────────────

func TestLogs_ScrubbingStore_RedactsSecretOnAppend(t *testing.T) {
	srv, tm, cs, raw, scrub := logsFixture(t)
	aliceTok := tokenForLogs(t, tm, "alice", "user")

	// Submit a job declaring HF_TOKEN as a secret.
	body := `{"id":"jl-1","command":"echo","env":{"HF_TOKEN":"hf_sekret"},"secret_keys":["HF_TOKEN"]}`
	rr := doWithToken(srv, "POST", "/jobs", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	// Append a chunk that carries the secret value.
	err := scrub.Append(context.Background(), logstore.LogEntry{
		JobID: "jl-1",
		Seq:   1,
		Data:  "ERROR: auth failed for token=hf_sekret",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// The inner raw store must NOT contain the plaintext.
	entries, _ := raw.Get(context.Background(), "jl-1")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry in raw, got %d", len(entries))
	}
	if strings.Contains(entries[0].Data, "hf_sekret") {
		t.Fatalf("raw store leaked plaintext: %q", entries[0].Data)
	}
	if !strings.Contains(entries[0].Data, "[REDACTED]") {
		t.Fatalf("raw store not redacted: %q", entries[0].Data)
	}

	// Verify the job still has the plaintext env in memory —
	// the scrub is log-specific; dispatch-time env is
	// unchanged.
	j, _ := cs.Get("jl-1")
	if j.Env["HF_TOKEN"] != "hf_sekret" {
		t.Errorf("job env should preserve plaintext for dispatch, got %q", j.Env["HF_TOKEN"])
	}
}

// ── Response-path redactor ──────────────────────────────────

// Belt-and-braces: simulate a chunk that landed verbatim
// (e.g. before the decorator was wired in a rolling deploy)
// by writing directly to the inner raw store. GET /jobs/{id}/
// logs must still redact.
func TestLogs_ResponsePath_RedactsLegacyChunks(t *testing.T) {
	srv, tm, _, raw, _ := logsFixture(t)
	aliceTok := tokenForLogs(t, tm, "alice", "user")

	// Submit with secret.
	body := `{"id":"jl-2","command":"echo","env":{"HF_TOKEN":"hf_sekret"},"secret_keys":["HF_TOKEN"]}`
	rr := doWithToken(srv, "POST", "/jobs", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	// Write DIRECTLY to the raw inner store (bypasses the
	// ScrubbingStore) — simulates a legacy chunk.
	err := raw.Append(context.Background(), logstore.LogEntry{
		JobID: "jl-2",
		Seq:   1,
		Data:  "leaked: hf_sekret on stdout",
	})
	if err != nil {
		t.Fatalf("direct Append: %v", err)
	}

	// GET /jobs/{id}/logs — response must redact.
	rr = doWithToken(srv, "GET", "/jobs/jl-2/logs", "", aliceTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rr.Code, rr.Body.String())
	}
	body2 := rr.Body.String()
	if strings.Contains(body2, "hf_sekret") {
		t.Fatalf("response leaked plaintext: %s", body2)
	}
	if !strings.Contains(body2, "[REDACTED]") {
		t.Fatalf("response not redacted: %s", body2)
	}
}

// ── End-to-end: submit, stream-equivalent append, GET ───────

func TestLogs_EndToEnd_SubmitChunkGet(t *testing.T) {
	srv, tm, _, _, scrub := logsFixture(t)
	aliceTok := tokenForLogs(t, tm, "alice", "user")

	body := `{"id":"jl-3","command":"echo","env":{"HF_TOKEN":"hf_sekret","OTHER":"notasecret"},"secret_keys":["HF_TOKEN"]}`
	rr := doWithToken(srv, "POST", "/jobs", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	// Two chunks — one with the secret, one without.
	for i, data := range []string{
		"prep: starting up",
		"credentials: using hf_sekret for HF api",
	} {
		err := scrub.Append(context.Background(), logstore.LogEntry{
			JobID: "jl-3",
			Seq:   uint64(i + 1),
			Data:  data,
		})
		if err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	rr = doWithToken(srv, "GET", "/jobs/jl-3/logs", "", aliceTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		JobID   string              `json:"job_id"`
		Entries []logstore.LogEntry `json:"entries"`
		Total   int                 `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total: want 2, got %d", resp.Total)
	}
	// First entry unchanged.
	if resp.Entries[0].Data != "prep: starting up" {
		t.Errorf("chunk 0 mutated: %q", resp.Entries[0].Data)
	}
	// Second entry redacted.
	if strings.Contains(resp.Entries[1].Data, "hf_sekret") {
		t.Errorf("chunk 1 leaked: %q", resp.Entries[1].Data)
	}
	if !strings.Contains(resp.Entries[1].Data, "[REDACTED]") {
		t.Errorf("chunk 1 not redacted: %q", resp.Entries[1].Data)
	}
	// The non-secret "OTHER=notasecret" env must be left
	// alone — a declared-plain env value is not a
	// substitution candidate, and its bytes appearing in a
	// log line must not be confused with a secret value.
	// (Not checked in the payload here since the test data
	// doesn't echo it, but the assertion that only
	// hf_sekret got swapped exercises the same property.)
}

// ── Job with no secrets: no allocation / no rewrite ─────────

func TestLogs_NoSecrets_PassesThroughVerbatim(t *testing.T) {
	srv, tm, _, raw, scrub := logsFixture(t)
	aliceTok := tokenForLogs(t, tm, "alice", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"jl-4","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	original := "nothing secret about any of this output"
	err := scrub.Append(context.Background(), logstore.LogEntry{
		JobID: "jl-4", Seq: 1, Data: original,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	entries, _ := raw.Get(context.Background(), "jl-4")
	if len(entries) != 1 || entries[0].Data != original {
		t.Errorf("verbatim passthrough broken: %+v", entries)
	}
}

// ── RBAC gate on /jobs/{id}/logs ────────────────────────────

// Feature 37 regression guard — the log-read endpoint must
// honour the same authz.ActionRead check as GET /jobs/{id}.
// Before feature 37 this endpoint had no per-job check at
// all, so any authenticated caller could fetch any job's
// logs.
func TestLogs_RBAC_NonOwnerForbidden(t *testing.T) {
	srv, tm, _, _, scrub := logsFixture(t)
	aliceTok := tokenForLogs(t, tm, "alice", "user")
	bobTok := tokenForLogs(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"jl-rbac","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	_ = scrub.Append(context.Background(), logstore.LogEntry{
		JobID: "jl-rbac", Seq: 1, Data: "hello",
	})

	// Alice — owner — reads successfully.
	rr = doWithToken(srv, "GET", "/jobs/jl-rbac/logs", "", aliceTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner GET: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Bob — non-owner — forbidden.
	rr = doWithToken(srv, "GET", "/jobs/jl-rbac/logs", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner GET: want 403, got %d", rr.Code)
	}
}

// ── Ensure cpb.Job is referenced so the import isn't dead ───

var _ = cpb.Job{}
