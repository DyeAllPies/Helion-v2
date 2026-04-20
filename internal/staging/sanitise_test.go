// internal/staging/sanitise_test.go
//
// Pure-function tests for sanitiseJobDirName — the defensive
// path-normalisation helper that guards against a malicious
// job_id reaching the filesystem layer with shell metacharacters
// or path-traversal bytes.

package staging

import "testing"

func TestSanitiseJobDirName_AlphanumericPreserved(t *testing.T) {
	got := sanitiseJobDirName("job-abc_123", "")
	if got != "job-abc_123" {
		t.Errorf("alphanum preserved: got %q", got)
	}
}

func TestSanitiseJobDirName_SpecialCharsReplaced(t *testing.T) {
	// Path separators, shell metacharacters, and NUL all collapse
	// to '_'. No byte outside the safe-alphabet makes it through.
	got := sanitiseJobDirName("evil/../id$(rm -rf /)", "")
	for _, c := range got {
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.'
		if !ok {
			t.Errorf("unsafe byte %q leaked through sanitiser", c)
		}
	}
}

func TestSanitiseJobDirName_WithSuffix_Joined(t *testing.T) {
	got := sanitiseJobDirName("j1", "attempt2")
	if got != "j1-attempt2" {
		t.Errorf("suffix joined: got %q, want j1-attempt2", got)
	}
}

func TestSanitiseJobDirName_SuffixSanitisedIndependently(t *testing.T) {
	// The suffix goes through the same clean() pass — an
	// attacker-controlled suffix can't smuggle slashes either.
	got := sanitiseJobDirName("j1", "../evil")
	// Expect no slashes in output.
	for _, c := range got {
		if c == '/' || c == '\\' {
			t.Errorf("path separator leaked: %q", got)
		}
	}
}

func TestSanitiseJobDirName_EmptyInputs_FallbackName(t *testing.T) {
	// Empty job id + empty suffix → "job" fallback. Guards
	// against a coding error creating an unnamed directory.
	got := sanitiseJobDirName("", "")
	if got != "job" {
		t.Errorf("empty: got %q, want 'job'", got)
	}
}

func TestSanitiseJobDirName_AllSpecialChars_ReplacedToUnderscores(t *testing.T) {
	// Every char replaced, but still returns non-empty.
	got := sanitiseJobDirName("!@#$%", "")
	if got == "" {
		t.Error("empty result")
	}
	for _, c := range got {
		if c != '_' {
			t.Errorf("byte %q is not an underscore", c)
		}
	}
}

// ── safeJoin (boundary-case coverage) ────────────────────────

func TestSafeJoin_EmptyRel_Errors(t *testing.T) {
	_, err := safeJoin("/tmp/root", "")
	if err == nil {
		t.Error("empty rel must error")
	}
}

func TestSafeJoin_AbsolutePath_Errors(t *testing.T) {
	_, err := safeJoin("/tmp/root", "/etc/passwd")
	if err == nil {
		t.Error("absolute rel must error")
	}
}

func TestSafeJoin_BackslashPrefix_Errors(t *testing.T) {
	// Windows-style absolute paths rejected even on POSIX so
	// Docker images copied between platforms don't silently
	// evaluate a different path.
	_, err := safeJoin("/tmp/root", "\\windows\\path")
	if err == nil {
		t.Error("backslash-prefix rel must error")
	}
}

func TestSafeJoin_DriveLetter_Errors(t *testing.T) {
	_, err := safeJoin("/tmp/root", "C:foo")
	if err == nil {
		t.Error("drive-letter rel must error")
	}
}

func TestSafeJoin_NulByte_Errors(t *testing.T) {
	_, err := safeJoin("/tmp/root", "a\x00b")
	if err == nil {
		t.Error("NUL byte rel must error")
	}
}

func TestSafeJoin_TraversalEscape_Errors(t *testing.T) {
	_, err := safeJoin("/tmp/root", "../escape")
	if err == nil {
		t.Error("traversal must error")
	}
}

func TestSafeJoin_ValidRelative_Joined(t *testing.T) {
	got, err := safeJoin("/tmp/root", "subdir/file.txt")
	if err != nil {
		t.Fatalf("valid rel errored: %v", err)
	}
	// The joined path should sit under /tmp/root.
	if got == "" {
		t.Error("empty result")
	}
}
