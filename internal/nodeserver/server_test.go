package nodeserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"log/slog"
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
	return New(rt, nil, "test-node", slog.Default())
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
