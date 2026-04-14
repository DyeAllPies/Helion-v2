package grpcserver

import (
	"testing"

	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// outputsFromProto is unexported but lives inside this package, so the
// test can exercise it directly. Keeps the grpcserver → cluster proto
// conversion free of regressions as JobResult.Outputs evolves.

func TestOutputsFromProto_NilAndEmpty(t *testing.T) {
	if got := outputsFromProto(nil); got != nil {
		t.Errorf("nil in → nil out; got %+v", got)
	}
	if got := outputsFromProto([]*pb.ArtifactOutput{}); got != nil {
		t.Errorf("empty in → nil out; got %+v", got)
	}
}

func TestOutputsFromProto_SkipsNilEntries(t *testing.T) {
	in := []*pb.ArtifactOutput{
		nil,
		{Name: "MODEL", Uri: "s3://b/m", Size: 100, Sha256: "abc", LocalPath: "out/m"},
		nil,
	}
	got := outputsFromProto(in)
	if len(got) != 1 {
		t.Fatalf("nil entries should be skipped: %+v", got)
	}
	if got[0].Name != "MODEL" || got[0].URI != "s3://b/m" ||
		got[0].Size != 100 || got[0].SHA256 != "abc" || got[0].LocalPath != "out/m" {
		t.Fatalf("field mapping: %+v", got[0])
	}
}

func TestOutputsFromProto_PreservesOrder(t *testing.T) {
	in := []*pb.ArtifactOutput{
		{Name: "A", Uri: "s3://b/a"},
		{Name: "B", Uri: "s3://b/b"},
		{Name: "C", Uri: "s3://b/c"},
	}
	got := outputsFromProto(in)
	if len(got) != 3 {
		t.Fatalf("len: %d", len(got))
	}
	want := []string{"A", "B", "C"}
	for i, n := range want {
		if got[i].Name != n {
			t.Errorf("index %d: got %q want %q", i, got[i].Name, n)
		}
	}
}

// ── trust-boundary validation ───────────────────────────────────────────
//
// validateReportedOutput is the coordinator's defence against a
// compromised node attesting to artifacts that it never actually
// uploaded to the configured store. Exhaustively cover the rejection
// rules so a future refactor cannot silently relax them.

func TestValidateReportedOutput_HappyPath(t *testing.T) {
	ok := []*pb.ArtifactOutput{
		{Name: "MODEL", Uri: "s3://bucket/jobs/j/out/model.pt", LocalPath: "out/model.pt"},
		{Name: "M", Uri: "file:///var/lib/helion/artifacts/m"},
		{Name: "A_B_1", Uri: "s3://b/x", Size: 1024, Sha256: "deadbeef"},
	}
	for _, o := range ok {
		if r := validateReportedOutput(o); r != "" {
			t.Errorf("%+v rejected: %s", o, r)
		}
	}
}

func TestValidateReportedOutput_RejectsBadSchemes(t *testing.T) {
	schemes := []string{
		"http://evil.example.com/x",
		"https://evil.example.com/x",
		"ftp://x",
		"javascript:alert(1)",
		"gs://google-bucket/x",
		"data:text/plain,attack",
		"/absolute/path",
		"not a uri",
		"",
	}
	for _, u := range schemes {
		o := &pb.ArtifactOutput{Name: "X", Uri: u}
		if validateReportedOutput(o) == "" {
			t.Errorf("scheme %q accepted; should be rejected", u)
		}
	}
}

func TestValidateReportedOutput_RejectsBadNames(t *testing.T) {
	names := []string{"", "lower", "with space", "0LEAD", "has-dash", "with.dot"}
	for _, n := range names {
		o := &pb.ArtifactOutput{Name: n, Uri: "s3://b/x"}
		if validateReportedOutput(o) == "" {
			t.Errorf("name %q accepted; should be rejected", n)
		}
	}
}

func TestValidateReportedOutput_RejectsOversizeFields(t *testing.T) {
	long := make([]byte, maxReportedURILen+1)
	for i := range long {
		long[i] = 'a'
	}
	o := &pb.ArtifactOutput{Name: "X", Uri: "s3://b/" + string(long)}
	if validateReportedOutput(o) == "" {
		t.Error("oversize URI accepted")
	}

	longName := make([]byte, maxReportedNameLen+1)
	for i := range longName {
		longName[i] = 'A'
	}
	o = &pb.ArtifactOutput{Name: string(longName), Uri: "s3://b/x"}
	if validateReportedOutput(o) == "" {
		t.Error("oversize name accepted")
	}
}

func TestValidateReportedOutput_RejectsNULAndControlBytes(t *testing.T) {
	bad := []string{"s3://b/\x00nul", "s3://b/has\nnewline", "s3://b/has\ttab"}
	for _, u := range bad {
		o := &pb.ArtifactOutput{Name: "X", Uri: u}
		if validateReportedOutput(o) == "" {
			t.Errorf("URI %q accepted; should be rejected", u)
		}
	}
}

func TestValidateReportedOutput_RejectsNegativeSize(t *testing.T) {
	o := &pb.ArtifactOutput{Name: "X", Uri: "s3://b/x", Size: -1}
	if validateReportedOutput(o) == "" {
		t.Error("negative size accepted")
	}
}

func TestOutputsFromProto_DropsInvalidEntries(t *testing.T) {
	in := []*pb.ArtifactOutput{
		{Name: "OK", Uri: "s3://b/ok"},
		{Name: "BAD_SCHEME", Uri: "http://evil/x"},
		{Name: "OK2", Uri: "file:///tmp/ok"},
		nil,
		{Name: "lowercase", Uri: "s3://b/x"}, // bad name
	}
	got := outputsFromProto(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 kept entries, got %d: %+v", len(got), got)
	}
	if got[0].Name != "OK" || got[1].Name != "OK2" {
		t.Fatalf("unexpected kept entries: %+v", got)
	}
}
