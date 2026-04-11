// cmd/helion-run/main.go
//
// helion-run submits a job to the Helion coordinator and prints the job ID
// and initial status to stdout.
//
// Usage
// ─────
//   helion-run <command> [args...]
//
// Environment
// ───────────
//   HELION_COORDINATOR   HTTP address of the coordinator API (required).
//                        Example: http://127.0.0.1:8080
//   HELION_TOKEN         JWT bearer token (required when coordinator has auth enabled).
//   HELION_JOB_ID        Stable job ID to use instead of generating a random one.
//                        Set this to a fixed value when retrying after a network
//                        failure — the coordinator deduplicates by job ID and will
//                        return 409 if the job was already accepted, letting the
//                        caller safely retry without creating a duplicate.
//
// Exit codes
// ──────────
//   0   job submitted successfully
//   1   usage error or submission failure
//
// Example
// ───────
//   $ HELION_COORDINATOR=http://127.0.0.1:8080 helion-run echo hello
//   job_id=job-1744321200-a3f2 status=pending

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// submitRequest mirrors api.SubmitRequest — duplicated here so cmd/ has no
// import cycle through internal/api.
type submitRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// jobResponse mirrors api.JobResponse for the fields helion-run cares about.
type jobResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "helion-run: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: helion-run <command> [args...]\n" +
			"  HELION_COORDINATOR must be set to the coordinator HTTP address")
	}

	coordAddr := os.Getenv("HELION_COORDINATOR")
	if coordAddr == "" {
		return fmt.Errorf("HELION_COORDINATOR environment variable is not set")
	}
	coordAddr = strings.TrimRight(coordAddr, "/")

	// AUDIT L5 (fixed): HELION_JOB_ID allows callers to supply a stable job ID
	// for idempotent retries. Without this, a network failure after the server
	// accepted the job but before the client received the response would produce
	// a duplicate job on retry. With a stable ID the coordinator returns 409 on
	// the retry instead of creating a second job.
	jobID := os.Getenv("HELION_JOB_ID")
	if jobID == "" {
		jobID = generateID()
	}
	req := submitRequest{
		ID:      jobID,
		Command: args[0],
		Args:    args[1:],
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := coordAddr + "/jobs"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("HELION_TOKEN"); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	var result jobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response (HTTP %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("coordinator returned HTTP %d: %s", resp.StatusCode, result.Error)
	}

	fmt.Printf("job_id=%s status=%s\n", result.ID, result.Status)
	return nil
}

// generateID produces a unique job ID of the form job-{unix_sec}-{rand4hex}.
// The timestamp prefix makes IDs sortable and debuggable.
// Falls back to a timestamp-only ID if crypto/rand is unavailable (extremely rare).
func generateID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// AUDIT M5 (fixed): previously this path called panic(), crashing the
		// process. Now it warns to stderr and falls back to a timestamp-only ID
		// so the CLI remains operational even on platforms where crypto/rand is
		// temporarily unavailable. Entropy is reduced but the process continues.
		fmt.Fprintf(os.Stderr, "helion-run: warning: crypto/rand unavailable, ID entropy reduced: %v\n", err)
		return fmt.Sprintf("job-%d-0000", time.Now().Unix())
	}
	return fmt.Sprintf("job-%d-%04x", time.Now().Unix(), b)
}
