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
		{cpb.JobStatusRetrying, "retrying"},
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
		cpb.JobStatusRetrying,
	}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("expected %s to not be terminal", s)
		}
	}
}

// ── Job.Env and Job.TimeoutSeconds ────────────────────────────────────────────

// ── WorkflowStatus.String ────────────────────────────────────────────────────

func TestWorkflowStatus_String_AllValues(t *testing.T) {
	cases := []struct {
		status cpb.WorkflowStatus
		want   string
	}{
		{cpb.WorkflowStatusPending, "pending"},
		{cpb.WorkflowStatusRunning, "running"},
		{cpb.WorkflowStatusCompleted, "completed"},
		{cpb.WorkflowStatusFailed, "failed"},
		{cpb.WorkflowStatusCancelled, "cancelled"},
		{cpb.WorkflowStatus(99), "unknown"},
	}
	for _, tc := range cases {
		got := tc.status.String()
		if got != tc.want {
			t.Errorf("WorkflowStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// ── WorkflowStatus.IsTerminal ────────────────────────────────────────────────

func TestWorkflowStatus_IsTerminal(t *testing.T) {
	terminals := []cpb.WorkflowStatus{
		cpb.WorkflowStatusCompleted,
		cpb.WorkflowStatusFailed,
		cpb.WorkflowStatusCancelled,
	}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}
	nonTerminals := []cpb.WorkflowStatus{
		cpb.WorkflowStatusPending,
		cpb.WorkflowStatusRunning,
	}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("expected %s to not be terminal", s)
		}
	}
}

// ── DependencyCondition.String ───────────────────────────────────────────────

func TestDependencyCondition_String_AllValues(t *testing.T) {
	cases := []struct {
		cond cpb.DependencyCondition
		want string
	}{
		{cpb.DependencyOnSuccess, "on_success"},
		{cpb.DependencyOnFailure, "on_failure"},
		{cpb.DependencyOnComplete, "on_complete"},
		{cpb.DependencyCondition(99), "unknown"},
	}
	for _, tc := range cases {
		got := tc.cond.String()
		if got != tc.want {
			t.Errorf("DependencyCondition(%d).String() = %q, want %q", tc.cond, got, tc.want)
		}
	}
}

// ── BackoffStrategy.String ────────────────────────────────────────────────────

func TestBackoffStrategy_String_AllValues(t *testing.T) {
	cases := []struct {
		s    cpb.BackoffStrategy
		want string
	}{
		{cpb.BackoffNone, "none"},
		{cpb.BackoffLinear, "linear"},
		{cpb.BackoffExponential, "exponential"},
		{cpb.BackoffStrategy(99), "unknown"},
	}
	for _, tc := range cases {
		got := tc.s.String()
		if got != tc.want {
			t.Errorf("BackoffStrategy(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// ── Job.Env and Job.TimeoutSeconds ────────────────────────────────────────────

func TestJob_EnvAndTimeout_DefaultToZeroValues(t *testing.T) {
	j := cpb.Job{ID: "j1", Command: "echo"}
	if len(j.Env) != 0 {
		t.Errorf("Env: want nil/empty map, got %v", j.Env)
	}
	if j.TimeoutSeconds != 0 {
		t.Errorf("TimeoutSeconds: want 0, got %d", j.TimeoutSeconds)
	}
}

func TestJob_EnvAndTimeout_CanBeSet(t *testing.T) {
	j := cpb.Job{
		ID:             "j2",
		Command:        "python3",
		Env:            map[string]string{"FOO": "bar", "WORKERS": "4"},
		TimeoutSeconds: 120,
	}
	if j.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO]: want 'bar', got %q", j.Env["FOO"])
	}
	if j.Env["WORKERS"] != "4" {
		t.Errorf("Env[WORKERS]: want '4', got %q", j.Env["WORKERS"])
	}
	if j.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds: want 120, got %d", j.TimeoutSeconds)
	}
}
