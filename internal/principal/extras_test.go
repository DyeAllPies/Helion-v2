// internal/principal/extras_test.go
//
// Coverage-focused tests for the helpers that the main
// principal_test.go suite doesn't exercise: SubjectFromID
// (used for feature-36 load-time backfill on job SubmittedBy
// compatibility) and the internal stringsIndexByte helper.

package principal_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

func TestSubjectFromID_StripsKindPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"user:alice", "alice"},
		{"operator:alice@ops", "alice@ops"},
		{"node:n-7", "n-7"},
		{"service:svc-prober", "svc-prober"},
		{"user:alice:with:colons", "alice:with:colons"}, // only strip first
	}
	for _, c := range cases {
		if got := principal.SubjectFromID(c.in); got != c.want {
			t.Errorf("SubjectFromID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubjectFromID_EmptyAndAnonymous_ReturnEmpty(t *testing.T) {
	// These are the two "there is no subject" sentinels; caller
	// relies on a zero string to distinguish "no user" from
	// "user named 'anonymous'".
	if got := principal.SubjectFromID(""); got != "" {
		t.Errorf("empty ID: got %q, want %q", got, "")
	}
	if got := principal.SubjectFromID("anonymous"); got != "" {
		t.Errorf("anonymous ID: got %q, want %q", got, "")
	}
}

func TestSubjectFromID_NoColon_PassesThrough(t *testing.T) {
	// Back-compat: test fixtures and legacy records sometimes
	// use a bare subject with no Kind prefix.
	if got := principal.SubjectFromID("plain-subject"); got != "plain-subject" {
		t.Errorf("no-colon: got %q, want plain-subject", got)
	}
}

func TestSubjectFromID_LegacyOwnerID_EmptySuffix(t *testing.T) {
	// LegacyOwnerID is "legacy:" with nothing after — SubjectFromID
	// returns the empty suffix. Feature 37's policy evaluator
	// treats this as admin-only.
	if got := principal.SubjectFromID(principal.LegacyOwnerID); got != "" {
		t.Errorf("LegacyOwnerID suffix: got %q, want empty", got)
	}
}
