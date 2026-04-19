// internal/api/analytics_publisher.go
//
// Feature 28 — helpers that emit events.Bus events from HTTP
// handlers. Kept in one file so a reviewer tracking "where does
// analytics get its data from?" has a single grep target.
//
// Every helper is a no-op when the Server has no eventBus wired
// (`s.eventBus == nil`) so unit tests that don't construct a bus
// continue to work without extra setup.
//
// Design:
//   - Each helper takes the request so it can inspect headers +
//     context (operator CN, User-Agent, RemoteAddr).
//   - None of these helpers mutate audit state. The audit log is
//     the forever-record; these publishers feed the analytics
//     store only. A missing publisher call never compromises
//     accountability — the audit event still went out.
//   - Helpers never return errors. A full buffer / dropped event
//     is acceptable — `events.Bus.Publish` is non-blocking and
//     the analytics store is "best effort" by design. Hard-
//     failing a submit path because analytics couldn't record it
//     is the wrong trade.

package api

import (
	"net/http"
	"strings"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// submissionSourceFromUA parses the User-Agent to classify the
// submitter. The dashboard (feature 22) sends a distinctive UA
// prefix; helion-run + helion-issue-op-cert CLIs send "helion-*";
// CI agents set their own. Unknown UAs fall back to SubmissionSourceUnknown.
func submissionSourceFromUA(ua string) string {
	ua = strings.ToLower(strings.TrimSpace(ua))
	switch {
	case ua == "":
		return events.SubmissionSourceUnknown
	case strings.Contains(ua, "helion-dashboard"), strings.Contains(ua, "mozilla"):
		return events.SubmissionSourceDashboard
	case strings.HasPrefix(ua, "helion-"):
		return events.SubmissionSourceCLI
	case strings.Contains(ua, "github-actions"),
		strings.Contains(ua, "gitlab-runner"),
		strings.Contains(ua, "jenkins"),
		strings.Contains(ua, "buildkite"):
		return events.SubmissionSourceCI
	default:
		return events.SubmissionSourceUnknown
	}
}

// truncateUA caps the User-Agent at 256 bytes before it enters
// the analytics pipeline. The sink truncates too (defence in
// depth) but we cap here so a pathological UA doesn't bloat the
// event in the in-memory buffer.
func truncateUA(ua string) string {
	const max = 256
	if len(ua) <= max {
		return ua
	}
	return ua[:max]
}

// recordSubmission publishes submission.recorded onto the event
// bus. Called from POST /jobs and POST /workflows on every
// terminal outcome (accept, reject, dry-run).
//
// rejectReason is empty on accept, the user-facing validator
// message on reject. The sink truncates it client-side before the
// DB insert so a novel-length error string doesn't break the row.
func (s *Server) recordSubmission(r *http.Request, kind, resourceID string, dryRun, accepted bool, rejectReason string) {
	if s.eventBus == nil {
		return
	}
	actor := actorFromContext(r.Context())
	operatorCN := OperatorCNFromContext(r.Context())
	source := submissionSourceFromUA(r.Header.Get("User-Agent"))
	ua := truncateUA(r.Header.Get("User-Agent"))
	s.eventBus.Publish(events.SubmissionRecorded(
		actor, operatorCN, source, kind, resourceID,
		dryRun, accepted, rejectReason, ua,
	))
}

// recordAuthOK publishes an auth.ok event on a successful auth
// middleware pass. Called with a minimal payload — the sink's
// upsertAuthEvent does the PII hashing.
func (s *Server) recordAuthOK(r *http.Request, actor string) {
	if s.eventBus == nil {
		return
	}
	s.eventBus.Publish(events.AuthOK(actor, r.RemoteAddr, truncateUA(r.Header.Get("User-Agent"))))
}

// recordAuthFail publishes an auth.fail event. reason is one of
// the events.AuthFailReason* constants. actor is best-effort — on
// missing_token we don't know who tried.
func (s *Server) recordAuthFail(r *http.Request, reason, actor string) {
	if s.eventBus == nil {
		return
	}
	s.eventBus.Publish(events.AuthFail(reason, actor, r.RemoteAddr, truncateUA(r.Header.Get("User-Agent"))))
}

// recordAuthRateLimit publishes an auth.rate_limit event when a
// per-subject limiter returns 429. path distinguishes admin
// limiters from analytics-query limiters so the dashboard panel
// can break them out.
func (s *Server) recordAuthRateLimit(r *http.Request, actor, path string) {
	if s.eventBus == nil {
		return
	}
	s.eventBus.Publish(events.AuthRateLimit(actor, path, r.RemoteAddr))
}

// recordTokenMint publishes an auth.token_mint event on
// POST /admin/tokens after the token is issued. issuedBy is the
// admin who called the endpoint; subject + role + ttlHours
// describe the newly-minted token.
func (s *Server) recordTokenMint(issuedBy, subject, role string, ttlHours int) {
	if s.eventBus == nil {
		return
	}
	s.eventBus.Publish(events.AuthTokenMint(issuedBy, subject, role, ttlHours))
}
