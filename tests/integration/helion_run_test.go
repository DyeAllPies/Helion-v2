// tests/integration/helion_run_test.go
//
// End-to-end test for the helion-run CLI exit criterion (Phase 2 §7):
//
//   "helion-run echo hello returns job ID; job appears in BadgerDB
//    with correct status."
//
// What this test does
// ───────────────────
// 1. Starts the coordinator's HTTP API server in-process (no gRPC needed for
//    job submission — that is a separate concern).
// 2. Calls the helion-run submission logic directly (run() is package-internal,
//    so we duplicate the HTTP call here against the live server — same as
//    what the CLI binary does).
// 3. Reads the job back from the JobStore (which is backed by a real
//    BadgerDB on t.TempDir()) and asserts it is present with status=pending.
//
// Why no real binary exec
// ───────────────────────
// Building and exec-ing the binary in a test adds build-time dependency and
// cross-platform friction.  The exit criterion is "job appears in BadgerDB
// with correct status" — that is fully verified by hitting the HTTP server
// in-process with the same JSON the CLI sends.  The CLI binary itself is
// exercised by `go build ./...` in CI.

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// startAPIServer starts the HTTP API server in-process and returns its address.
// The server is shut down via t.Cleanup.
func startAPIServer(t *testing.T, jobs *cluster.JobStore) string {
	t.Helper()
	addr := freePort(t)
	
	// Phase 4: NewServer requires additional components
	// Wrap JobStore in adapter to provide paginated List method
	jobsAdapter := api.NewJobStoreAdapter(jobs)
	rateLimiter := ratelimit.NewNodeLimiter()
	srv := api.NewServer(jobsAdapter, nil, nil, nil, nil, rateLimiter)

	go func() {
		if err := srv.Serve(addr); err != nil && err != http.ErrServerClosed {
			t.Logf("API server: %v", err)
		}
	}()

	// Wait for the server to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Logf("API server shutdown: %v", err)
		}
	})

	return addr
}

// submitJob calls POST /jobs exactly as helion-run does.
func submitJob(t *testing.T, addr, jobID, command string, args []string) *api.JobResponse {
	t.Helper()

	body, err := json.Marshal(api.SubmitRequest{
		ID:      jobID,
		Command: command,
		Args:    args,
	})
	if err != nil {
		t.Fatalf("marshal submit request: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /jobs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&errResp); decErr == nil {
			t.Fatalf("POST /jobs returned HTTP %d: %s", resp.StatusCode, errResp.Error)
		}
		t.Fatalf("POST /jobs returned HTTP %d", resp.StatusCode)
	}

	var result api.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode job response: %v", err)
	}
	return &result
}

// ── TestHelionRun_SubmitJob_AppearInBadgerDB ──────────────────────────────────
//
// Phase 2 exit criterion:
//   "helion-run echo hello returns job ID; job appears in BadgerDB
//    with correct status."

func TestHelionRun_SubmitJob_AppearInBadgerDB(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir()

	// ── Start coordinator HTTP API backed by real BadgerDB ─────────────────
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() {
		if err := persister.Close(); err != nil {
			t.Errorf("close BadgerDB: %v", err)
		}
	})

	jobs := cluster.NewJobStore(persister, nil)
	addr := startAPIServer(t, jobs)

	// ── Submit a job (what helion-run does) ────────────────────────────────
	const jobID = "test-job-echo-hello"
	result := submitJob(t, addr, jobID, "echo", []string{"hello"})

	// CLI output: job_id=<id> status=<status>
	t.Logf("job_id=%s status=%s", result.ID, result.Status)

	if result.ID != jobID {
		t.Errorf("response job_id = %q, want %q", result.ID, jobID)
	}
	if result.Status != "pending" {
		t.Errorf("response status = %q, want %q", result.Status, "pending")
	}

	// ── Verify job is in the in-memory JobStore ────────────────────────────
	j, err := jobs.Get(jobID)
	if err != nil {
		t.Fatalf("JobStore.Get(%q): %v", jobID, err)
	}
	if j.Status != cpb.JobStatusPending {
		t.Errorf("JobStore status = %s, want pending", j.Status)
	}
	if j.Command != "echo" {
		t.Errorf("command = %q, want %q", j.Command, "echo")
	}
	if len(j.Args) != 1 || j.Args[0] != "hello" {
		t.Errorf("args = %v, want [hello]", j.Args)
	}

	// ── Verify job survives a BadgerDB close + reopen (crash recovery) ─────
	// This is the "appears in BadgerDB" part of the exit criterion —
	// the job must be durable, not just in-memory.
	if err := persister.Close(); err != nil {
		t.Fatalf("close BadgerDB pre-reopen: %v", err)
	}

	persister2, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("reopen BadgerDB: %v", err)
	}
	t.Cleanup(func() {
		if err := persister2.Close(); err != nil {
			t.Errorf("close BadgerDB2: %v", err)
		}
	})

	jobs2 := cluster.NewJobStore(persister2, nil)
	if err := jobs2.Restore(ctx); err != nil {
		t.Fatalf("Restore after reopen: %v", err)
	}

	j2, err := jobs2.Get(jobID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if j2.Status != cpb.JobStatusPending {
		t.Errorf("status after reopen = %s, want pending", j2.Status)
	}
	t.Logf("job %s confirmed in BadgerDB after close/reopen: status=%s", jobID, j2.Status)
}

// ── TestHelionRun_GetJob ──────────────────────────────────────────────────────

// TestHelionRun_GetJob verifies that GET /jobs/{id} returns the job after
// submission — the same read-back the CLI would do for a status query.
func TestHelionRun_GetJob(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() {
		if err := persister.Close(); err != nil {
			t.Logf("close BadgerDB: %v", err)
		}
	})

	jobs := cluster.NewJobStore(persister, nil)
	addr := startAPIServer(t, jobs)

	submitJob(t, addr, "get-test-job", "sleep", []string{"5"})

	resp, err := http.Get(fmt.Sprintf("http://%s/jobs/get-test-job", addr))
	if err != nil {
		t.Fatalf("GET /jobs/get-test-job: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /jobs/{id} returned HTTP %d, want 200", resp.StatusCode)
	}

	var result api.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.ID != "get-test-job" {
		t.Errorf("id = %q, want %q", result.ID, "get-test-job")
	}
	if result.Status != "pending" {
		t.Errorf("status = %q, want pending", result.Status)
	}
}

// ── TestHelionRun_NotFound ────────────────────────────────────────────────────

func TestHelionRun_NotFound(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() {
		if err := persister.Close(); err != nil {
			t.Logf("close BadgerDB: %v", err)
		}
	})

	jobs := cluster.NewJobStore(persister, nil)
	addr := startAPIServer(t, jobs)

	resp, err := http.Get(fmt.Sprintf("http://%s/jobs/does-not-exist", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ── TestHelionRun_MissingFields ───────────────────────────────────────────────

func TestHelionRun_MissingFields(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() {
		if err := persister.Close(); err != nil {
			t.Logf("close BadgerDB: %v", err)
		}
	})

	jobs := cluster.NewJobStore(persister, nil)
	addr := startAPIServer(t, jobs)

	cases := []struct {
		name string
		body string
	}{
		{"missing id", `{"command":"echo"}`},
		{"missing command", `{"id":"j1"}`},
		{"empty body", `{}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(
				"http://"+addr+"/jobs",
				"application/json",
				bytes.NewBufferString(tc.body),
			)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}
