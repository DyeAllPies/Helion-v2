// internal/nodeserver/service_prober_test.go
//
// Integration tests for feature-17's readiness probe state machine.
// The prober's edge-triggered emit logic (one event per unknown→ready
// or ready↔unhealthy flip, NEVER one per tick) is the invariant that
// bounds the coordinator's audit-log cardinality — a regression that
// fired on every tick would spam the bus and bloat the log with no
// new signal.
//
// These tests drive a real gRPC coordinator + a real grpcclient from
// the node side so the whole emission chain runs:
// probeService → emitServiceEvent → grpcclient.ReportServiceEvent →
// grpcserver handler → ServiceRegistry.Upsert + audit.LogServiceEvent.
// Covers both the prober state machine and the audit-log observer in
// one pass.

package nodeserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// proberMockAudit counts ServiceEvent audit calls so tests can assert
// edge-trigger cardinality directly (the service-events counter also
// doubles as the audit-log observer — a dropped LogServiceEvent call
// fails the same assertion).
type proberMockAudit struct{ serviceEvents atomic.Int32 }

func (m *proberMockAudit) LogJobSubmit(_ context.Context, _, _, _ string) error { return nil }
func (m *proberMockAudit) LogRateLimitHit(_ context.Context, _ string, _ float64) error {
	return nil
}
func (m *proberMockAudit) LogSecurityViolation(_ context.Context, _, _, _ string) error { return nil }
func (m *proberMockAudit) LogServiceEvent(_ context.Context, _, _ string, _ bool, _ uint32, _ string, _ uint32) error {
	m.serviceEvents.Add(1)
	return nil
}

// proberMockJobStore wires a single service job pinned to "prober-node"
// so the cross-node-poison check in the ReportServiceEvent handler
// sees a matching NodeID. Submit + Transition aren't used by the
// prober path but are required to satisfy grpcserver.JobStoreIface.
type proberMockJobStore struct{ jobs map[string]*cpb.Job }

func (m *proberMockJobStore) Get(id string) (*cpb.Job, error) {
	if j, ok := m.jobs[id]; ok {
		return j, nil
	}
	return nil, errors.New("not found")
}

func (m *proberMockJobStore) Submit(_ context.Context, j *cpb.Job) error {
	m.jobs[j.ID] = j
	return nil
}

func (m *proberMockJobStore) Transition(_ context.Context, _ string, _ cpb.JobStatus, _ cluster.TransitionOptions) error {
	return nil
}

// setupProberHarness spins up a coordinator gRPC server, a
// node-side grpcclient wired to it, and returns everything the
// tests need to invoke probeService directly. Caller is
// responsible for cancelling the returned context.
func setupProberHarness(t *testing.T) (*Server, *cluster.ServiceRegistry, *proberMockAudit, func()) {
	t.Helper()

	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	js := &proberMockJobStore{jobs: map[string]*cpb.Job{
		"svc-job": {ID: "svc-job", NodeID: "prober-node"},
	}}
	sr := cluster.NewServiceRegistry()
	al := &proberMockAudit{}

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithJobStore(js),
		grpcserver.WithServiceRegistry(sr),
		grpcserver.WithAuditLogger(al),
	)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	time.Sleep(40 * time.Millisecond)

	nb, err := auth.NewNodeBundle(coordBundle.CA, "prober-node")
	if err != nil {
		t.Fatalf("NewNodeBundle: %v", err)
	}
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("grpcclient.New: %v", err)
	}

	node := New(nil, nil, client, "prober-node", "go", slog.Default())
	node.SetAdvertiseAddress(addr)

	stop := func() {
		_ = client.Close()
		srv.Stop()
	}
	return node, sr, al, stop
}

// spec parses host:port back into port-only so probeService can
// target the loopback httptest server without the scheme prefix.
func portOf(t *testing.T, server *httptest.Server) uint32 {
	t.Helper()
	u := strings.TrimPrefix(server.URL, "http://")
	_, portStr, err := net.SplitHostPort(u)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return uint32(p)
}

// TestProbeService_HappyPath_EmitsSingleReadyEvent pins the core
// edge-triggering invariant: a service that stays ready across many
// ticks emits exactly ONE `service.ready` event, not one per tick.
// Without this, a regression that dropped the `if !haveLastReady ||
// ready != lastReady` guard at service_prober.go:103 would silently
// spam the audit log at every tick — the registry and dashboard
// would still show "ready", so no surface-level test would trip.
func TestProbeService_HappyPath_EmitsSingleReadyEvent(t *testing.T) {
	// Drop probeInterval to something fast so we get multiple ticks
	// in a short observation window.
	origInterval := probeInterval
	probeInterval = 25 * time.Millisecond
	t.Cleanup(func() { probeInterval = origInterval })

	// Health server always returns 200.
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(health.Close)

	node, sr, al, stop := setupProberHarness(t)
	t.Cleanup(stop)

	ctx, cancel := context.WithCancel(context.Background())
	spec := &pb.ServiceSpec{
		Port:            portOf(t, health),
		HealthPath:      "/",
		HealthInitialMs: 0,
	}
	done := make(chan struct{})
	go func() {
		node.probeService(ctx, "svc-job", spec)
		close(done)
	}()

	// Observe 5+ ticks worth (~150ms).
	time.Sleep(175 * time.Millisecond)
	cancel()
	<-done

	// Registry must show the service ready.
	ep, ok := sr.Get("svc-job")
	if !ok {
		t.Fatal("service-registry entry missing")
	}
	if !ep.Ready {
		t.Errorf("registry Ready: got false, want true")
	}

	// Exactly one event fired despite 5+ ticks. The assertion is
	// ==1 not <=1 so a regression that dropped the initial emit
	// (haveLastReady flag never trips) also fails.
	got := al.serviceEvents.Load()
	if got != 1 {
		t.Fatalf("edge-trigger broken: got %d events, want 1 across the observation window", got)
	}
}

// TestProbeService_ReadyToUnhealthyTransition_EmitsOnFlip covers the
// complementary transition — a service that was reachable stops
// being reachable. Asserts exactly 2 events (one ready, one
// unhealthy) even across many ticks in each state, and that the
// registry's Ready flag flipped to false.
func TestProbeService_ReadyToUnhealthyTransition_EmitsOnFlip(t *testing.T) {
	origInterval := probeInterval
	probeInterval = 25 * time.Millisecond
	t.Cleanup(func() { probeInterval = origInterval })

	// The health server starts healthy; after ~80ms it flips to
	// 500. An atomic flag drives the switch so the test doesn't
	// depend on the test goroutine vs. the server goroutine
	// scheduling.
	var unhealthy atomic.Bool
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if unhealthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(health.Close)

	node, sr, al, stop := setupProberHarness(t)
	t.Cleanup(stop)

	ctx, cancel := context.WithCancel(context.Background())
	spec := &pb.ServiceSpec{
		Port:       portOf(t, health),
		HealthPath: "/",
	}
	done := make(chan struct{})
	go func() {
		node.probeService(ctx, "svc-job", spec)
		close(done)
	}()

	// Wait for at least one ready tick.
	time.Sleep(80 * time.Millisecond)
	// Flip to unhealthy. Observe enough ticks for the transition
	// to propagate plus a few "stay unhealthy" ticks to prove we
	// don't re-emit.
	unhealthy.Store(true)
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	ep, ok := sr.Get("svc-job")
	if !ok {
		t.Fatal("service-registry entry missing")
	}
	if ep.Ready {
		t.Error("registry Ready: got true, want false after flip")
	}

	// Two transitions → 2 events. More would mean per-tick spam.
	got := al.serviceEvents.Load()
	if got != 2 {
		t.Fatalf("edge-trigger broken on flip: got %d events, want 2 (ready, unhealthy)", got)
	}
}
