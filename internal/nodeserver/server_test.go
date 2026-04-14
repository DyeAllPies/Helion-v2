package nodeserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// ── mock runtime ──────────────────────────────────────────────────────────────

type mockRuntime struct {
	mu      sync.Mutex
	result  runtime.RunResult
	err     error
	delay   time.Duration
	cancels map[string]context.CancelFunc
}

func newMock(result runtime.RunResult, err error) *mockRuntime {
	return &mockRuntime{result: result, err: err, cancels: make(map[string]context.CancelFunc)}
}

func (m *mockRuntime) Run(ctx context.Context, req runtime.RunRequest) (runtime.RunResult, error) {
	jctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancels[req.JobID] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, req.JobID)
		m.mu.Unlock()
		cancel()
	}()

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-jctx.Done():
			return runtime.RunResult{ExitCode: -1}, nil
		}
	}
	return m.result, m.err
}

func (m *mockRuntime) Cancel(jobID string) error {
	m.mu.Lock()
	cancel, ok := m.cancels[jobID]
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return nil
}

func (m *mockRuntime) Close() error { return nil }

// ── helpers ───────────────────────────────────────────────────────────────────

func newServer(rt runtime.Runtime) *Server {
	return New(rt, nil, nil, "test-node", "go", slog.Default())
}

// ── Dispatch tests ────────────────────────────────────────────────────────────

func TestDispatch_Success(t *testing.T) {
	rt := newMock(runtime.RunResult{ExitCode: 0, Stdout: []byte("ok")}, nil)
	srv := newServer(rt)

	ack, err := srv.Dispatch(context.Background(), &pb.DispatchRequest{
		JobId:   "j1",
		Command: "/bin/true",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !ack.Accepted {
		t.Errorf("expected Accepted=true, got error: %q", ack.Error)
	}
	if ack.JobId != "j1" {
		t.Errorf("job_id: got %q want %q", ack.JobId, "j1")
	}
}

func TestDispatch_RuntimeError(t *testing.T) {
	rt := newMock(runtime.RunResult{}, errors.New("exec failed"))
	srv := newServer(rt)

	ack, err := srv.Dispatch(context.Background(), &pb.DispatchRequest{
		JobId:   "j2",
		Command: "/bin/false",
	})
	if err != nil {
		t.Fatalf("Dispatch returned gRPC error: %v", err)
	}
	if ack.Accepted {
		t.Error("expected Accepted=false on runtime error")
	}
	if ack.Error == "" {
		t.Error("expected non-empty Error on runtime error")
	}
}

func TestDispatch_KillReasonPropagated(t *testing.T) {
	for _, reason := range []string{"OOMKilled", "Seccomp", "Timeout"} {
		t.Run(reason, func(t *testing.T) {
			rt := newMock(runtime.RunResult{ExitCode: -1, KillReason: reason}, nil)
			srv := newServer(rt)

			ack, err := srv.Dispatch(context.Background(), &pb.DispatchRequest{
				JobId:   "jkill",
				Command: "/bin/true",
			})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if ack.Error != reason {
				t.Errorf("Error field: got %q want %q", ack.Error, reason)
			}
		})
	}
}

func TestDispatch_MissingJobID(t *testing.T) {
	rt := newMock(runtime.RunResult{}, nil)
	srv := newServer(rt)

	_, err := srv.Dispatch(context.Background(), &pb.DispatchRequest{Command: "/bin/true"})
	if err == nil {
		t.Error("expected error for missing job_id")
	}
}

// TestDispatch_RunningJobsCounter verifies the atomic counter is correct
// during and after execution.
func TestDispatch_RunningJobsCounter(t *testing.T) {
	rt := &mockRuntime{
		delay:   50 * time.Millisecond,
		result:  runtime.RunResult{ExitCode: 0},
		cancels: make(map[string]context.CancelFunc),
	}
	srv := newServer(rt)

	if got := srv.RunningJobs(); got != 0 {
		t.Fatalf("initial RunningJobs: got %d want 0", got)
	}

	done := make(chan struct{})
	go func() {
		srv.Dispatch(context.Background(), &pb.DispatchRequest{JobId: "j-counter", Command: "cmd"}) //nolint:errcheck
		close(done)
	}()

	// Give the goroutine time to enter Run().
	time.Sleep(10 * time.Millisecond)
	if got := srv.RunningJobs(); got != 1 {
		t.Errorf("during RunningJobs: got %d want 1", got)
	}

	<-done
	if got := srv.RunningJobs(); got != 0 {
		t.Errorf("after RunningJobs: got %d want 0", got)
	}
}

// ── Cancel tests ──────────────────────────────────────────────────────────────

func TestCancel_RunningJob(t *testing.T) {
	rt := &mockRuntime{
		delay:   500 * time.Millisecond,
		result:  runtime.RunResult{ExitCode: 0},
		cancels: make(map[string]context.CancelFunc),
	}
	srv := newServer(rt)

	done := make(chan struct{})
	go func() {
		srv.Dispatch(context.Background(), &pb.DispatchRequest{JobId: "jcancel", Command: "cmd"}) //nolint:errcheck
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	ack, err := srv.Cancel(context.Background(), &pb.CancelRequest{JobId: "jcancel"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !ack.Ok {
		t.Error("expected Ok=true from Cancel")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("job did not terminate after Cancel")
	}
}

func TestCancel_MissingJobID(t *testing.T) {
	srv := newServer(newMock(runtime.RunResult{}, nil))
	_, err := srv.Cancel(context.Background(), &pb.CancelRequest{})
	if err == nil {
		t.Error("expected error for missing job_id")
	}
}

// failCancelRuntime always returns an error from Cancel.
type failCancelRuntime struct{ *mockRuntime }

func (f *failCancelRuntime) Cancel(_ string) error {
	return errors.New("runtime cancel failed")
}

func TestCancel_RuntimeError_ReturnsNotFound(t *testing.T) {
	rt := &failCancelRuntime{newMock(runtime.RunResult{}, nil)}
	srv := newServer(rt)

	_, err := srv.Cancel(context.Background(), &pb.CancelRequest{JobId: "nonexistent"})
	if err == nil {
		t.Error("expected error when runtime Cancel fails, got nil")
	}
}

// ── GetMetrics tests ──────────────────────────────────────────────────────────

func TestGetMetrics_Idle(t *testing.T) {
	srv := newServer(newMock(runtime.RunResult{}, nil))
	m, err := srv.GetMetrics(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if m.RunningJobs != 0 {
		t.Errorf("RunningJobs: got %d want 0", m.RunningJobs)
	}
	if m.TotalJobs != 0 {
		t.Errorf("TotalJobs: got %d want 0", m.TotalJobs)
	}
}

// ── reportResult (with non-nil client) ───────────────────────────────────────

func TestReportResult_WithFailingClient_LogsWarning(t *testing.T) {
	// Create a real node bundle + a client pointing to an unreachable address.
	// This exercises the reportResult code path after the nil-client early return.
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	nb, err := auth.NewNodeBundle(coordBundle.CA, "rr-node")
	if err != nil {
		t.Fatalf("NewNodeBundle: %v", err)
	}
	// Point to an address where nothing is listening — ReportResult will fail.
	client, err := grpcclient.New("127.0.0.1:1", "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("grpcclient.New: %v", err)
	}
	defer client.Close()

	rt := newMock(runtime.RunResult{ExitCode: 0}, nil)
	srv := New(rt, nil, client, "rr-node", "go", slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Dispatch runs the job, then calls reportResult which calls client.ReportResult.
	// The RPC will fail (nothing listening) and a warning will be logged.
	// The Dispatch itself should still succeed.
	ack, err := srv.Dispatch(ctx, &pb.DispatchRequest{JobId: "rr-job", Command: "/bin/true"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !ack.Accepted {
		t.Errorf("expected Accepted=true, got: %q", ack.Error)
	}
}

func TestGetMetrics_AfterJobs(t *testing.T) {
	rt := newMock(runtime.RunResult{ExitCode: 0}, nil)
	srv := newServer(rt)

	for i, id := range []string{"m1", "m2", "m3"} {
		srv.Dispatch(context.Background(), &pb.DispatchRequest{JobId: id, Command: "cmd"}) //nolint:errcheck
		_ = i
	}

	m, err := srv.GetMetrics(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if m.TotalJobs != 3 {
		t.Errorf("TotalJobs: got %d want 3", m.TotalJobs)
	}
}

// ── Limits forwarded to runtime ───────────────────────────────────────────────

// capturingRuntime records the last RunRequest so tests can inspect it.
type capturingRuntime struct {
	last runtime.RunRequest
}

func (c *capturingRuntime) Run(_ context.Context, req runtime.RunRequest) (runtime.RunResult, error) {
	c.last = req
	return runtime.RunResult{ExitCode: 0}, nil
}
func (c *capturingRuntime) Cancel(_ string) error { return nil }
func (c *capturingRuntime) Close() error          { return nil }

func TestDispatch_LimitsForwardedToRuntime(t *testing.T) {
	cap := &capturingRuntime{}
	srv := newServer(cap)

	_, err := srv.Dispatch(context.Background(), &pb.DispatchRequest{
		JobId:   "lim-job",
		Command: "stress",
		Limits: &pb.ResourceLimits{
			MemoryBytes: 536870912,
			CpuQuotaUs:  50000,
			CpuPeriodUs: 100000,
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if cap.last.Limits.MemoryBytes != 536870912 {
		t.Errorf("MemoryBytes: want 536870912, got %d", cap.last.Limits.MemoryBytes)
	}
	if cap.last.Limits.CPUQuotaUS != 50000 {
		t.Errorf("CPUQuotaUS: want 50000, got %d", cap.last.Limits.CPUQuotaUS)
	}
	if cap.last.Limits.CPUPeriodUS != 100000 {
		t.Errorf("CPUPeriodUS: want 100000, got %d", cap.last.Limits.CPUPeriodUS)
	}
}

func TestDispatch_NilLimits_NoRuntimePanic(t *testing.T) {
	cap := &capturingRuntime{}
	srv := newServer(cap)

	_, err := srv.Dispatch(context.Background(), &pb.DispatchRequest{
		JobId:   "nolim-job",
		Command: "echo",
		// Limits intentionally nil
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// All limit fields should be zero — no panic.
	lim := cap.last.Limits
	if lim.MemoryBytes != 0 || lim.CPUQuotaUS != 0 || lim.CPUPeriodUS != 0 {
		t.Errorf("expected zero limits, got %+v", lim)
	}
}
