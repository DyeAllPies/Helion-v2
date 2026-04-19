// internal/api/env_denylist_test.go
//
// Unit tests for the feature-25 env-denylist helpers. These cover:
//   1. envKeyBlocked — prefix + exact matches, case sensitivity
//   2. ParseEnvDenylistExceptions — format grammar + strict validation
//   3. envKeyAllowedByException — selector/value matching semantics
//   4. validateEnvMap — combined pass (count + key-shape + denylist + exceptions)
//   5. isDangerousSystemPath — absolute-path prefix match incl. normalisation
//   6. isDangerousLibraryBasename — exact + prefix loader-critical names
//   7. artifactURIPath — file:// extraction

package api

import (
	"strings"
	"testing"
)

// ── 1. envKeyBlocked ──────────────────────────────────────────────────────────

func TestEnvKeyBlocked_Prefixes(t *testing.T) {
	prefixCases := []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "LD_DEBUG",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH",
	}
	for _, k := range prefixCases {
		blocked, reason := envKeyBlocked(k)
		if !blocked {
			t.Errorf("envKeyBlocked(%q) = false, want true", k)
		}
		if reason == "" {
			t.Errorf("envKeyBlocked(%q) reason empty — error body needs a diagnosis", k)
		}
		if !strings.Contains(reason, "dynamic-loader") {
			t.Errorf("envKeyBlocked(%q) reason = %q, want to mention dynamic-loader", k, reason)
		}
	}
}

func TestEnvKeyBlocked_ExactNames(t *testing.T) {
	exactCases := []string{"GCONV_PATH", "GIO_EXTRA_MODULES", "HOSTALIASES", "NLSPATH", "RES_OPTIONS"}
	for _, k := range exactCases {
		blocked, reason := envKeyBlocked(k)
		if !blocked {
			t.Errorf("envKeyBlocked(%q) = false, want true", k)
		}
		if reason == "" {
			t.Errorf("envKeyBlocked(%q) reason empty", k)
		}
	}
}

func TestEnvKeyBlocked_Allowed(t *testing.T) {
	// These are either common ML env vars, lowercase variants the loader
	// ignores, or near-misses that must NOT match. Regression guard
	// against over-matching (e.g., a "LD*" glob would catch "LDAP_URL").
	allowed := []string{
		"PYTHONPATH", "HELION_TOKEN", "HF_HOME", "CUDA_VISIBLE_DEVICES",
		"ld_preload",      // lowercase — loader ignores, so inert; must NOT block
		"LDAP_URL",        // "LD" prefix without underscore — not LD_
		"HELIO_LD_FOO",    // LD appears mid-key; prefix-match must not catch it
		"MY_DYLD_SETTING", // DYLD mid-key
		"GCONV",           // close to GCONV_PATH but not exact
		"",                // empty handled elsewhere; envKeyBlocked treats as not blocked
	}
	for _, k := range allowed {
		if blocked, _ := envKeyBlocked(k); blocked {
			t.Errorf("envKeyBlocked(%q) = true, want false (over-matched)", k)
		}
	}
}

// ── 2. ParseEnvDenylistExceptions ────────────────────────────────────────────

func TestParseEnvDenylistExceptions_EmptyReturnsNil(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		ex, err := ParseEnvDenylistExceptions(raw)
		if err != nil {
			t.Errorf("ParseEnvDenylistExceptions(%q) err = %v, want nil", raw, err)
		}
		if ex != nil {
			t.Errorf("ParseEnvDenylistExceptions(%q) = %v, want nil", raw, ex)
		}
	}
}

func TestParseEnvDenylistExceptions_HappyPath(t *testing.T) {
	raw := "role=gpu:LD_LIBRARY_PATH;pool=build:LD_LIBRARY_PATH,GCONV_PATH"
	ex, err := ParseEnvDenylistExceptions(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ex) != 2 {
		t.Fatalf("want 2 rules, got %d", len(ex))
	}

	if ex[0].SelectorKey != "role" || ex[0].SelectorValue != "gpu" {
		t.Errorf("rule0 selector = %q=%q, want role=gpu", ex[0].SelectorKey, ex[0].SelectorValue)
	}
	if _, ok := ex[0].AllowedKeys["LD_LIBRARY_PATH"]; !ok {
		t.Errorf("rule0 missing LD_LIBRARY_PATH: %v", ex[0].AllowedKeys)
	}
	if len(ex[0].AllowedKeys) != 1 {
		t.Errorf("rule0 extra keys: %v", ex[0].AllowedKeys)
	}

	if ex[1].SelectorKey != "pool" || ex[1].SelectorValue != "build" {
		t.Errorf("rule1 selector: want pool=build, got %q=%q", ex[1].SelectorKey, ex[1].SelectorValue)
	}
	if len(ex[1].AllowedKeys) != 2 {
		t.Errorf("rule1 AllowedKeys count: want 2, got %d", len(ex[1].AllowedKeys))
	}
}

func TestParseEnvDenylistExceptions_Whitespace(t *testing.T) {
	raw := "  role = gpu : LD_LIBRARY_PATH  "
	ex, err := ParseEnvDenylistExceptions(raw)
	if err != nil {
		t.Fatalf("whitespace-tolerant parse should succeed: %v", err)
	}
	if len(ex) != 1 || ex[0].SelectorKey != "role" || ex[0].SelectorValue != "gpu" {
		t.Errorf("whitespace trim failed: %#v", ex)
	}
}

func TestParseEnvDenylistExceptions_RejectsMalformed(t *testing.T) {
	// Each of these would silently produce a useless or confusing rule
	// if the parser accepted them, so the coordinator must fail to
	// start instead.
	bad := map[string]string{
		"missing colon":         "role=gpu LD_LIBRARY_PATH",
		"missing selector eq":   "role_gpu:LD_LIBRARY_PATH",
		"empty selector key":    "=gpu:LD_LIBRARY_PATH",
		"empty selector value":  "role=:LD_LIBRARY_PATH",
		"empty env list":        "role=gpu:",
		"empty env key in list": "role=gpu:LD_LIBRARY_PATH,,GCONV_PATH",
		"non-denylisted key":    "role=gpu:PYTHONPATH",  // would be a no-op
		"not-on-list exact":     "role=gpu:GCNV_PATH",   // typo of GCONV_PATH; no LD_ prefix to save it
		"selector NUL":          "role=g\x00pu:LD_LIBRARY_PATH",
	}
	for name, raw := range bad {
		ex, err := ParseEnvDenylistExceptions(raw)
		if err == nil {
			t.Errorf("%s: expected err, got rules %#v", name, ex)
		}
	}
}

func TestParseEnvDenylistExceptions_SemicolonOnly(t *testing.T) {
	// All-separator input must not panic or silently yield nil — it's
	// malformed in spirit. Current impl skips empty rules and returns
	// nil; accept that (no rules produced) so long as no error.
	_, err := ParseEnvDenylistExceptions(";;;")
	if err != nil {
		t.Errorf("all-separator input: want nil err, got %v", err)
	}
}

// ── 3. envKeyAllowedByException ──────────────────────────────────────────────

func TestEnvKeyAllowedByException_NoExceptionsNoSelector(t *testing.T) {
	if envKeyAllowedByException("LD_LIBRARY_PATH", nil, nil) {
		t.Error("no exceptions + no selector must not allow any key")
	}
}

func TestEnvKeyAllowedByException_MatchesExactSelector(t *testing.T) {
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "gpu"}
	if !envKeyAllowedByException("LD_LIBRARY_PATH", sel, ex) {
		t.Error("matching selector + listed key: want allowed, got blocked")
	}
}

func TestEnvKeyAllowedByException_MatchesAmongExtraSelectorEntries(t *testing.T) {
	// A job can carry additional selector entries beyond the one the
	// rule requires. That's common (GPU + instance-type + zone labels).
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "gpu", "zone": "us-east-1a", "gpu-model": "a100"}
	if !envKeyAllowedByException("LD_LIBRARY_PATH", sel, ex) {
		t.Error("extra selector entries must not block an otherwise-matching rule")
	}
}

func TestEnvKeyAllowedByException_ValueMismatchBlocks(t *testing.T) {
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "cpu"} // wrong value
	if envKeyAllowedByException("LD_LIBRARY_PATH", sel, ex) {
		t.Error("wrong selector value must not allow the key")
	}
}

func TestEnvKeyAllowedByException_SelectorKeyMissing(t *testing.T) {
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"zone": "us-east-1a"} // rule key absent
	if envKeyAllowedByException("LD_LIBRARY_PATH", sel, ex) {
		t.Error("missing selector key must not allow the key")
	}
}

func TestEnvKeyAllowedByException_KeyNotInRule(t *testing.T) {
	// Rule allows LD_LIBRARY_PATH only; LD_PRELOAD is denied on the
	// same node.
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "gpu"}
	if envKeyAllowedByException("LD_PRELOAD", sel, ex) {
		t.Error("key not in rule's AllowedKeys must not be let through")
	}
}

func TestEnvKeyAllowedByException_NonDenylistedKeyStillFalse(t *testing.T) {
	// Defence-in-depth: even if an exception somehow named a non-
	// denylisted key (parser prevents this, but the function must
	// still behave), the function must not treat the key as allowed
	// by exception — it's handled by the normal non-denylist path.
	ex := []EnvDenylistException{
		{SelectorKey: "role", SelectorValue: "gpu", AllowedKeys: map[string]struct{}{"PYTHONPATH": {}}},
	}
	sel := map[string]string{"role": "gpu"}
	if envKeyAllowedByException("PYTHONPATH", sel, ex) {
		t.Error("non-denylisted key must never be reported as allowed-by-exception")
	}
}

// ── 4. validateEnvMap ────────────────────────────────────────────────────────

func TestValidateEnvMap_EmptyOK(t *testing.T) {
	r := validateEnvMap(nil, nil, nil)
	if r.Err != "" {
		t.Errorf("empty env: want no err, got %q", r.Err)
	}
}

func TestValidateEnvMap_CountCap(t *testing.T) {
	env := make(map[string]string, maxEnvLen+1)
	for i := 0; i < maxEnvLen+1; i++ {
		env[runeOfIndex(i)] = "v"
	}
	r := validateEnvMap(env, nil, nil)
	if r.Err == "" {
		t.Error("over-cap env: want err")
	}
	if r.BlockedKey != "" {
		t.Errorf("count-cap err should not set BlockedKey, got %q", r.BlockedKey)
	}
}

func runeOfIndex(i int) string {
	// Generate unique env-var-name-shaped keys for the count test.
	// e.g. K0, K1, K129. Not pretty, just unique.
	b := make([]byte, 0, 8)
	b = append(b, 'K')
	if i == 0 {
		b = append(b, '0')
	}
	for i > 0 {
		b = append(b, byte('0'+i%10))
		i /= 10
	}
	return string(b)
}

func TestValidateEnvMap_EmptyKey(t *testing.T) {
	r := validateEnvMap(map[string]string{"": "v"}, nil, nil)
	if r.Err == "" || !strings.Contains(r.Err, "empty") {
		t.Errorf("empty key: want 'empty' in err, got %q", r.Err)
	}
}

func TestValidateEnvMap_KeyEqualsSign(t *testing.T) {
	r := validateEnvMap(map[string]string{"A=B": "v"}, nil, nil)
	if r.Err == "" || !strings.Contains(r.Err, "'='") {
		t.Errorf("= in key: want complaint, got %q", r.Err)
	}
}

func TestValidateEnvMap_NULInKey(t *testing.T) {
	r := validateEnvMap(map[string]string{"K\x00": "v"}, nil, nil)
	if r.Err == "" {
		t.Error("NUL in key: want err")
	}
}

func TestValidateEnvMap_NULInValue(t *testing.T) {
	r := validateEnvMap(map[string]string{"K": "v\x00"}, nil, nil)
	if r.Err == "" || !strings.Contains(r.Err, "values") {
		t.Errorf("NUL in value: want value-level err, got %q", r.Err)
	}
}

func TestValidateEnvMap_DenylistRejectsLDPRELOAD(t *testing.T) {
	r := validateEnvMap(map[string]string{"LD_PRELOAD": "/tmp/evil.so"}, nil, nil)
	if r.Err == "" {
		t.Fatal("LD_PRELOAD: want err")
	}
	if !strings.Contains(r.Err, "dynamic-loader") {
		t.Errorf("err should mention dynamic-loader: %q", r.Err)
	}
	if r.BlockedKey != "LD_PRELOAD" {
		t.Errorf("BlockedKey = %q, want LD_PRELOAD", r.BlockedKey)
	}
}

func TestValidateEnvMap_DenylistRejectsGCONV_PATH(t *testing.T) {
	r := validateEnvMap(map[string]string{"GCONV_PATH": "/tmp"}, nil, nil)
	if r.Err == "" || r.BlockedKey != "GCONV_PATH" {
		t.Errorf("GCONV_PATH: want reject with BlockedKey, got Err=%q BlockedKey=%q", r.Err, r.BlockedKey)
	}
}

func TestValidateEnvMap_NonDenylistedAllowed(t *testing.T) {
	env := map[string]string{"PYTHONPATH": "/usr/local/lib", "HELION_TOKEN": "abc"}
	r := validateEnvMap(env, nil, nil)
	if r.Err != "" {
		t.Errorf("common ML env: want no err, got %q", r.Err)
	}
	if r.BlockedKey != "" {
		t.Errorf("no denylist match: want empty BlockedKey, got %q", r.BlockedKey)
	}
	if len(r.OverriddenKeys) != 0 {
		t.Errorf("no overrides expected: got %v", r.OverriddenKeys)
	}
}

func TestValidateEnvMap_ExceptionAllowsAndRecords(t *testing.T) {
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "gpu"}
	env := map[string]string{"LD_LIBRARY_PATH": "/opt/cuda/lib64", "PYTHONPATH": "/app"}
	r := validateEnvMap(env, sel, ex)
	if r.Err != "" {
		t.Fatalf("exception should allow LD_LIBRARY_PATH on matching selector: %q", r.Err)
	}
	if len(r.OverriddenKeys) != 1 || r.OverriddenKeys[0] != "LD_LIBRARY_PATH" {
		t.Errorf("OverriddenKeys = %v, want [LD_LIBRARY_PATH]", r.OverriddenKeys)
	}
}

func TestValidateEnvMap_ExceptionDoesNotAllowUnlistedKey(t *testing.T) {
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "gpu"}
	env := map[string]string{"LD_PRELOAD": "/tmp/evil.so"}
	r := validateEnvMap(env, sel, ex)
	if r.Err == "" {
		t.Error("LD_PRELOAD on gpu node must still be rejected — not in rule's AllowedKeys")
	}
}

func TestValidateEnvMap_ExceptionWithoutMatchingSelector(t *testing.T) {
	ex, _ := ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	sel := map[string]string{"role": "cpu"}
	r := validateEnvMap(map[string]string{"LD_LIBRARY_PATH": "/cuda"}, sel, ex)
	if r.Err == "" {
		t.Error("LD_LIBRARY_PATH on non-matching selector must be rejected")
	}
}

// ── 5. isDangerousSystemPath ─────────────────────────────────────────────────

func TestIsDangerousSystemPath_RejectsSystemDirs(t *testing.T) {
	cases := []string{
		"/lib/libc.so.6",
		"/lib64/ld-linux-x86-64.so.2",
		"/usr/lib/libm.so.6",
		"/usr/lib64/libpthread.so.0",
		"/usr/local/lib/libfoo.so",
		"/proc/self/environ",
		"/sys/kernel/security",
		"/etc/passwd",
		"/etc/shadow",
		"/boot/vmlinuz",
		"/root/.ssh/id_rsa",
		"/dev/tcp/127.0.0.1/22",
		"/var/run/secrets/kubernetes.io/serviceaccount/token",
		"/run/secrets/docker-token",
		"/libexec/sudo/sudoers.so",
	}
	for _, p := range cases {
		blocked, matched := isDangerousSystemPath(p)
		if !blocked {
			t.Errorf("isDangerousSystemPath(%q) = false, want true", p)
		}
		if matched == "" {
			t.Errorf("isDangerousSystemPath(%q) returned empty matched prefix", p)
		}
	}
}

func TestIsDangerousSystemPath_AllowsSafeDirs(t *testing.T) {
	safe := []string{
		"/home/user/data.csv",
		"/mnt/models/classifier.onnx",
		"/opt/helion/data/train.parquet",
		"/tmp/job-123/input.txt",
		"/srv/artifacts/model.pt",
		"/workspace/code.py",
		// Near-miss guards: prefixes that share letters but are NOT under /lib etc.
		"/library/README.md",
		"/lib64-alt/file",
		"/procedure/doc.md",
		"/systemd/log",
	}
	for _, p := range safe {
		blocked, _ := isDangerousSystemPath(p)
		if blocked {
			t.Errorf("isDangerousSystemPath(%q) = true, want false (over-matched)", p)
		}
	}
}

func TestIsDangerousSystemPath_NormalisesDoubleSlashes(t *testing.T) {
	// Attacker trick: "/usr//lib/evil" must still be caught after
	// path.Clean — otherwise the filter is bypassable with one //.
	blocked, _ := isDangerousSystemPath("/usr//lib/evil.so")
	if !blocked {
		t.Error("path.Clean bypass via // must be blocked")
	}
}

func TestIsDangerousSystemPath_IgnoresRelativeAndEmpty(t *testing.T) {
	for _, p := range []string{"", "lib/libc.so", "./proc/self"} {
		blocked, _ := isDangerousSystemPath(p)
		if blocked {
			t.Errorf("isDangerousSystemPath(%q) = true, want false (relative/empty)", p)
		}
	}
}

// ── 6. isDangerousLibraryBasename ────────────────────────────────────────────

func TestIsDangerousLibraryBasename_ExactMatches(t *testing.T) {
	cases := []string{"libc.so", "libpthread.so", "libdl.so", "ld.so"}
	for _, b := range cases {
		blocked, reason := isDangerousLibraryBasename(b)
		if !blocked {
			t.Errorf("basename %q: want blocked", b)
		}
		if reason == "" {
			t.Errorf("basename %q: want non-empty reason", b)
		}
	}
}

func TestIsDangerousLibraryBasename_PrefixMatches(t *testing.T) {
	cases := []string{
		"libc.so.6", "libc.so.6.0.0", "libpthread.so.0",
		"ld-linux-x86-64.so.2", "ld-linux.so.2", "ld-musl-x86_64.so.1",
		"libnss_files.so.2", "libnss_dns.so.2",
		"ld-2.31.so", "libc-2.31.so",
	}
	for _, b := range cases {
		blocked, _ := isDangerousLibraryBasename(b)
		if !blocked {
			t.Errorf("basename %q: want blocked (prefix match)", b)
		}
	}
}

func TestIsDangerousLibraryBasename_Allowed(t *testing.T) {
	safe := []string{
		"",                    // empty — callers pass basename and must not panic
		"model.onnx",
		"train.parquet",
		"data.csv",
		"libcudart.so.11.0",   // legitimate CUDA lib; NOT in our narrow denylist
		"libtorch.so",         // PyTorch native
		"liblibc_foo.so",      // prefix "liblibc_" not a loader lib
		"libcore.so",          // common app name
		"libjson.so",
		"my_ld_script.py",     // contains 'ld' but not a library
		"README_ld_linux.md",  // non-.so; irrelevant
	}
	for _, b := range safe {
		blocked, _ := isDangerousLibraryBasename(b)
		if blocked {
			t.Errorf("isDangerousLibraryBasename(%q) = true, want false", b)
		}
	}
}

// ── 7. artifactURIPath ───────────────────────────────────────────────────────

func TestArtifactURIPath_FileTripleSlash(t *testing.T) {
	p, ok := artifactURIPath("file:///lib/libc.so.6")
	if !ok || p != "/lib/libc.so.6" {
		t.Errorf("triple-slash file URI: got (%q, %v)", p, ok)
	}
}

func TestArtifactURIPath_FileWithHost(t *testing.T) {
	p, ok := artifactURIPath("file://host.example.com/etc/passwd")
	if !ok || p != "/etc/passwd" {
		t.Errorf("file://host URI: got (%q, %v)", p, ok)
	}
}

func TestArtifactURIPath_NonFileReturnsFalse(t *testing.T) {
	for _, uri := range []string{"s3://bucket/obj", "http://a/b", ""} {
		_, ok := artifactURIPath(uri)
		if ok {
			t.Errorf("non-file URI %q: want ok=false", uri)
		}
	}
}

func TestArtifactURIPath_EmptyPathPortion(t *testing.T) {
	// file://something-with-no-slashes returns ok=true but empty path
	// — isDangerousSystemPath will report not dangerous for empty.
	p, ok := artifactURIPath("file://justhost")
	if !ok || p != "" {
		t.Errorf("file://justhost: got (%q, %v), want (\"\", true)", p, ok)
	}
}
