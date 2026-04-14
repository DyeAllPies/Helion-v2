package api

import (
	"strings"
	"testing"
)

func baseReq() *SubmitRequest {
	return &SubmitRequest{ID: "j1", Command: "true"}
}

// ── artifact name ───────────────────────────────────────────────────────────

func TestValidateArtifactName(t *testing.T) {
	good := []string{"A", "DATA", "TRAIN_1", "FOO_BAR_BAZ", "X_Y_Z"}
	for _, n := range good {
		if !validateArtifactName(n) {
			t.Errorf("expected ok: %q", n)
		}
	}
	bad := []string{
		"", "lower", "0START", "with space", "with-dash",
		"dot.sep", "$shell", strings.Repeat("A", maxArtifactNameLen+1),
	}
	for _, n := range bad {
		if validateArtifactName(n) {
			t.Errorf("expected reject: %q", n)
		}
	}
}

// ── local path ───────────────────────────────────────────────────────────

func TestValidateLocalPath(t *testing.T) {
	okCases := []string{"a", "a/b", "a/b/c.txt", "in/train.parquet"}
	for _, p := range okCases {
		if msg := validateLocalPath(p); msg != "" {
			t.Errorf("%q: unexpected: %s", p, msg)
		}
	}
	badCases := []string{
		"", "/abs", "a/..", "../esc", "a/./b", "a//b",
		"a\\b", "has\x00nul",
		strings.Repeat("a", maxArtifactLocalPath+1),
	}
	for _, p := range badCases {
		if msg := validateLocalPath(p); msg == "" {
			t.Errorf("%q: expected rejection", p)
		}
	}
}

// ── bindings (inputs/outputs) ────────────────────────────────────────────

func TestValidateSubmitRequest_InputsHappyPath(t *testing.T) {
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{
		{Name: "TRAIN", URI: "s3://b/x", LocalPath: "in/train.parquet"},
		{Name: "VAL", URI: "file:///tmp/v", LocalPath: "in/val.parquet"},
	}
	req.Outputs = []ArtifactBindingRequest{
		{Name: "MODEL", LocalPath: "out/model.pt"},
	}
	if msg := validateSubmitRequest(req); msg != "" {
		t.Fatalf("expected ok, got: %s", msg)
	}
}

func TestValidateSubmitRequest_InputRequiresURI(t *testing.T) {
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{{Name: "X", LocalPath: "in/x"}}
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "uri is required") {
		t.Fatalf("unexpected: %q", msg)
	}
}

func TestValidateSubmitRequest_OutputMustNotHaveURI(t *testing.T) {
	req := baseReq()
	req.Outputs = []ArtifactBindingRequest{
		{Name: "X", URI: "file:///already", LocalPath: "out/x"},
	}
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "must be empty on submit") {
		t.Fatalf("unexpected: %q", msg)
	}
}

func TestValidateSubmitRequest_DuplicateInputName(t *testing.T) {
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{
		{Name: "DATA", URI: "s3://b/a", LocalPath: "a"},
		{Name: "DATA", URI: "s3://b/b", LocalPath: "b"},
	}
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "duplicate name") {
		t.Fatalf("unexpected: %q", msg)
	}
}

func TestValidateSubmitRequest_SameNameAcrossInputOutputIsFine(t *testing.T) {
	// HELION_INPUT_X and HELION_OUTPUT_X are disjoint env vars, so sharing
	// a logical name between the two directions is intentional.
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{{Name: "X", URI: "s3://b/x", LocalPath: "in/x"}}
	req.Outputs = []ArtifactBindingRequest{{Name: "X", LocalPath: "out/x"}}
	if msg := validateSubmitRequest(req); msg != "" {
		t.Fatalf("expected ok: %s", msg)
	}
}

func TestValidateSubmitRequest_TooManyBindings(t *testing.T) {
	req := baseReq()
	for i := 0; i <= maxArtifactBindings; i++ {
		req.Inputs = append(req.Inputs, ArtifactBindingRequest{
			Name:      "I",
			URI:       "s3://b/x",
			LocalPath: "a",
		})
	}
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "inputs must not exceed") {
		t.Fatalf("unexpected: %q", msg)
	}
}

func TestValidateSubmitRequest_OversizeURI(t *testing.T) {
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{{
		Name: "X", URI: strings.Repeat("u", maxArtifactURILen+1), LocalPath: "a",
	}}
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "uri must not exceed") {
		t.Fatalf("unexpected: %q", msg)
	}
}

func TestValidateSubmitRequest_NULInURI(t *testing.T) {
	req := baseReq()
	req.Inputs = []ArtifactBindingRequest{{
		Name: "X", URI: "s3://b/\x00x", LocalPath: "a",
	}}
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "must not contain NUL") {
		t.Fatalf("unexpected: %q", msg)
	}
}

// ── working_dir ──────────────────────────────────────────────────────────

func TestValidateSubmitRequest_WorkingDir(t *testing.T) {
	req := baseReq()
	req.WorkingDir = strings.Repeat("w", maxWorkingDirLen+1)
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "working_dir must not exceed") {
		t.Fatalf("unexpected: %q", msg)
	}
	req.WorkingDir = "has\x00nul"
	if msg := validateSubmitRequest(req); !strings.Contains(msg, "working_dir must not contain NUL") {
		t.Fatalf("unexpected: %q", msg)
	}
}

// ── node_selector ────────────────────────────────────────────────────────

func TestValidateNodeSelector(t *testing.T) {
	okCases := []map[string]string{
		nil,
		{},
		{"gpu": "a100"},
		{"gpu": "a100", "cuda": "12.4"},
	}
	for _, s := range okCases {
		if msg := validateNodeSelector(s); msg != "" {
			t.Errorf("%v: %s", s, msg)
		}
	}

	tooMany := map[string]string{}
	for i := 0; i <= maxNodeSelectorEntries; i++ {
		tooMany[string(rune('a'+i))+"k"] = "v"
	}
	if msg := validateNodeSelector(tooMany); !strings.Contains(msg, "must not exceed") {
		t.Fatalf("unexpected: %q", msg)
	}
	if msg := validateNodeSelector(map[string]string{"": "v"}); !strings.Contains(msg, "keys must be") {
		t.Fatalf("unexpected: %q", msg)
	}
	if msg := validateNodeSelector(map[string]string{"k=bad": "v"}); !strings.Contains(msg, "'=' or NUL") {
		t.Fatalf("unexpected: %q", msg)
	}
	if msg := validateNodeSelector(map[string]string{"k": strings.Repeat("v", maxNodeSelectorValLen+1)}); !strings.Contains(msg, "values must not exceed") {
		t.Fatalf("unexpected: %q", msg)
	}
}

// ── conversion ───────────────────────────────────────────────────────────

func TestConvertBindings(t *testing.T) {
	if convertBindings(nil) != nil {
		t.Fatal("nil should convert to nil")
	}
	if convertBindings([]ArtifactBindingRequest{}) != nil {
		t.Fatal("empty should convert to nil")
	}
	in := []ArtifactBindingRequest{
		{Name: "A", URI: "s3://b/a", LocalPath: "in/a"},
	}
	out := convertBindings(in)
	if len(out) != 1 || out[0].Name != "A" || out[0].URI != "s3://b/a" || out[0].LocalPath != "in/a" {
		t.Fatalf("conversion: %+v", out)
	}
}
