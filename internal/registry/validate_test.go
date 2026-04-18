package registry

import (
	"math"
	"strings"
	"testing"
)

// ── Name ────────────────────────────────────────────────────────────────

func TestValidateName(t *testing.T) {
	ok := []string{"iris", "iris-v2", "team.ml.iris", "a", "0-9", "under_score"}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("want ok for %q: %v", n, err)
		}
	}
	bad := []string{
		"", "UPPER", "has space", "has/slash", "has:colon",
		"has\x00nul", strings.Repeat("a", maxNameLen+1),
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("expected reject for %q", n)
		}
	}
}

// ── Version ─────────────────────────────────────────────────────────────

func TestValidateVersion(t *testing.T) {
	ok := []string{
		"v1.0.0", "1.2.3-rc.1", "2026-04-14",
		"git-abc1234", "0.1.0+build.5", "Latest",
	}
	for _, v := range ok {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("want ok for %q: %v", v, err)
		}
	}
	bad := []string{"", "has space", "has/slash", "has\x00nul", strings.Repeat("a", maxVersionLen+1)}
	for _, v := range bad {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("expected reject for %q", v)
		}
	}
}

// ── URI ────────────────────────────────────────────────────────────────

func TestValidateURI(t *testing.T) {
	ok := []string{
		"file:///var/lib/helion/artifacts/x",
		"s3://helion/jobs/train-1/out/model.pt",
	}
	for _, u := range ok {
		if err := ValidateURI(u); err != nil {
			t.Errorf("want ok for %q: %v", u, err)
		}
	}
	bad := []string{
		"",                           // empty
		"http://evil/x",              // wrong scheme
		"https://evil/x",             // wrong scheme
		"ftp://x",                    // wrong scheme
		"relative/path",              // no scheme
		"s3://bucket/has\x00nul",     // control byte
		"s3://bucket/has\nnewline",   // control byte
		"s3://" + strings.Repeat("a", maxURILen),
	}
	for _, u := range bad {
		if err := ValidateURI(u); err == nil {
			t.Errorf("expected reject for %q", u)
		}
	}
}

// ── Tags + Metrics ──────────────────────────────────────────────────────

func TestValidateTags(t *testing.T) {
	if err := ValidateTags(map[string]string{"env": "prod", "team": "ml"}); err != nil {
		t.Fatalf("happy path: %v", err)
	}
	// Too many entries.
	big := map[string]string{}
	for i := 0; i <= maxTagEntries; i++ {
		big[string(rune('a'+i))+"k"] = "v"
	}
	if err := ValidateTags(big); err == nil {
		t.Error("expected too-many-entries reject")
	}
	// = in key.
	if err := ValidateTags(map[string]string{"k=bad": "v"}); err == nil {
		t.Error("expected '=' in key reject")
	}
	// NUL in value.
	if err := ValidateTags(map[string]string{"k": "has\x00nul"}); err == nil {
		t.Error("expected NUL in value reject")
	}
}

func TestValidateMetrics(t *testing.T) {
	if err := ValidateMetrics(map[string]float64{"accuracy": 0.95, "f1": 0.87}); err != nil {
		t.Fatalf("happy: %v", err)
	}
	if err := ValidateMetrics(map[string]float64{"nan": math.NaN()}); err == nil {
		t.Error("expected NaN reject")
	}
	if err := ValidateMetrics(map[string]float64{"inf": math.Inf(1)}); err == nil {
		t.Error("expected +Inf reject")
	}
	if err := ValidateMetrics(map[string]float64{"inf": math.Inf(-1)}); err == nil {
		t.Error("expected -Inf reject")
	}
	if err := ValidateMetrics(map[string]float64{"": 1}); err == nil {
		t.Error("expected empty-key reject")
	}
}

// TestValidateMetrics_FiniteExtremesAccepted pins the boundary
// between "finite, round-trips through JSON" and "infinite, breaks
// JSON". The validator's comment promises to reject only NaN and
// ±Inf; an earlier implementation used `v > 1e308 || v < -1e308`
// which spuriously rejected finite metrics above 1e308 (MaxFloat64
// ≈ 1.798e308 falls in that window). Fix switched to math.IsInf.
// Without this test, a refactor that reintroduced the threshold
// check would slip past — all existing metric tests use values
// in [-1, 1].
func TestValidateMetrics_FiniteExtremesAccepted(t *testing.T) {
	cases := map[string]float64{
		"max":  math.MaxFloat64,
		"nmax": -math.MaxFloat64,
		"big":  1.5e308, // between 1e308 and MaxFloat64 — the regression window
	}
	if err := ValidateMetrics(cases); err != nil {
		t.Fatalf("finite extremes should validate: %v", err)
	}
}

// ── Composed validators ────────────────────────────────────────────────

func TestValidateDataset_HappyAndPartialLineage(t *testing.T) {
	ok := &Dataset{Name: "iris", Version: "v1", URI: "s3://b/k"}
	if err := ValidateDataset(ok); err != nil {
		t.Fatalf("happy: %v", err)
	}
	bad := &Dataset{Name: "iris", Version: "", URI: "s3://b/k"}
	if err := ValidateDataset(bad); err == nil {
		t.Fatal("missing version should fail")
	}
}

func TestValidateModel_FullLineage(t *testing.T) {
	m := &Model{
		Name:    "resnet",
		Version: "v0.1",
		URI:     "s3://b/jobs/train/out/model.pt",
		SourceDataset: DatasetRef{
			Name: "imagenet", Version: "v2",
		},
		Metrics: map[string]float64{"top1": 0.76},
	}
	if err := ValidateModel(m); err != nil {
		t.Fatalf("full lineage: %v", err)
	}
}

func TestValidateModel_PartialLineage_Rejected(t *testing.T) {
	// Name set, version missing — partial lineage is ambiguous;
	// reject so the audit record is never half-formed.
	m := &Model{
		Name:          "resnet",
		Version:       "v0.1",
		URI:           "s3://b/k",
		SourceDataset: DatasetRef{Name: "imagenet"},
	}
	if err := ValidateModel(m); err == nil {
		t.Fatal("partial lineage should fail")
	}
}

func TestValidateModel_NoLineage_OK(t *testing.T) {
	// Empty SourceDataset is legitimate — lineage not recorded.
	m := &Model{Name: "r", Version: "v1", URI: "s3://b/k"}
	if err := ValidateModel(m); err != nil {
		t.Fatalf("no lineage: %v", err)
	}
}

func TestValidateModel_Framework(t *testing.T) {
	m := &Model{Name: "r", Version: "v1", URI: "s3://b/k", Framework: "pytorch"}
	if err := ValidateModel(m); err != nil {
		t.Fatalf("framework: %v", err)
	}
	m.Framework = "has\x00nul"
	if err := ValidateModel(m); err == nil {
		t.Fatal("NUL in framework should fail")
	}
	m.Framework = strings.Repeat("a", maxFrameworkLen+1)
	if err := ValidateModel(m); err == nil {
		t.Fatal("oversize framework should fail")
	}
}
