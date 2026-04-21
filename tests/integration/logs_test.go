// tests/integration/logs_test.go
//
// End-to-end tests for GET /jobs/{id}/logs endpoint.

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/logstore"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
)

func TestGetJobLogs_ReturnsStoredLogs(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	ls := logstore.NewBadgerLogStore(persister, 7*24*time.Hour)
	addr := freePort(t)

	jobsAdapter := api.NewJobStoreAdapter(jobs)
	rateLimiter := ratelimit.NewNodeLimiter()
	srv := api.NewServer(jobsAdapter, nil, nil, nil, nil, rateLimiter, nil, nil)
	srv.DisableAuth()
	srv.SetLogStore(ls)

	go func() {
		if err := srv.Serve(addr); err != nil && err != http.ErrServerClosed {
			t.Logf("API server: %v", err)
		}
	}()

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
		_ = srv.Shutdown(ctx)
	})

	// Submit a job and store some logs.
	submitJob(t, addr, "log-job-1", "echo", []string{"hello"})

	ctx := context.Background()
	_ = ls.Append(ctx, logstore.LogEntry{JobID: "log-job-1", Seq: 1, Data: "stdout line 1"})
	_ = ls.Append(ctx, logstore.LogEntry{JobID: "log-job-1", Seq: 2, Data: "stdout line 2"})

	// GET /jobs/log-job-1/logs
	resp, err := http.Get(fmt.Sprintf("http://%s/jobs/log-job-1/logs", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		JobID   string             `json:"job_id"`
		Entries []logstore.LogEntry `json:"entries"`
		Total   int                `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Total != 2 {
		t.Errorf("total = %d, want 2", result.Total)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].Data != "stdout line 1" {
		t.Errorf("first entry = %q, want 'stdout line 1'", result.Entries[0].Data)
	}

	t.Logf("GET /jobs/log-job-1/logs: %d entries", result.Total)
}

func TestGetJobLogs_TailParam(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	ls := logstore.NewBadgerLogStore(persister, 7*24*time.Hour)
	addr := freePort(t)

	jobsAdapter := api.NewJobStoreAdapter(jobs)
	srv := api.NewServer(jobsAdapter, nil, nil, nil, nil, ratelimit.NewNodeLimiter(), nil, nil)
	srv.DisableAuth()
	srv.SetLogStore(ls)

	go func() { _ = srv.Serve(addr) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ := http.Get("http://" + addr + "/healthz")
		if resp != nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Feature 37 authz — the log-read handler loads the Job record
	// first so it can authorise against the owner + share list. The
	// record has to exist before the GET, even though logs live in
	// a separate store.
	submitJob(t, addr, "tail-job", "echo", []string{"test"})

	ctx := context.Background()
	for i := 1; i <= 10; i++ {
		_ = ls.Append(ctx, logstore.LogEntry{JobID: "tail-job", Seq: uint64(i), Data: fmt.Sprintf("line %d", i)})
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/jobs/tail-job/logs?tail=3", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Entries []logstore.LogEntry `json:"entries"`
		Total   int                `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Total != 3 {
		t.Errorf("total = %d, want 3 (tail=3)", result.Total)
	}
}

func TestGetJobLogs_EmptyLogs(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	ls := logstore.NewBadgerLogStore(persister, 7*24*time.Hour)
	addr := freePort(t)

	jobsAdapter := api.NewJobStoreAdapter(jobs)
	srv := api.NewServer(jobsAdapter, nil, nil, nil, nil, ratelimit.NewNodeLimiter(), nil, nil)
	srv.DisableAuth()
	srv.SetLogStore(ls)

	go func() { _ = srv.Serve(addr) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ := http.Get("http://" + addr + "/healthz")
		if resp != nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Feature 37 authz — same requirement as the tail test: the
	// handler loads the Job record for ownership checks before
	// reaching the (empty) log store, so the record must exist.
	submitJob(t, addr, "no-logs-job", "echo", []string{"no-logs"})

	resp, err := http.Get(fmt.Sprintf("http://%s/jobs/no-logs-job/logs", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("total = %d, want 0", result.Total)
	}
}
