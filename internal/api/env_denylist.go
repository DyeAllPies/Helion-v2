// internal/api/env_denylist.go
//
// Feature 25 — dangerous-env denylist + optional per-node overrides +
// staging-path safety checks for system-library locations and loader-
// critical basenames.
//
// Three layers, all applied at submit time (real path + dry-run both):
//
//   1. envKeyBlocked — refuses env var keys that feed the dynamic loader
//      or a glibc/glib module system. The Go runtime passes the submit
//      env map verbatim to exec.Command.Env; without this check, any
//      caller with submit permission can stage an LD_PRELOAD hijack on
//      the node. The Rust runtime calls env_clear() before spawn so the
//      hole is Go-runtime specific, but both paths share the validator
//      because defence-in-depth (a Rust-runtime rollback would not
//      re-open the hole).
//
//   2. Per-node exceptions — an operator may legitimately need
//      LD_LIBRARY_PATH on a dedicated GPU node for CUDA dlopen. The
//      coordinator accepts HELION_ENV_DENYLIST_EXCEPTIONS=
//      <selector_key>=<selector_value>:<env_key>[,<env_key>...]
//      [;<next_rule>]. A job's env var is allowed only when its
//      NodeSelector carries the exact selector_key=selector_value pair
//      AND the env_key is listed for that rule. Overrides are audited
//      (audit.EventEnvDenylistOverride) so use of the escape hatch
//      shows up in the log.
//
//   3. Dangerous-path / dangerous-library checks on artifact bindings.
//      Submit-time complement to the loader-injection class of attacks:
//      reject `file://` URIs rooted at /lib, /usr/lib, /proc, /sys, etc.,
//      and LocalPath basenames that match a loader-critical library
//      (libc.so*, ld-linux*.so*, libpthread.so*, libdl.so*). Covers the
//      "staged symlink pointing at dangerous library" deferred item
//      from the feature 25 spec.

package api

import (
	"fmt"
	"path"
	"strings"
)

// ── Denylist tables ───────────────────────────────────────────────────────────

// envKeyBlockedPrefixes are prefix-matched against env-var keys. These
// cover the loader-injection vectors from the glibc + macOS loader
// documentation. See feature 25 spec for the full list with rationale.
var envKeyBlockedPrefixes = []string{
	"LD_",   // glibc dynamic loader (LD_PRELOAD, LD_LIBRARY_PATH, LD_AUDIT, …)
	"DYLD_", // macOS loader (DYLD_INSERT_LIBRARIES, DYLD_LIBRARY_PATH, …)
}

// envKeyBlockedExact are whole-key matches (case-sensitive — Unix env
// vars are case-sensitive, and the Linux loader ignores lowercase
// variants, so matching verbatim is correct).
var envKeyBlockedExact = map[string]struct{}{
	"GCONV_PATH":        {}, // glibc iconv — load attacker modules on charset conv
	"GIO_EXTRA_MODULES": {}, // glib — load attacker modules at g_type_init
	"HOSTALIASES":       {}, // glibc — alt hosts file, redirects name resolution
	"NLSPATH":           {}, // glibc — catalogue path, tangential attack surface
	"RES_OPTIONS":       {}, // glibc resolver override
}

// envKeyBlocked reports whether key is a known dynamic-loader or glibc
// module-loading variable. reason carries a short human string suitable
// for a 400 response body and for the audit event's detail field.
//
// Case sensitivity: matches verbatim. Uppercase-only is correct because
// the Linux loader itself only honours uppercase — a submitter writing
// `ld_preload` would set an inert variable, not get loader-injection.
func envKeyBlocked(key string) (blocked bool, reason string) {
	for _, p := range envKeyBlockedPrefixes {
		if strings.HasPrefix(key, p) {
			return true, "dynamic-loader injection vector (" + p + "* prefix)"
		}
	}
	if _, ok := envKeyBlockedExact[key]; ok {
		return true, "known module-loading or resolver env var"
	}
	return false, ""
}

// ── Per-node overrides ────────────────────────────────────────────────────────

// EnvDenylistException is one parsed rule from
// HELION_ENV_DENYLIST_EXCEPTIONS. A rule binds a single
// NodeSelector entry (SelectorKey=SelectorValue) to a set of env-var
// keys that jobs pinning to that selector may set despite the
// denylist.
//
// Matching semantics: a job's env key K is allowed iff
//   1. envKeyBlocked(K) is true,
//   2. the job's NodeSelector contains SelectorKey with value
//      SelectorValue verbatim, and
//   3. K is present in AllowedKeys.
//
// The job's NodeSelector may carry additional entries beyond the one
// matched by the rule — the rule is "if this label/value is present,
// allow these env vars", not "exactly this selector".
type EnvDenylistException struct {
	SelectorKey   string
	SelectorValue string
	AllowedKeys   map[string]struct{}
}

// ParseEnvDenylistExceptions parses the
// HELION_ENV_DENYLIST_EXCEPTIONS environment variable.
//
// Format (BNF-ish):
//
//	EXCEPTIONS   = RULE { ";" RULE }
//	RULE         = SELECTOR ":" ENV_KEY { "," ENV_KEY }
//	SELECTOR     = SELECTOR_KEY "=" SELECTOR_VALUE
//
// Example:
//
//	role=gpu:LD_LIBRARY_PATH;pool=build:LD_LIBRARY_PATH,GCONV_PATH
//
// Rules are validated strictly: bad input returns an error so the
// coordinator fails to start rather than silently running with a
// half-parsed exception set. Empty input returns (nil, nil).
//
// Each AllowedKey must itself be a currently-denylisted key —
// otherwise the rule is meaningless (the key wouldn't be blocked in
// the first place), and silently accepting noise would let an
// operator typo "LD_PERLOAD" into a real-looking "exception" that
// silently does nothing.
func ParseEnvDenylistExceptions(raw string) ([]EnvDenylistException, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []EnvDenylistException
	for _, rawRule := range strings.Split(raw, ";") {
		rule := strings.TrimSpace(rawRule)
		if rule == "" {
			continue
		}
		colon := strings.IndexByte(rule, ':')
		if colon < 0 {
			return nil, fmt.Errorf("env denylist exception %q: expected '<selector>:<env_key>[,env_key]*'", rule)
		}
		selector := strings.TrimSpace(rule[:colon])
		envCSV := strings.TrimSpace(rule[colon+1:])

		eq := strings.IndexByte(selector, '=')
		if eq < 0 {
			return nil, fmt.Errorf("env denylist exception %q: selector must be '<key>=<value>'", rule)
		}
		selKey := strings.TrimSpace(selector[:eq])
		selVal := strings.TrimSpace(selector[eq+1:])
		if selKey == "" || selVal == "" {
			return nil, fmt.Errorf("env denylist exception %q: selector key and value must both be non-empty", rule)
		}
		if strings.ContainsAny(selKey, "\x00") || strings.ContainsAny(selVal, "\x00") {
			return nil, fmt.Errorf("env denylist exception %q: NUL byte in selector", rule)
		}

		if envCSV == "" {
			return nil, fmt.Errorf("env denylist exception %q: at least one env key must follow ':'", rule)
		}

		allowed := make(map[string]struct{})
		for _, rawKey := range strings.Split(envCSV, ",") {
			k := strings.TrimSpace(rawKey)
			if k == "" {
				return nil, fmt.Errorf("env denylist exception %q: empty env key in list", rule)
			}
			if blocked, _ := envKeyBlocked(k); !blocked {
				return nil, fmt.Errorf("env denylist exception %q: env key %q is not on the denylist — the rule would have no effect", rule, k)
			}
			allowed[k] = struct{}{}
		}

		out = append(out, EnvDenylistException{
			SelectorKey:   selKey,
			SelectorValue: selVal,
			AllowedKeys:   allowed,
		})
	}
	return out, nil
}

// envKeyAllowedByException reports whether the job's NodeSelector
// carries an exception rule that allows the given (otherwise-blocked)
// env key.
//
// Design note: the caller is expected to have already established
// that envKeyBlocked(key) is true. We still re-check it here as a
// defence-in-depth guard — an exception must never promote a non-
// blocked key into an allow that bypasses future denylist growth.
func envKeyAllowedByException(key string, selector map[string]string, exceptions []EnvDenylistException) bool {
	if blocked, _ := envKeyBlocked(key); !blocked {
		return false
	}
	if len(exceptions) == 0 || len(selector) == 0 {
		return false
	}
	for _, ex := range exceptions {
		v, ok := selector[ex.SelectorKey]
		if !ok || v != ex.SelectorValue {
			continue
		}
		if _, ok := ex.AllowedKeys[key]; ok {
			return true
		}
	}
	return false
}

// ── Combined env-map validation ───────────────────────────────────────────────

// EnvValidationResult summarises the outcome of validateEnvMap. The
// caller uses it to decide whether to 400, whether to emit an audit
// event, and (on the allow-via-exception path) which keys were let
// through so each override can be audited individually.
type EnvValidationResult struct {
	// Err is non-empty on rejection. The caller sends it as the 400
	// body and emits an EventEnvDenylistReject audit record.
	Err string
	// BlockedKey is the first key that tripped the denylist, echoed
	// into the audit record's detail field. Empty when Err is set by
	// a non-denylist check (count/empty-key/NUL).
	BlockedKey string
	// OverriddenKeys are denylist keys that were allowed through by a
	// per-node exception. The caller emits one
	// EventEnvDenylistOverride per entry so the escape-hatch usage is
	// loudly audited even when the submit succeeds.
	OverriddenKeys []string
}

// validateEnvMap runs every env-level check in one pass so the handler
// can't accidentally drop one: count cap, empty key, NUL byte, denylist
// match (with per-node exception lookup).
//
// The result struct separates "why it failed" from "side-effects to
// audit on success" so the caller's audit logic stays small.
func validateEnvMap(env map[string]string, selector map[string]string, exceptions []EnvDenylistException) EnvValidationResult {
	var r EnvValidationResult
	if len(env) > maxEnvLen {
		r.Err = fmt.Sprintf("env must not exceed %d entries", maxEnvLen)
		return r
	}
	for k, v := range env {
		if k == "" {
			r.Err = "env keys must not be empty"
			return r
		}
		if strings.ContainsAny(k, "=\x00") {
			r.Err = "env keys must not contain '=' or NUL"
			return r
		}
		if strings.ContainsRune(v, '\x00') {
			r.Err = "env values must not contain NUL"
			return r
		}
		if blocked, reason := envKeyBlocked(k); blocked {
			if envKeyAllowedByException(k, selector, exceptions) {
				r.OverriddenKeys = append(r.OverriddenKeys, k)
				continue
			}
			r.Err = fmt.Sprintf("env key %q rejected: %s", k, reason)
			r.BlockedKey = k
			return r
		}
	}
	return r
}

// ── Dangerous-path / dangerous-library artifact checks ────────────────────────

// dangerousSystemPathPrefixes are absolute path prefixes the coordinator
// refuses to reference as a `file://` artifact URI. These are the
// standard locations for the dynamic loader, shared libraries, kernel-
// exported filesystems, and credential material. A submit that names
// any of them is almost certainly attempting to stage a system library
// or secret into a job's working dir — neither of which is a legitimate
// Helion use case.
//
// Listed as prefixes (trailing slash) so `/lib64-foo` isn't spuriously
// caught. The root comparison uses path.Clean so `/usr//lib/foo`
// doesn't slip through.
var dangerousSystemPathPrefixes = []string{
	"/lib/",
	"/lib64/",
	"/libexec/",
	"/usr/lib/",
	"/usr/lib64/",
	"/usr/libexec/",
	"/usr/local/lib/",
	"/usr/local/lib64/",
	"/proc/",
	"/sys/",
	"/dev/",
	"/boot/",
	"/etc/",
	"/root/",
	"/var/run/secrets/",
	"/run/secrets/",
	"/run/credentials/",
}

// isDangerousSystemPath reports whether absPath lies under one of the
// sensitive system directories. Accepts and normalises both forward-
// slash and already-clean inputs; returns the canonical matched
// prefix for the error/audit message.
//
// Input must be absolute. A relative path is never a system-library
// path — callers filter those out separately (LocalPath is already
// required to be relative by validateLocalPath).
func isDangerousSystemPath(absPath string) (bool, string) {
	if absPath == "" || !strings.HasPrefix(absPath, "/") {
		return false, ""
	}
	clean := path.Clean(absPath)
	// path.Clean("/") == "/", path.Clean("/lib") == "/lib". Append "/" so
	// "/lib" and "/lib/foo" both match "/lib/".
	cmp := clean
	if !strings.HasSuffix(cmp, "/") {
		cmp += "/"
	}
	for _, p := range dangerousSystemPathPrefixes {
		if strings.HasPrefix(cmp, p) {
			return true, strings.TrimSuffix(p, "/")
		}
	}
	return false, ""
}

// dangerousLibraryBasenames are file basenames that match loader-
// critical shared libraries. Staging one of these under a job's
// working dir gives an attacker a ready target for dlopen-by-relative-
// path and similar tricks; a legitimate ML job has no reason to ship
// its own libc or dynamic linker.
//
// Stored as patterns keyed by how we match them:
//   - exact: whole-basename equality
//   - prefix: basename starts with this string (for versioned SONAMEs
//     like libc.so.6, libc.so.6.0.0)
//
// The denylist is intentionally narrow: it covers the libraries the
// dynamic linker itself consults on every exec. Adding domain libs
// (libcuda, libtorch, …) would break legitimate ML artifact staging.
var dangerousLibraryExactBasenames = map[string]struct{}{
	"libc.so":       {},
	"libpthread.so": {},
	"libdl.so":      {},
	"libm.so":       {},
	"librt.so":      {},
	"libnsl.so":     {},
	"libutil.so":    {},
	"libresolv.so":  {},
	"libcrypt.so":   {},
	"ld.so":         {},
}

var dangerousLibraryPrefixes = []string{
	"libc.so.",       // libc.so.6, libc.so.6.0.0, …
	"libpthread.so.", // libpthread.so.0
	"libdl.so.",
	"libm.so.",
	"librt.so.",
	"libnsl.so.",
	"libutil.so.",
	"libresolv.so.",
	"libcrypt.so.",
	"libnss_",     // libnss_files.so.2, libnss_dns.so.2
	"ld-linux",    // ld-linux-x86-64.so.2, ld-linux.so.2
	"ld-musl",     // ld-musl-x86_64.so.1
	"ld.so.",      // ld.so.1
	"ld-2.",       // ld-2.31.so (older glibc layouts)
	"libc-",       // libc-2.31.so (older glibc layouts)
}

// isDangerousLibraryBasename reports whether the given filename (no
// directory component) is a loader-critical shared library. The
// returned reason is suitable for a 400 body and audit detail field.
//
// basename is compared verbatim — case-sensitive — because Unix
// filesystems are case-sensitive and the loader looks for exact names.
func isDangerousLibraryBasename(basename string) (bool, string) {
	if basename == "" {
		return false, ""
	}
	if _, ok := dangerousLibraryExactBasenames[basename]; ok {
		return true, "loader-critical shared library"
	}
	for _, p := range dangerousLibraryPrefixes {
		if strings.HasPrefix(basename, p) {
			return true, "loader-critical shared library (" + p + "* prefix)"
		}
	}
	return false, ""
}

// ── Handler-side denylist check (uses Server exception rules) ───────────────

// envDenylistCheck runs the denylist-only portion of env validation
// with the Server's parsed per-node exception rules. The shape/count
// checks have already run in validateSubmitRequest (or its workflow
// equivalent); this function exists only to apply the denylist with
// the Server-scoped configuration the pure validator can't see.
//
// The caller is expected to emit audit events based on the returned
// OverriddenKeys and BlockedKey. The caller also decides whether to
// 400 (Err != "") or proceed.
func (s *Server) envDenylistCheck(env, selector map[string]string) EnvValidationResult {
	return denylistOnly(env, selector, s.envDenylistExceptions)
}

// denylistOnly is the pure function beneath envDenylistCheck. Skips
// count / empty-key / NUL checks (already done by validateSubmitRequest)
// and only applies the denylist + exception lookup. Exposed as a
// standalone function so the workflow handler can call it for each
// child job without needing a different entry point.
func denylistOnly(env, selector map[string]string, exceptions []EnvDenylistException) EnvValidationResult {
	var r EnvValidationResult
	for k := range env {
		blocked, reason := envKeyBlocked(k)
		if !blocked {
			continue
		}
		if envKeyAllowedByException(k, selector, exceptions) {
			r.OverriddenKeys = append(r.OverriddenKeys, k)
			continue
		}
		r.Err = fmt.Sprintf("env key %q rejected: %s", k, reason)
		r.BlockedKey = k
		return r
	}
	return r
}

// artifactURIPath extracts the filesystem path portion of a `file://`
// URI for the dangerous-path check. Returns ("", false) for any other
// scheme so the caller can skip the check on s3:// (the bucket object
// names aren't filesystem-meaningful).
//
// file:// URIs come in two shapes in practice:
//   - file:///abs/path   (triple slash, RFC-compliant)
//   - file://host/path   (rare; host portion ignored by local stagers)
//
// We normalise both to the absolute-path form.
func artifactURIPath(uri string) (string, bool) {
	const prefix = "file://"
	if !strings.HasPrefix(uri, prefix) {
		return "", false
	}
	rest := uri[len(prefix):]
	// Triple-slash form: file:///foo → "/foo".
	if strings.HasPrefix(rest, "/") {
		return rest, true
	}
	// file://host/abs → strip host.
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		return rest[idx:], true
	}
	// file://something-with-no-path — treat as empty, not dangerous.
	return "", true
}
