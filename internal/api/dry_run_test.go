// internal/api/dry_run_test.go

package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
)

func TestParseDryRunParam_TableCases(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantDryRun bool
		wantErr    bool
	}{
		// Absent — real-submit path.
		{"absent", "", false, false},

		// Truthy.
		{"1",        "dry_run=1",    true, false},
		{"true",     "dry_run=true", true, false},
		{"TRUE",     "dry_run=TRUE", true, false},
		{"True",     "dry_run=True", true, false},
		{"yes",      "dry_run=yes",  true, false},
		{"YES",      "dry_run=YES",  true, false},

		// Falsy.
		{"0",        "dry_run=0",     false, false},
		{"false",    "dry_run=false", false, false},
		{"FALSE",    "dry_run=FALSE", false, false},
		{"no",       "dry_run=no",    false, false},
		{"empty",    "dry_run=",      false, false},

		// Invalid — must 400, NOT silently fall through to real
		// submit. This is the regression guard: a typo like
		// `?dry_run=yees` would otherwise execute an unintended
		// real submission.
		//
		// Note: Go 1.17+ no longer accepts `;` as a query
		// separator AND Go 1.26 treats a `;` in the query string
		// as fatal — the whole Query() map comes back empty. So
		// an attempt like `?dry_run=1;DROP` never reaches this
		// parser at all; net/url already discarded it. That's
		// fine security-wise: the caller gets the absent-query
		// treatment (false, nil) → real-submit path runs under
		// normal validators. We don't need a test case for it.
		{"typo",     "dry_run=yees",   false, true},
		{"maybe",    "dry_run=maybe",  false, true},
		{"numberish", "dry_run=2",     false, true},
		{"bool-ish", "dry_run=True1",  false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/test?"+tc.query, nil)
			got, err := api.ParseDryRunParam(r)
			if tc.wantErr && err == nil {
				t.Fatalf("ParseDryRunParam(%q) expected error, got nil", tc.query)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ParseDryRunParam(%q) unexpected error: %v", tc.query, err)
			}
			if got != tc.wantDryRun {
				t.Errorf("ParseDryRunParam(%q) = %v, want %v", tc.query, got, tc.wantDryRun)
			}
		})
	}
}

func TestParseDryRunParam_ErrorMessageIsDescriptive(t *testing.T) {
	// The error message has to be clear enough that an operator
	// running curl can diagnose the 400 without reading source.
	// Regression guard: a bare "invalid value" would be useless.
	r := httptest.NewRequest(http.MethodPost, "/test?dry_run=maybe", nil)
	_, err := api.ParseDryRunParam(r)
	if err == nil {
		t.Fatal("expected error for unrecognised dry_run value")
	}
	if !strings.Contains(err.Error(), "dry_run") {
		t.Errorf("error must mention dry_run: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "maybe") {
		t.Errorf("error must echo the offending value: %q", err.Error())
	}
}

func TestParseDryRunParam_AbsentQueryString(t *testing.T) {
	// No query string at all — not just "?dry_run=". Both paths
	// must return (false, nil); the caller hits the real submit
	// path either way.
	r := httptest.NewRequest(http.MethodPost, "/test", nil)
	got, err := api.ParseDryRunParam(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("ParseDryRunParam should return false when dry_run absent")
	}
}
