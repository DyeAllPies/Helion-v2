package coordinatorpb_test

import (
	"testing"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── JobStatus.String ──────────────────────────────────────────────────────────

func TestJobStatus_String_AllValues(t *testing.T) {
	cases := []struct {
		status cpb.JobStatus
		want   string
	}{
		{cpb.JobStatusUnknown, "unknown"},
		{cpb.JobStatusPending, "pending"},
		{cpb.JobStatusDispatching, "dispatching"},
		{cpb.JobStatusRunning, "running"},
		{cpb.JobStatusCompleted, "completed"},
		{cpb.JobStatusFailed, "failed"},
		{cpb.JobStatusTimeout, "timeout"},
		{cpb.JobStatusLost, "lost"},
		{cpb.JobStatus(99), "unknown"}, // out-of-range → "unknown"
	}
	for _, tc := range cases {
		got := tc.status.String()
		if got != tc.want {
			t.Errorf("JobStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// ── JobStatus.IsTerminal ──────────────────────────────────────────────────────

func TestJobStatus_IsTerminal_TerminalStatuses(t *testing.T) {
	terminals := []cpb.JobStatus{
		cpb.JobStatusCompleted,
		cpb.JobStatusFailed,
		cpb.JobStatusTimeout,
		cpb.JobStatusLost,
	}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}
}

func TestJobStatus_IsTerminal_NonTerminalStatuses(t *testing.T) {
	nonTerminals := []cpb.JobStatus{
		cpb.JobStatusUnknown,
		cpb.JobStatusPending,
		cpb.JobStatusDispatching,
		cpb.JobStatusRunning,
	}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("expected %s to not be terminal", s)
		}
	}
}
