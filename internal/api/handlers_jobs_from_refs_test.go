package api

import (
	"strings"
	"testing"
)

// ── validateArtifactFromShape ───────────────────────────────────────────

func TestValidateArtifactFromShape_Accepts(t *testing.T) {
	ok := []string{
		"preprocess.TRAIN",
		"job-1.MODEL",
		"nested/name.OUTPUT",
		"job.with.dots.OUT", // last dot splits upstream vs output
		"A.B",
	}
	for _, r := range ok {
		if msg := validateArtifactFromShape(r); msg != "" {
			t.Errorf("should accept %q: %s", r, msg)
		}
	}
}

func TestValidateArtifactFromShape_Rejects(t *testing.T) {
	bad := []string{
		"",
		"no-dot-here",
		".leading",
		"trailing.",
		"upper.lowercase", // output must match [A-Z_][A-Z0-9_]*
		"upstream.0LEAD",
		"upstream.with-dash",
		"upstream.has space",
		"upstream.has\x00nul",
	}
	for _, r := range bad {
		if msg := validateArtifactFromShape(r); msg == "" {
			t.Errorf("should reject %q", r)
		}
	}
}

func TestValidateArtifactFromShape_Oversize(t *testing.T) {
	long := strings.Repeat("a", maxArtifactFromLen) + ".OUT"
	if msg := validateArtifactFromShape(long); msg == "" {
		t.Fatal("oversize ref accepted")
	}
}

// ── SplitFromRef ────────────────────────────────────────────────────────

func TestSplitFromRef(t *testing.T) {
	cases := []struct {
		ref, wantU, wantO string
	}{
		{"preprocess.TRAIN", "preprocess", "TRAIN"},
		{"a.b.c.D", "a.b.c", "D"}, // splits at last dot
		{"", "", ""},
		{"no-dot", "", ""},
		{".leading", "", ""},
		{"trailing.", "", ""},
	}
	for _, c := range cases {
		u, o := SplitFromRef(c.ref)
		if u != c.wantU || o != c.wantO {
			t.Errorf("SplitFromRef(%q): got (%q,%q), want (%q,%q)",
				c.ref, u, o, c.wantU, c.wantO)
		}
	}
}

// ── plain-submit path rejects From ──────────────────────────────────────

func TestValidateSubmitRequest_PlainJobRejectsFrom(t *testing.T) {
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{
		{Name: "X", From: "upstream.OUT", LocalPath: "in/x"},
	}
	msg := validateSubmitRequest(req)
	if !strings.Contains(msg, "workflow-job inputs") {
		t.Fatalf("expected workflow-only rejection, got: %q", msg)
	}
}

// ── workflow path accepts From ──────────────────────────────────────────

func TestValidateArtifactBindingsCtx_WorkflowInputsAcceptFrom(t *testing.T) {
	bs := []ArtifactBindingRequest{
		{Name: "TRAIN", From: "preprocess.TRAIN_DATA", LocalPath: "in/train.parquet"},
	}
	if msg := validateArtifactBindingsCtx("inputs", bs, true, true); msg != "" {
		t.Fatalf("should accept From in workflow context: %s", msg)
	}
}

func TestValidateArtifactBindingsCtx_URIAndFromMutuallyExclusive(t *testing.T) {
	bs := []ArtifactBindingRequest{
		{Name: "X", URI: "s3://b/x", From: "upstream.OUT", LocalPath: "a"},
	}
	msg := validateArtifactBindingsCtx("inputs", bs, true, true)
	if !strings.Contains(msg, "mutually exclusive") {
		t.Fatalf("unexpected: %q", msg)
	}
}

func TestValidateArtifactBindingsCtx_InputRequiresOneOf(t *testing.T) {
	bs := []ArtifactBindingRequest{
		{Name: "X", LocalPath: "a"},
	}
	msg := validateArtifactBindingsCtx("inputs", bs, true, true)
	if !strings.Contains(msg, "one of uri or from is required") {
		t.Fatalf("unexpected: %q", msg)
	}
}

// Outputs must never take a From reference — the runtime assigns URIs.
func TestValidateArtifactBindingsCtx_OutputsRejectFrom(t *testing.T) {
	bs := []ArtifactBindingRequest{
		{Name: "MODEL", From: "upstream.X", LocalPath: "out/m"},
	}
	// allowFrom=true but requireURI=false — this is the outputs call shape.
	msg := validateArtifactBindingsCtx("outputs", bs, false, true)
	if !strings.Contains(msg, "only valid on workflow-job inputs") {
		t.Fatalf("unexpected: %q", msg)
	}
}
