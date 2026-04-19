// internal/api/handlers_jobs.go
//
// Job CRUD handlers:
//   POST /jobs        — submit a new job
//   GET  /jobs/{id}   — read a single job
//   GET  /jobs        — list jobs (paginated, filterable by status)

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// AUDIT M4 (fixed): body size is capped at 1 MiB via http.MaxBytesReader so that
// a large request cannot exhaust coordinator memory.
const maxSubmitBodyBytes = 1 << 20 // 1 MiB

// AUDIT M7 (fixed): timeout_seconds is bounded to 1 hour. Values outside
// [0, maxTimeoutSeconds] are rejected with 400 Bad Request.
const maxTimeoutSeconds = 3600 // 1 hour upper bound

// AUDIT C4 / C5 (fixed): input validation bounds.
//
// maxArgsLen and maxEnvLen prevent a caller from exhausting memory by
// submitting unbounded argv / environment slices. The limits are generous
// (much larger than any realistic job) but sharp enough that a runaway
// caller cannot weaponise them.
const (
	maxArgsLen = 512
	maxEnvLen  = 128
)

// minMemoryBytes is the floor enforced when MemoryBytes is set (cgroup memory
// controllers reject values this small and modern runtimes crash-loop below it).
const minMemoryBytes = 4 << 20 // 4 MiB

// CPUPeriodUS must be within the Linux cgroup-v2 valid range.
const (
	minCPUPeriodUS = 1_000
	maxCPUPeriodUS = 1_000_000
)

// maxCPUCores caps CPUQuotaUS at `maxCPUCores * CPUPeriodUS` so a single job
// cannot claim more than 512 logical cores.
const maxCPUCores = 512

// maxGPUs bounds a single job's GPU reservation. The cluster's biggest
// commercially-available hosts ship with 8 GPUs today; 16 gives
// plenty of headroom for future multi-root configurations without
// letting an oversized request slip through and starve the
// bin-packing loop.
const maxGPUs = 16

// forbiddenCommandChars lists bytes that are never valid inside req.Command.
// Forbidding path separators prevents absolute-path execution; forbidding
// shell metacharacters is defense-in-depth (the runtime does not invoke a
// shell, but disallowing them surfaces mistakes early and blocks the most
// common injection shapes).
const forbiddenCommandChars = "/\\`$|&;<>\x00"

// Step 2 — ML pipeline input/output bounds. These caps mirror the
// defense-in-depth approach used elsewhere in this file: each bound is
// generous (much larger than any realistic ML job) but sharp enough that
// an unbounded request cannot weaponise it. The same limits apply to
// inputs and outputs independently.
const (
	maxArtifactBindings    = 64   // per-direction (inputs or outputs)
	maxArtifactNameLen     = 64   // environment-variable name ceiling
	maxArtifactLocalPath   = 512  // relative path length
	maxArtifactURILen      = 2048 // URI length ceiling
	maxNodeSelectorEntries = 32
	maxNodeSelectorKeyLen  = 63
	maxNodeSelectorValLen  = 253
	maxWorkingDirLen       = 512

	// Feature 17 — inference service spec.
	maxHealthPathLen         = 256          // bytes in service.health_path
	maxServiceHealthInitialMs = 30 * 60 * 1000 // 30 min grace cap
)

// validateArtifactName enforces that Name is a valid shell identifier.
// The runtime uses it verbatim in HELION_INPUT_<NAME>, so anything that
// isn't [A-Z_][A-Z0-9_]* would either fail on exec or, worse, be
// interpreted oddly by the child process's shell wrappers.
func validateArtifactName(name string) bool {
	if name == "" || len(name) > maxArtifactNameLen {
		return false
	}
	for i, c := range name {
		switch {
		case c >= 'A' && c <= 'Z':
		case c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// validateLocalPath rejects paths that could escape the working
// directory. Absolute paths, backslashes (Windows separators snuck past
// clients), NUL bytes, and any ".." segment are refused. A traversal
// check runs after filepath.Clean-style normalisation in the runtime,
// but we reject early here so a bad request never even reaches a node.
func validateLocalPath(p string) string {
	if p == "" {
		return "local_path is required"
	}
	if len(p) > maxArtifactLocalPath {
		return fmt.Sprintf("local_path must not exceed %d bytes", maxArtifactLocalPath)
	}
	if strings.ContainsRune(p, '\x00') {
		return "local_path must not contain NUL"
	}
	if strings.ContainsRune(p, '\\') {
		return "local_path must use forward slashes"
	}
	if strings.HasPrefix(p, "/") {
		return "local_path must be relative"
	}
	// Segment scan: reject empty and ".." segments. "." is allowed only
	// as the single leading segment in patterns like "./file", which
	// we normalise by not allowing it outright — keep things simple.
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == ".." || seg == "." {
			return "local_path must not contain empty, '.', or '..' segments"
		}
	}
	return ""
}

// validateNodeSelector applies label-shape rules before persisting.
// Keys and values are clamped to Kubernetes-compatible sizes so that
// once the scheduler wires these in (step 4), the limits already match
// operator muscle memory.
func validateNodeSelector(sel map[string]string) string {
	if len(sel) > maxNodeSelectorEntries {
		return fmt.Sprintf("node_selector must not exceed %d entries", maxNodeSelectorEntries)
	}
	for k, v := range sel {
		if k == "" || len(k) > maxNodeSelectorKeyLen {
			return fmt.Sprintf("node_selector keys must be 1-%d bytes", maxNodeSelectorKeyLen)
		}
		if strings.ContainsAny(k, "=\x00") {
			return "node_selector keys must not contain '=' or NUL"
		}
		if len(v) > maxNodeSelectorValLen {
			return fmt.Sprintf("node_selector values must not exceed %d bytes", maxNodeSelectorValLen)
		}
		if strings.ContainsRune(v, '\x00') {
			return "node_selector values must not contain NUL"
		}
	}
	return ""
}

// validateSubmitRequest runs the AUDIT C4 / C5 input checks that do not depend
// on request state. Returns an empty string on success or a user-facing error
// message (which the caller sends with 400 Bad Request).
func validateSubmitRequest(req *SubmitRequest) string {
	// Command content — path separators, shell metacharacters, NUL.
	if strings.ContainsAny(req.Command, forbiddenCommandChars) {
		return "command must not contain path separators or shell metacharacters"
	}
	// Args count.
	if len(req.Args) > maxArgsLen {
		return fmt.Sprintf("args must not exceed %d entries", maxArgsLen)
	}
	// Env count.
	if len(req.Env) > maxEnvLen {
		return fmt.Sprintf("env must not exceed %d entries", maxEnvLen)
	}
	// Env key/value content — no `=` or NUL in keys, no NUL in values.
	// The feature-25 denylist runs separately in the handler via
	// s.validateEnvDenylist so it has access to the per-node exception
	// rules stored on the Server; this function stays pure/testable.
	for k, v := range req.Env {
		if k == "" {
			return "env keys must not be empty"
		}
		if strings.ContainsAny(k, "=\x00") {
			return "env keys must not contain '=' or NUL"
		}
		if strings.ContainsRune(v, '\x00') {
			return "env values must not contain NUL"
		}
	}
	// MemoryBytes floor (only when set).
	if req.Limits.MemoryBytes > 0 && req.Limits.MemoryBytes < minMemoryBytes {
		return fmt.Sprintf("limits.memory_bytes must be at least %d when set", minMemoryBytes)
	}
	// CPUPeriodUS range (only when set).
	if req.Limits.CPUPeriodUS > 0 {
		if req.Limits.CPUPeriodUS < minCPUPeriodUS || req.Limits.CPUPeriodUS > maxCPUPeriodUS {
			return fmt.Sprintf("limits.cpu_period_us must be in [%d, %d]", minCPUPeriodUS, maxCPUPeriodUS)
		}
	}
	// CPUQuotaUS cap — at most maxCPUCores × period.
	if req.Limits.CPUQuotaUS > 0 && req.Limits.CPUPeriodUS > 0 {
		if req.Limits.CPUQuotaUS > req.Limits.CPUPeriodUS*maxCPUCores {
			return fmt.Sprintf("limits.cpu_quota_us must not exceed %d × cpu_period_us", maxCPUCores)
		}
	}
	// GPU reservation cap — defended at submit so the scheduler and
	// the per-node GPU allocator never see an unreachable request.
	if req.Resources != nil && req.Resources.GPUs > maxGPUs {
		return fmt.Sprintf("resources.gpus must not exceed %d", maxGPUs)
	}
	// Step 2 — ML pipeline fields.
	if len(req.WorkingDir) > maxWorkingDirLen {
		return fmt.Sprintf("working_dir must not exceed %d bytes", maxWorkingDirLen)
	}
	if strings.ContainsRune(req.WorkingDir, '\x00') {
		return "working_dir must not contain NUL"
	}
	if msg := validateArtifactBindings("inputs", req.Inputs, true); msg != "" {
		return msg
	}
	if msg := validateArtifactBindings("outputs", req.Outputs, false); msg != "" {
		return msg
	}
	// Names must be unique across inputs alone and outputs alone so the
	// HELION_INPUT_<NAME> / HELION_OUTPUT_<NAME> exports are unambiguous.
	if dup := firstDuplicateBindingName(req.Inputs); dup != "" {
		return fmt.Sprintf("inputs: duplicate name %q", dup)
	}
	if dup := firstDuplicateBindingName(req.Outputs); dup != "" {
		return fmt.Sprintf("outputs: duplicate name %q", dup)
	}
	if msg := validateNodeSelector(req.NodeSelector); msg != "" {
		return msg
	}
	if msg := validateServiceSpec(req.Service); msg != "" {
		return msg
	}
	return ""
}

// validateServiceSpec runs the feature-17 inference-job submit checks.
// A nil Service is valid (the job is a normal batch job); when set,
// the port must be in the user range and the health path must be a
// syntactically conservative absolute path. Grace period is bounded
// by maxServiceHealthInitialMs so a misconfigured job cannot delay
// its own failure detection indefinitely.
func validateServiceSpec(s *ServiceSpecRequest) string {
	if s == nil {
		return ""
	}
	if s.Port < 1 || s.Port > 65535 {
		return "service.port must be in [1, 65535]"
	}
	// Reject privileged ports — the node-agent runs as a non-root
	// DaemonSet in production and binding below 1024 would fail
	// anyway; catching it at submit gives a crisp 400 rather than a
	// spawn-time crash.
	if s.Port < 1024 {
		return "service.port must be ≥ 1024 (privileged ports are not bindable by the node agent)"
	}
	if s.HealthPath == "" {
		return "service.health_path is required when service is set"
	}
	if !strings.HasPrefix(s.HealthPath, "/") {
		return "service.health_path must start with '/'"
	}
	if len(s.HealthPath) > maxHealthPathLen {
		return fmt.Sprintf("service.health_path must not exceed %d bytes", maxHealthPathLen)
	}
	if strings.ContainsAny(s.HealthPath, " \t\r\n\x00") {
		return "service.health_path must not contain whitespace or NUL"
	}
	if s.HealthInitialMs > maxServiceHealthInitialMs {
		return fmt.Sprintf("service.health_initial_ms must not exceed %d (30 min)", maxServiceHealthInitialMs)
	}
	return ""
}

// validateArtifactBindings is the legacy plain-submit validator —
// rejects any From reference because From is only meaningful inside a
// workflow where sibling jobs exist. Use validateArtifactBindingsCtx
// for the workflow path.
func validateArtifactBindings(kind string, bs []ArtifactBindingRequest, requireURI bool) string {
	return validateArtifactBindingsCtx(kind, bs, requireURI, false)
}

// validateArtifactBindingsCtx runs per-binding checks common to inputs
// and outputs. requireURI differentiates the two: inputs must name the
// artifact to pull, outputs have their URI assigned by the runtime.
// allowFrom widens the input rule to accept a From reference as an
// alternative to URI — only appropriate inside a workflow submission.
func validateArtifactBindingsCtx(kind string, bs []ArtifactBindingRequest, requireURI, allowFrom bool) string {
	if len(bs) > maxArtifactBindings {
		return fmt.Sprintf("%s must not exceed %d entries", kind, maxArtifactBindings)
	}
	for i, b := range bs {
		if !validateArtifactName(b.Name) {
			return fmt.Sprintf("%s[%d].name must match [A-Z_][A-Z0-9_]*", kind, i)
		}
		if msg := validateLocalPath(b.LocalPath); msg != "" {
			return fmt.Sprintf("%s[%d].%s", kind, i, msg)
		}
		if len(b.URI) > maxArtifactURILen {
			return fmt.Sprintf("%s[%d].uri must not exceed %d bytes", kind, i, maxArtifactURILen)
		}
		for j := 0; j < len(b.URI); j++ {
			if c := b.URI[j]; c == 0 || c < 0x20 || c == 0x7f {
				return fmt.Sprintf("%s[%d].uri must not contain NUL or control bytes", kind, i)
			}
		}
		// From is only valid on *workflow-job* inputs (allowFrom=true
		// and requireURI=true — outputs don't get a From, neither do
		// plain submits because there's no "upstream" without a DAG).
		if b.From != "" {
			if !allowFrom || !requireURI {
				return fmt.Sprintf("%s[%d].from is only valid on workflow-job inputs", kind, i)
			}
			if b.URI != "" {
				return fmt.Sprintf("%s[%d]: uri and from are mutually exclusive", kind, i)
			}
			if msg := validateArtifactFromShape(b.From); msg != "" {
				return fmt.Sprintf("%s[%d].from %s", kind, i, msg)
			}
			// A from-reference skips the URI-required and scheme checks below.
			continue
		}
		if requireURI && b.URI == "" {
			if allowFrom {
				return fmt.Sprintf("%s[%d]: one of uri or from is required", kind, i)
			}
			return fmt.Sprintf("%s[%d].uri is required", kind, i)
		}
		if !requireURI && b.URI != "" {
			return fmt.Sprintf("%s[%d].uri must be empty on submit (assigned by runtime)", kind, i)
		}
		// Input URIs are dereferenced by the node's stager: the
		// command line to attach an attacker's malware to a training
		// job is a Put + a submit pointing at `http://attacker/x`.
		// Lock the scheme to what the configured artifact Store
		// understands — everything else is rejected here, long before
		// the node gets involved.
		if requireURI && !isAllowedArtifactScheme(b.URI) {
			return fmt.Sprintf("%s[%d].uri scheme must be file:// or s3://", kind, i)
		}
		// Feature 25 — dangerous-path / loader-library guards.
		//
		// (a) file:// URIs rooted under system-library / kernel-export
		//     / secret-material directories are refused at submit time.
		//     A legitimate job has no reason to stage /lib/libc.so.6,
		//     /proc/self/environ, or /var/run/secrets/... as an artifact
		//     input. If artifactURIPath reports a non-file URI we skip
		//     the check — s3:// object keys aren't filesystem paths.
		if b.URI != "" {
			if p, ok := artifactURIPath(b.URI); ok {
				if bad, matched := isDangerousSystemPath(p); bad {
					return fmt.Sprintf("%s[%d].uri refuses to reference system path %s* (feature 25)", kind, i, matched)
				}
			}
		}
		// (b) LocalPath basenames matching a loader-critical shared
		//     library (libc.so*, ld-linux*.so*, libpthread.so*, …).
		//     Staging an input with one of these filenames under the
		//     job's working dir gives an attacker a ready target for
		//     dlopen-by-relative-path and LD_LIBRARY_PATH tricks; a
		//     legitimate ML job never ships its own libc.
		if base := lastPathSegment(b.LocalPath); base != "" {
			if bad, reason := isDangerousLibraryBasename(base); bad {
				return fmt.Sprintf("%s[%d].local_path basename %q is a %s (feature 25)", kind, i, base, reason)
			}
		}
	}
	return ""
}

// lastPathSegment returns the filename portion of a forward-slash
// path. Stays in this file because validateLocalPath has already
// rejected absolute paths, backslashes, NUL, and .. / . segments, so
// a simple LastIndex split is safe here.
func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// maxArtifactFromLen caps the "<job>.<output>" reference length. The
// upstream name obeys whatever limit the workflow-name validator
// imposes elsewhere; the output-name half is capped by
// maxArtifactNameLen. 256 bytes is more than either half could
// legitimately need combined.
const maxArtifactFromLen = 256

// validateArtifactFromShape checks the "<upstream_job>.<output_name>"
// syntax. The output name must match the same charset as a binding
// Name (so HELION_INPUT_<NAME> stays a valid env var) and the upstream
// job name must be non-empty. Splitting at the *last* '.' lets us
// accept workflow job names that happen to contain periods (e.g.
// user.build-1). Returns the empty string on success or a reason
// suffix that the caller prepends with "from " for a human message.
func validateArtifactFromShape(ref string) string {
	if ref == "" {
		return "is required"
	}
	if len(ref) > maxArtifactFromLen {
		return fmt.Sprintf("must not exceed %d bytes", maxArtifactFromLen)
	}
	for i := 0; i < len(ref); i++ {
		if c := ref[i]; c == 0 || c < 0x20 || c == 0x7f {
			return "must not contain NUL or control bytes"
		}
	}
	dot := strings.LastIndexByte(ref, '.')
	if dot <= 0 || dot == len(ref)-1 {
		return `must be "<upstream_job>.<output_name>"`
	}
	upstream := ref[:dot]
	output := ref[dot+1:]
	if upstream == "" {
		return `must be "<upstream_job>.<output_name>"`
	}
	if !validateArtifactName(output) {
		return fmt.Sprintf("output name %q must match [A-Z_][A-Z0-9_]*", output)
	}
	return ""
}

// SplitFromRef pulls the "<upstream>.<output>" pair out of a valid
// From reference. The caller must have run validateArtifactFromShape
// first; this helper does no re-validation and returns ("","") for
// malformed input.
func SplitFromRef(ref string) (upstream, output string) {
	if ref == "" {
		return "", ""
	}
	dot := strings.LastIndexByte(ref, '.')
	if dot <= 0 || dot == len(ref)-1 {
		return "", ""
	}
	return ref[:dot], ref[dot+1:]
}

// isAllowedArtifactScheme returns true iff uri starts with one of the
// schemes the artifact Store understands. Kept separate from the URL
// parser: we only need a prefix match and want no allocation.
func isAllowedArtifactScheme(uri string) bool {
	return strings.HasPrefix(uri, "file://") || strings.HasPrefix(uri, "s3://")
}

func firstDuplicateBindingName(bs []ArtifactBindingRequest) string {
	seen := make(map[string]struct{}, len(bs))
	for _, b := range bs {
		if _, ok := seen[b.Name]; ok {
			return b.Name
		}
		seen[b.Name] = struct{}{}
	}
	return ""
}

// convertBindings lifts the API-layer request shape to the persisted
// cpb.ArtifactBinding form. Returns nil for an empty slice so that the
// BadgerDB-serialised Job carries an omitted key rather than an empty
// array (match the rest of this struct's ergonomics).
func convertBindings(bs []ArtifactBindingRequest) []cpb.ArtifactBinding {
	if len(bs) == 0 {
		return nil
	}
	out := make([]cpb.ArtifactBinding, len(bs))
	for i, b := range bs {
		out[i] = cpb.ArtifactBinding{
			Name:      b.Name,
			URI:       b.URI,
			From:      b.From,
			LocalPath: b.LocalPath,
		}
	}
	return out
}

func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	// AUDIT M4: cap request body before decoding to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)

	// Feature 24 — parse the dry-run flag BEFORE decoding the body
	// so an obvious typo (?dry_run=maybe) rejects cheap. Decode +
	// validators still run in dry-run mode; we only skip the final
	// Submit() + audit-event emission.
	dryRun, err := ParseDryRunParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// AUDIT M4: generic message prevents leaking internal decode details.
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}
	// AUDIT M7: reject negative and excessive timeout values.
	if req.TimeoutSeconds < 0 {
		writeError(w, http.StatusBadRequest, "timeout_seconds must not be negative")
		return
	}
	if req.TimeoutSeconds > maxTimeoutSeconds {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("timeout_seconds must not exceed %d", maxTimeoutSeconds))
		return
	}
	// AUDIT C4 / C5: validate command, args, env, and resource limits.
	if msg := validateSubmitRequest(&req); msg != "" {
		s.recordSubmission(r, "job", req.ID, dryRun, false, msg) // feature 28
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	// Feature 26 — secret-key declarations must be well-formed and must
	// name only keys that actually appear in Env. Rejected at submit so
	// a typo never ships to storage where GET would silently not redact
	// a value the operator thought they flagged.
	if msg := validateSecretKeys(req.Env, req.SecretKeys); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	job := &cpb.Job{
		ID:             req.ID,
		Command:        req.Command,
		Args:           req.Args,
		Env:            req.Env,
		TimeoutSeconds: req.TimeoutSeconds,
		Limits: cpb.ResourceLimits{
			MemoryBytes: req.Limits.MemoryBytes,
			CPUQuotaUS:  req.Limits.CPUQuotaUS,
			CPUPeriodUS: req.Limits.CPUPeriodUS,
		},
		WorkingDir:   req.WorkingDir,
		Inputs:       convertBindings(req.Inputs),
		Outputs:      convertBindings(req.Outputs),
		NodeSelector: req.NodeSelector,
		SecretKeys:   req.SecretKeys, // feature 26
	}
	if req.Service != nil {
		job.Service = &cpb.ServiceSpec{
			Port:            req.Service.Port,
			HealthPath:      req.Service.HealthPath,
			HealthInitialMS: req.Service.HealthInitialMs,
		}
	}

	// Parse optional priority.
	if req.Priority != nil {
		if *req.Priority > 100 {
			writeError(w, http.StatusBadRequest, "priority must be between 0 and 100")
			return
		}
		job.Priority = *req.Priority
	}

	// Parse optional resource request.
	if req.Resources != nil {
		job.Resources = cpb.ResourceRequest{
			CpuMillicores: req.Resources.CpuMillicores,
			MemoryBytes:   req.Resources.MemoryBytes,
			Slots:         req.Resources.Slots,
			GPUs:          req.Resources.GPUs,
		}
	}

	// Parse optional retry policy.
	if req.RetryPolicy != nil {
		rp := req.RetryPolicy
		if rp.MaxAttempts > 100 {
			writeError(w, http.StatusBadRequest, "retry_policy.max_attempts must not exceed 100")
			return
		}
		policy := &cpb.RetryPolicy{
			MaxAttempts:    rp.MaxAttempts,
			InitialDelayMs: rp.InitialDelayMs,
			MaxDelayMs:     rp.MaxDelayMs,
			Jitter:         true, // default
		}
		if rp.Jitter != nil {
			policy.Jitter = *rp.Jitter
		}
		switch strings.ToLower(rp.Backoff) {
		case "none":
			policy.Backoff = cpb.BackoffNone
		case "linear":
			policy.Backoff = cpb.BackoffLinear
		case "", "exponential":
			policy.Backoff = cpb.BackoffExponential
		default:
			writeError(w, http.StatusBadRequest, "retry_policy.backoff must be none, linear, or exponential")
			return
		}
		if policy.MaxAttempts > 1 {
			job.RetryPolicy = policy
		}
	}

	// AUDIT L1 (fixed): record the caller's JWT subject so handleGetJob
	// can enforce per-job RBAC. Skipped when auth is disabled (dev mode).
	if s.tokenManager != nil {
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			job.SubmittedBy = claims.Subject
		}
	}
	// Feature 36 — stamp the fully-qualified Principal ID as the
	// resource's owner. SubmittedBy above keeps its legacy shape
	// (bare JWT subject) for back-compat with the AUDIT L1 RBAC
	// check in handleGetJob; OwnerPrincipal is the new authoritative
	// field feature 37's authz engine will consult.
	//
	// We stamp EVEN when auth is disabled (dev mode) — the resulting
	// ID is "anonymous", which feature 37 refuses for non-trivial
	// actions. Stamping late, after every validator has run, means
	// a rejected request never writes an owner we then have to
	// clean up.
	job.OwnerPrincipal = principal.FromContext(r.Context()).ID

	// Feature 37 — gate job submission on ActionWrite against a
	// Job resource owned by the caller. The owner check (rule 6)
	// trivially passes because we just stamped the caller as
	// owner, BUT the kind-based rules still apply:
	//   - Kind=node → denied (node JWTs cannot submit via REST).
	//     Closes a major feature-37 exploit vector: a compromised
	//     node's mTLS-derived JWT cannot stand up fake jobs on the
	//     coordinator.
	//   - Kind=anonymous → denied (no DisableAuth bypass reaches
	//     here; the middleware now stamps dev-admin instead).
	//   - Kind=service → denied unless the service has an
	//     ActionWrite/job allow in internal/authz/rules.go
	//     (today only workflow_runner + retry_loop do).
	if !s.authzCheck(w, r, authz.ActionWrite,
		authz.JobResource(job.ID, job.OwnerPrincipal, job.WorkflowID)) {
		return
	}

	// Feature 25 — dynamic-loader env-var denylist. Must run AFTER the
	// pure shape checks (validateSubmitRequest already handled count +
	// empty/NUL keys) and BEFORE the dry-run branch so a dry-run of a
	// denylisted submit still returns 400 — dry-run is not a validation-
	// skip probe. Per-node exceptions (HELION_ENV_DENYLIST_EXCEPTIONS)
	// are consulted via s.envDenylistCheck; each override fires its own
	// audit event so the escape hatch is visible.
	envCheck := s.envDenylistCheck(job.Env, job.NodeSelector)
	actor := actorFromContext(r.Context())
	if envCheck.Err != "" {
		if s.audit != nil {
			if err := s.audit.Log(r.Context(), audit.EventEnvDenylistReject, actor, map[string]interface{}{
				"job_id":      job.ID,
				"blocked_key": envCheck.BlockedKey,
				"dry_run":     dryRun,
			}); err != nil {
				logAuditErr(false, "env_denylist_reject", err)
			}
		}
		writeError(w, http.StatusBadRequest, envCheck.Err)
		return
	}
	for _, k := range envCheck.OverriddenKeys {
		if s.audit != nil {
			if err := s.audit.Log(r.Context(), audit.EventEnvDenylistOverride, actor, map[string]interface{}{
				"job_id":  job.ID,
				"env_key": k,
				"dry_run": dryRun,
			}); err != nil {
				logAuditErr(false, "env_denylist_override", err)
			}
		}
	}

	// Feature 24 — dry-run short-circuit. Every validator above has
	// already run; at this point we know the request would be
	// accepted on the real path. Skip Submit() + job_submit audit;
	// emit a distinct job_dry_run audit event + respond 200 with
	// `"dry_run": true` so the client can diff against the would-be
	// real response without any state change.
	if dryRun {
		if s.audit != nil {
			dryDetails := map[string]interface{}{
				"job_id":  job.ID,
				"command": job.Command,
			}
			if job.OwnerPrincipal != "" {
				dryDetails["resource_owner"] = job.OwnerPrincipal // Feature 36
			}
			if err := s.audit.Log(r.Context(), audit.EventJobDryRun, actor, stampOperatorCN(r.Context(), dryDetails)); err != nil {
				logAuditErr(false, "job.dry_run", err)
			}
		}
		s.recordSubmission(r, "job", job.ID, true, true, "") // feature 28
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeJSON(w, "handleSubmitJob.dry_run", dryRunResponse(jobToResponse(job)))
		return
	}

	if err := s.jobs.Submit(r.Context(), job); err != nil {
		// AUDIT M8 (fixed): duplicate job IDs return 409 Conflict rather than
		// silently overwriting the existing job or returning 500.
		if errors.Is(err, cluster.ErrJobExists) {
			writeError(w, http.StatusConflict, "job with this id already exists")
			return
		}
		slog.Error("job submit failed", slog.String("job_id", job.ID), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "job submission failed")
		return
	}

	// Phase 4: Log job submission to audit log. Feature 26 — include
	// the declared secret-key NAMES so a reviewer can see which env
	// vars the submitter marked secret. Values are never included.
	// Feature 27 — stampOperatorCN adds `operator_cn` when the
	// request arrived with a verified client cert (via TLS or
	// loopback-only Nginx headers).
	// Called via Log (not LogJobSubmit) so we can attach secret_keys
	// and operator_cn without changing the shared LogJobSubmit
	// interface that grpcserver / nodeserver / tests also implement.
	if s.audit != nil {
		details := map[string]interface{}{
			"job_id":  job.ID,
			"command": job.Command,
		}
		if sk := auditSafeSecretKeys(job.SecretKeys); len(sk) > 0 {
			details["secret_keys"] = sk
		}
		// Feature 36 — `resource_owner` is the principal ID the
		// action is acting ON (the job's owner). Distinct from
		// `actor` (who did it) because service-principals will
		// later perform state transitions on user-owned jobs.
		if job.OwnerPrincipal != "" {
			details["resource_owner"] = job.OwnerPrincipal
		}
		if err := s.audit.Log(r.Context(), audit.EventJobSubmit, actor, stampOperatorCN(r.Context(), details)); err != nil {
			logAuditErr(false, "job.submit", err)
		}
	}

	s.recordSubmission(r, "job", job.ID, false, true, "") // feature 28
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleSubmitJob", jobToResponse(job))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	// Go 1.22+ ServeMux pattern variables via r.PathValue.
	id := r.PathValue("id")
	if id == "" {
		// Fallback for older pattern parsing: extract from URL path.
		id = strings.TrimPrefix(r.URL.Path, "/jobs/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id required")
		return
	}

	job, err := s.jobs.Get(id)
	if err != nil {
		// AUDIT L4 (fixed): generic "job not found" — does not echo the ID back,
		// preventing enumeration information leakage.
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// Feature 37 — unified authz. Replaces the legacy AUDIT L1
	// `claims.Subject == job.SubmittedBy` check: same fail-closed
	// semantics (admin OR owner) but the decision goes through the
	// typed policy engine, emits EventAuthzDeny on refusal, and
	// returns a machine-readable deny code to clients. DisableAuth
	// stamps a dev-admin principal upstream, so the dev path still
	// passes this check without a bypass branch here.
	if !s.authzCheck(w, r, authz.ActionRead,
		authz.JobResource(job.ID, job.OwnerPrincipal, job.WorkflowID)) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetJob", jobToResponse(job))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Feature 37 — load the job BEFORE mutating so the authz
	// decision sees the authoritative OwnerPrincipal. Pre-feature-37
	// this endpoint had no per-job RBAC; any authenticated user
	// could cancel any job. The Load + Allow pattern matches
	// handleGetJob above.
	job, err := s.jobs.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if !s.authzCheck(w, r, authz.ActionCancel,
		authz.JobResource(job.ID, job.OwnerPrincipal, job.WorkflowID)) {
		return
	}

	if err := s.jobs.CancelJob(r.Context(), id, "cancelled via API"); err != nil {
		if errors.Is(err, cluster.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		if errors.Is(err, cluster.ErrJobAlreadyTerminal) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Error("cancel job failed", slog.String("job_id", id), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "job cancellation failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleCancelJob", map[string]interface{}{
		"id":      id,
		"status":  "cancelled",
		"message": "job cancelled successfully",
	})
}

// validJobStatuses is the set of status strings accepted by the ?status= query
// parameter. Values are uppercase to match the underlying store convention.
var validJobStatuses = map[string]bool{
	"UNKNOWN": true, "PENDING": true, "DISPATCHING": true, "RUNNING": true,
	"COMPLETED": true, "FAILED": true, "TIMEOUT": true, "LOST": true,
	"RETRYING": true, "SCHEDULED": true, "CANCELLED": true, "SKIPPED": true,
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	pageStr := r.URL.Query().Get("page")
	page := 1
	if pageStr != "" {
		p, err := strconv.Atoi(pageStr)
		if err != nil || p < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return
		}
		page = p
	}

	sizeStr := r.URL.Query().Get("size")
	size := 20
	if sizeStr != "" {
		sz, err := strconv.Atoi(sizeStr)
		if err != nil || sz < 1 || sz > 100 {
			writeError(w, http.StatusBadRequest, "size must be an integer between 1 and 100")
			return
		}
		size = sz
	}

	// AUDIT M6 (fixed): cap page number to prevent integer-overflow style
	// offset calculations that could OOM the coordinator.
	const maxPage = 10_000
	if page > maxPage {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("page must not exceed %d", maxPage))
		return
	}

	statusFilter := strings.ToUpper(r.URL.Query().Get("status"))
	if statusFilter != "" && !validJobStatuses[statusFilter] {
		writeError(w, http.StatusBadRequest, "invalid status filter")
		return
	}

	// Feature 37 — fetch the full status-filtered set, filter
	// per-row via authz.Allow(ActionRead), then paginate the
	// permitted subset. This matches the filter-in-memory
	// strategy the spec chose over a scope-push-down into the
	// store. At MVP scale (< ~1000 active jobs) the overhead is
	// negligible; a follow-up slice can wire a store-level
	// owner filter if a deployment hits the cliff.
	//
	// A non-admin caller sees exactly the jobs they own (or
	// share via feature 38 when it lands). Admin / dev-admin
	// see everything because authz.Allow short-circuits for
	// them. A deny does NOT emit EventAuthzDeny per-row — that
	// would flood the audit log with expected filtering
	// decisions; only handler-level denials (e.g. unauthorised
	// access to a single resource) are audited.
	allJobs, err := s.jobs.ListAll(r.Context(), statusFilter)
	if err != nil {
		slog.Error("list jobs failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	p := principal.FromContext(r.Context())
	permitted := make([]*cpb.Job, 0, len(allJobs))
	for _, job := range allJobs {
		if authz.Allow(p, authz.ActionRead,
			authz.JobResource(job.ID, job.OwnerPrincipal, job.WorkflowID)) == nil {
			permitted = append(permitted, job)
		}
	}
	total := len(permitted)

	// Paginate after filtering so the total reflects what the
	// caller is actually permitted to see.
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	window := permitted[start:end]

	jobResponses := make([]JobResponse, len(window))
	for i, job := range window {
		jobResponses[i] = jobToResponse(job)
	}

	resp := JobListResponse{
		Jobs:  jobResponses,
		Total: total,
		Page:  page,
		Size:  size,
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListJobs", resp)
}
