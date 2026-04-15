package grpcserver

import (
	"io"
	"log/slog"
	"testing"

	pb "github.com/DyeAllPies/Helion-v2/proto"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── prefix attestation ─────────────────────────────────────────────────
//
// Exercises uriBelongsToJob — the ML-threat-model defence against a
// compromised node attesting artifacts that don't live under its
// assigned job's key space.

func TestURIBelongsToJob_LocalAndS3(t *testing.T) {
	ok := []struct {
		uri, jobID, local string
	}{
		{"s3://bucket/jobs/job-42/out/model.pt", "job-42", "out/model.pt"},
		{"file:///var/lib/helion/artifacts/jobs/job-42/out/m", "job-42", "out/m"},
		{"s3://b/jobs/wf-ml/train/out/m", "wf-ml/train", "out/m"},
	}
	for _, c := range ok {
		if !uriBelongsToJob(c.uri, c.jobID, c.local) {
			t.Errorf("should accept: uri=%q job=%q local=%q", c.uri, c.jobID, c.local)
		}
	}
}

func TestURIBelongsToJob_RejectsForeignPrefixes(t *testing.T) {
	bad := []struct {
		uri, jobID, local string
	}{
		// Cross-job theft: node reports an artifact under another
		// job's key space.
		{"s3://bucket/jobs/other-job/stolen.pt", "my-job", "stolen.pt"},
		// Completely outside the jobs/ tree.
		{"s3://bucket/raw/model.pt", "my-job", "model.pt"},
		// Job-id prefix collision without the trailing slash.
		{"s3://bucket/jobs/my-jobSUFFIX/x", "my-job", "x"},
		// LocalPath mismatch — the URI carries the right job but a
		// different file than what the node's binding claimed.
		{"s3://bucket/jobs/my-job/something-else", "my-job", "declared"},
		// Empty job_id / local_path must never match.
		{"s3://bucket/jobs//x", "", "x"},
		{"s3://bucket/jobs/my-job/", "my-job", ""},
	}
	for _, c := range bad {
		if uriBelongsToJob(c.uri, c.jobID, c.local) {
			t.Errorf("should reject: uri=%q job=%q local=%q", c.uri, c.jobID, c.local)
		}
	}
}

// ── scheme + prefix combined ───────────────────────────────────────────

func TestAttestOutputs_DropsPrefixMismatch(t *testing.T) {
	// Build a minimal *Server — attestOutputs only reads s.log and
	// s.audit (the latter is nil-checked).
	s := &Server{log: silentLogger()}

	in := []*pb.ArtifactOutput{
		{Name: "OK", Uri: "s3://b/jobs/job-x/out/a", LocalPath: "out/a"},
		{Name: "CROSS", Uri: "s3://b/jobs/other-job/out/b", LocalPath: "out/b"},
		{Name: "NONE", Uri: "s3://b/direct/c", LocalPath: "c"},
		// Job matches but local_path doesn't — node attested a
		// different file than it declared.
		{Name: "LYING", Uri: "s3://b/jobs/job-x/out/other", LocalPath: "out/declared"},
	}
	// declaredNames=nil keeps this test focused on the prefix-mismatch
	// path; the new declared-Names cross-check has its own dedicated
	// test in report_result_test.go.
	got := s.attestOutputs("job-x", "node-1", in, nil)
	if len(got) != 1 || got[0].Name != "OK" {
		t.Fatalf("expected only OK to survive attestation: %+v", got)
	}
}

func TestAttestOutputs_TruncatesAboveCap(t *testing.T) {
	s := &Server{log: silentLogger()}
	many := make([]*pb.ArtifactOutput, maxReportedOutputs+5)
	for i := range many {
		many[i] = &pb.ArtifactOutput{
			Name:      "X",
			Uri:       "s3://b/jobs/j/out/x",
			LocalPath: "out/x",
		}
	}
	got := s.attestOutputs("j", "n", many, nil)
	if len(got) > maxReportedOutputs {
		t.Fatalf("attestOutputs returned %d > cap %d", len(got), maxReportedOutputs)
	}
}
