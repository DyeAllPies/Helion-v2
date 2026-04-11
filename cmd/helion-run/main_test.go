// cmd/helion-run/main_test.go
//
// Tests for the helion-run CLI entry point. run(args) is exported for
// testing so each branch (usage error, env missing, network error,
// successful submit, 4xx response) can be exercised without spawning a
// process.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── run() usage and config errors ────────────────────────────────────────────

func TestRun_NoArgs_ReturnsUsageError(t *testing.T) {
	t.Setenv("HELION_COORDINATOR", "http://example.invalid")
	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("want usage error, got %v", err)
	}
}

func TestRun_MissingCoordinator_ReturnsError(t *testing.T) {
	t.Setenv("HELION_COORDINATOR", "")
	err := run([]string{"echo", "hello"})
	if err == nil || !strings.Contains(err.Error(), "HELION_COORDINATOR") {
		t.Errorf("want HELION_COORDINATOR error, got %v", err)
	}
}

func TestRun_InvalidCoordinatorURL_ReturnsError(t *testing.T) {
	// "http:// bad" has a space — http.NewRequest rejects it.
	t.Setenv("HELION_COORDINATOR", "http:// bad")
	err := run([]string{"echo", "hi"})
	if err == nil {
		t.Error("want error from invalid URL")
	}
}

func TestRun_NetworkError_ReturnsError(t *testing.T) {
	// Port 1 on localhost is typically unused — Do() fails.
	t.Setenv("HELION_COORDINATOR", "http://127.0.0.1:1")
	err := run([]string{"echo", "hi"})
	if err == nil {
		t.Error("want network error")
	}
}

// ── run() successful and failed server responses ────────────────────────────

func TestRun_Success_PrintsJobIDAndStatus(t *testing.T) {
	var gotBody submitRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)

		// Echo back a well-formed JobResponse.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(jobResponse{
			ID:     gotBody.ID,
			Status: "pending",
		})
	}))
	defer srv.Close()

	t.Setenv("HELION_COORDINATOR", srv.URL)
	t.Setenv("HELION_JOB_ID", "fixed-job-id-for-test")
	t.Setenv("HELION_TOKEN", "test-bearer-token")

	if err := run([]string{"echo", "hello", "world"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if gotBody.ID != "fixed-job-id-for-test" {
		t.Errorf("server saw ID %q, want fixed-job-id-for-test", gotBody.ID)
	}
	if gotBody.Command != "echo" {
		t.Errorf("server saw command %q, want echo", gotBody.Command)
	}
	if len(gotBody.Args) != 2 || gotBody.Args[0] != "hello" || gotBody.Args[1] != "world" {
		t.Errorf("server saw args %v, want [hello world]", gotBody.Args)
	}
}

func TestRun_Server4xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(jobResponse{Error: "bad request"})
	}))
	defer srv.Close()

	t.Setenv("HELION_COORDINATOR", srv.URL)
	t.Setenv("HELION_JOB_ID", "")
	err := run([]string{"echo", "x"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("want error containing 400, got %v", err)
	}
}

func TestRun_ServerReturnsUnparseableBody_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	t.Setenv("HELION_COORDINATOR", srv.URL)
	err := run([]string{"echo"})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("want decode error, got %v", err)
	}
}

func TestRun_StripsTrailingSlashFromCoordinator(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(jobResponse{ID: "id", Status: "pending"})
	}))
	defer srv.Close()

	// Trailing slash must be stripped so the final URL is "<srv>/jobs",
	// not "<srv>//jobs".
	t.Setenv("HELION_COORDINATOR", srv.URL+"/")
	if err := run([]string{"echo"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotURL != "/jobs" {
		t.Errorf("got path %q, want /jobs", gotURL)
	}
}

// ── generateID ────────────────────────────────────────────────────────────────

func TestGenerateID_FormatAndUniqueness(t *testing.T) {
	// crypto/rand.Read is virtually never going to fail in-process, so the
	// fallback branch stays uncovered here. Format: job-<unix>-<4hex>.
	ids := make(map[string]struct{}, 8)
	for i := 0; i < 8; i++ {
		id := generateID()
		if !strings.HasPrefix(id, "job-") {
			t.Errorf("id %q: missing 'job-' prefix", id)
		}
		parts := strings.Split(id, "-")
		if len(parts) != 3 {
			t.Errorf("id %q: want 3 '-'-separated parts, got %d", id, len(parts))
		}
		if len(parts[2]) != 4 {
			t.Errorf("id %q: want 4-hex suffix, got %d chars", id, len(parts[2]))
		}
		ids[id] = struct{}{}
	}
	// 8 back-to-back generateID calls in the same second should produce at
	// least two distinct IDs (2 bytes of entropy).
	if len(ids) < 2 {
		t.Errorf("generateID produced too few unique ids: %d/8", len(ids))
	}
}
