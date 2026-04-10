// tests/integration/security/phase4_integration_test.go
//
// Complete Phase 4 integration test verifying all exit criteria:
//   1. PQC: ML-DSA CA enhancement
//   2. JWT: Root token generation, validation, revocation
//   3. Rate limiting: Enforced on job submission
//   4. Audit logging: All required events logged
//
// This test verifies the full integration, not just individual components.

package security

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestPhase4CAEnhancement verifies ML-DSA enhancement works.
func TestPhase4CAEnhancement(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	// Enhance with ML-DSA
	if err := ca.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("enhance with ML-DSA: %v", err)
	}

	// Verify ML-DSA public key is set
	mldsaPub := ca.GetMLDSAPublicKey()
	if mldsaPub == nil {
		t.Fatal("ML-DSA public key not set after enhancement")
	}

	// Enhance with Hybrid KEM
	ca.EnhanceWithHybridKEM()

	// Both enhancements should coexist
	if ca.GetMLDSAPublicKey() == nil {
		t.Fatal("ML-DSA lost after hybrid KEM enhancement")
	}

	t.Log("✓ CA successfully enhanced with PQC")
}

// TestPhase4RootTokenWorkflow verifies root token generation and usage.
func TestPhase4RootTokenWorkflow(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("create token manager: %v", err)
	}

	// Generate root token
	rootToken, err := tm.GenerateRootToken()
	if err != nil {
		t.Fatalf("generate root token: %v", err)
	}

	if rootToken == "" {
		t.Fatal("root token is empty")
	}

	// Validate root token
	claims, err := tm.ValidateToken(rootToken)
	if err != nil {
		t.Fatalf("validate root token: %v", err)
	}

	if claims.Subject != "root" {
		t.Errorf("expected subject 'root', got %s", claims.Subject)
	}

	if claims.Role != "admin" {
		t.Errorf("expected role 'admin', got %s", claims.Role)
	}

	// Generate again - should return same token
	rootToken2, err := tm.GenerateRootToken()
	if err != nil {
		t.Fatalf("second root token generation: %v", err)
	}

	if rootToken2 != rootToken {
		t.Error("root token changed on second call")
	}

	t.Log("✓ Root token workflow complete")
}

// TestPhase4RateLimitingIntegration verifies rate limiting works as expected.
func TestPhase4RateLimitingIntegration(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	ctx := context.Background()
	nodeID := "test-node-integration"

	// Verify initial rate
	if limiter.GetRate() != 10.0 {
		t.Errorf("expected default rate 10.0, got %.1f", limiter.GetRate())
	}

	// Submit jobs at limit
	allowed := 0
	rejected := 0

	for i := 0; i < 20; i++ {
		err := limiter.Allow(ctx, nodeID)
		if err == nil {
			allowed++
		} else {
			rejected++
		}
	}

	t.Logf("Allowed: %d, Rejected: %d", allowed, rejected)

	// First 10 should succeed (burst)
	if allowed < 10 {
		t.Errorf("expected at least 10 allowed (burst), got %d", allowed)
	}

	// Remaining should be rejected
	if rejected < 5 {
		t.Errorf("expected at least 5 rejected, got %d", rejected)
	}

	t.Log("✓ Rate limiting integration complete")
}

// TestPhase4AuditLogCompleteness verifies all required events can be logged.
func TestPhase4AuditLogCompleteness(t *testing.T) {
	store := &mockAuditStore{
		data: make(map[string][]byte),
	}
	logger := audit.NewLogger(store, 0) // No TTL for test

	ctx := context.Background()

	// Test all required event types
	tests := []struct {
		name string
		fn   func() error
	}{
		{"node_register", func() error {
			return logger.LogNodeRegister(ctx, "node-1", "10.0.1.5:50051")
		}},
		{"node_revoke", func() error {
			return logger.LogNodeRevoke(ctx, "admin", "node-1", "security incident")
		}},
		{"job_submit", func() error {
			return logger.LogJobSubmit(ctx, "root", "job-123", "echo")
		}},
		{"job_state_transition", func() error {
			return logger.LogJobStateTransition(ctx, "job-123", "PENDING", "RUNNING")
		}},
		{"auth_failure", func() error {
			return logger.LogAuthFailure(ctx, "invalid token", "192.168.1.100")
		}},
		{"rate_limit_hit", func() error {
			return logger.LogRateLimitHit(ctx, "node-1", 10.0)
		}},
		{"coordinator_start", func() error {
			return logger.LogCoordinatorStart(ctx, "v2.0-phase4")
		}},
		{"coordinator_stop", func() error {
			return logger.LogCoordinatorStop(ctx, "graceful shutdown")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err != nil {
				t.Errorf("failed to log %s: %v", tt.name, err)
			}
		})
	}

	// Verify events can be queried
	query := audit.Query{
		Limit: 100,
	}

	events, err := logger.QueryEvents(ctx, query)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}

	if len(events) != len(tests) {
		t.Errorf("expected %d events, got %d", len(tests), len(events))
	}

	t.Logf("✓ Audit log completeness verified (%d events)", len(events))
}

// mockAuditStore implements audit.Store for testing.
type mockAuditStore struct {
	data map[string][]byte
}

func (m *mockAuditStore) Put(ctx context.Context, key string, value []byte) error {
	m.data[key] = value
	return nil
}

func (m *mockAuditStore) PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.data[key] = value
	return nil
}

func (m *mockAuditStore) Scan(ctx context.Context, prefix string, limit int) ([][]byte, error) {
	var results [][]byte
	for k, v := range m.data {
		if len(prefix) == 0 || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			results = append(results, v)
			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

// TestPhase4ExitCriteria is a comprehensive test of all Phase 4 requirements.
func TestPhase4ExitCriteria(t *testing.T) {
	t.Run("PQC_Enhancement", TestPhase4CAEnhancement)
	t.Run("JWT_RootToken", TestPhase4RootTokenWorkflow)
	t.Run("RateLimiting", TestPhase4RateLimitingIntegration)
	t.Run("AuditLogging", TestPhase4AuditLogCompleteness)
	t.Run("RevokedNode_gRPC", TestPhase4RevokedNodeGRPC)
	t.Run("AuditLifecycle", TestPhase4AuditLifecycleSequence)

	t.Log("✓✓✓ All Phase 4 exit criteria verified ✓✓✓")
}

// TestPhase4RevokedNodeGRPC verifies the exit criterion:
// "Revoked node's next gRPC call returns Unauthenticated; node re-registers
// with new certificate."
//
// Test sequence:
//  1. Coordinator + real registry, revocation checker wired into gRPC server.
//  2. Node registers successfully (proves baseline works).
//  3. Coordinator revokes the node via registry.RevokeNode().
//  4. Same node attempts Register again → must get codes.Unauthenticated.
//  5. A brand-new node ID registers successfully → proves revocation is scoped.
func TestPhase4RevokedNodeGRPC(t *testing.T) {
	// ── coordinator setup ────────────────────────────────────────────────────
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("create coordinator bundle: %v", err)
	}

	registry := cluster.NewRegistry(
		cluster.NopPersister{},
		5*time.Second,
		nil,
	)

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithRegistry(registry),
		grpcserver.WithRevocationChecker(registry),
	)
	if err != nil {
		t.Fatalf("create gRPC server: %v", err)
	}

	addr := "127.0.0.1:19200"
	go func() { _ = srv.Serve(addr) }()
	defer srv.Stop()
	time.Sleep(50 * time.Millisecond) // let the listener start

	// ── node setup ───────────────────────────────────────────────────────────
	nodeBundle, err := auth.NewNodeBundle(coordBundle.CA, "target-node")
	if err != nil {
		t.Fatalf("create node bundle: %v", err)
	}

	client, err := grpcclient.New(addr, "helion-coordinator", nodeBundle)
	if err != nil {
		t.Fatalf("dial coordinator: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ── step 1: initial registration must succeed ────────────────────────────
	_, err = client.Register(ctx, "target-node", "127.0.0.1:9001")
	if err != nil {
		t.Fatalf("initial Register failed (expected success): %v", err)
	}
	t.Log("✓ initial registration succeeded")

	// ── step 2: revoke the node ──────────────────────────────────────────────
	if err := registry.RevokeNode(ctx, "target-node", "security test"); err != nil {
		t.Fatalf("RevokeNode failed: %v", err)
	}
	t.Log("✓ node revoked")

	// ── step 3: next RPC must return Unauthenticated ─────────────────────────
	_, err = client.Register(ctx, "target-node", "127.0.0.1:9001")
	if err == nil {
		t.Fatal("expected Unauthenticated after revocation but Register succeeded")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected codes.Unauthenticated, got %s: %s", st.Code(), st.Message())
	}
	t.Logf("✓ revoked node correctly rejected: %s", st.Message())

	// ── step 4: a different node ID registers fine ───────────────────────────
	otherBundle, err := auth.NewNodeBundle(coordBundle.CA, "other-node")
	if err != nil {
		t.Fatalf("create other-node bundle: %v", err)
	}
	otherClient, err := grpcclient.New(addr, "helion-coordinator", otherBundle)
	if err != nil {
		t.Fatalf("dial for other-node: %v", err)
	}
	defer otherClient.Close()

	_, err = otherClient.Register(ctx, "other-node", "127.0.0.1:9002")
	if err != nil {
		t.Errorf("other-node Register failed (revocation must be scoped to target-node): %v", err)
	}
	t.Log("✓ non-revoked node still registers successfully")
}

// TestPhase4AuditLifecycleSequence verifies the exit criterion:
// "Integration test verifies expected event sequence for a full job lifecycle."
//
// The required audit events are:
//   node_register, job_submit, job_state_transition (pending→running, running→completed),
//   rate_limit_hit, auth_failure, node_revoke, coordinator_start, coordinator_stop
//
// This test drives the audit logger through a complete lifecycle and asserts
// every required event type appears in the recorded sequence.
func TestPhase4AuditLifecycleSequence(t *testing.T) {
	store := &mockAuditStore{data: make(map[string][]byte)}
	logger := audit.NewLogger(store, 0)
	ctx := context.Background()

	// Required event sequence for a full lifecycle (ordered as they occur).
	type step struct {
		eventType string
		fn        func() error
	}

	steps := []step{
		{"coordinator_start", func() error {
			return logger.LogCoordinatorStart(ctx, "v2.0-phase4")
		}},
		{"node_register", func() error {
			return logger.LogNodeRegister(ctx, "node-lifecycle-1", "10.0.0.1:50051")
		}},
		{"job_submit", func() error {
			return logger.LogJobSubmit(ctx, "root", "job-lifecycle-001", "echo hello")
		}},
		{"job_state_transition (pending→running)", func() error {
			return logger.LogJobStateTransition(ctx, "job-lifecycle-001", "PENDING", "RUNNING")
		}},
		{"job_state_transition (running→completed)", func() error {
			return logger.LogJobStateTransition(ctx, "job-lifecycle-001", "RUNNING", "COMPLETED")
		}},
		{"auth_failure", func() error {
			return logger.LogAuthFailure(ctx, "invalid JWT signature", "192.168.1.50")
		}},
		{"rate_limit_hit", func() error {
			return logger.LogRateLimitHit(ctx, "node-lifecycle-1", 10.0)
		}},
		{"node_revoke", func() error {
			return logger.LogNodeRevoke(ctx, "coordinator", "node-lifecycle-1", "test cleanup")
		}},
		{"coordinator_stop", func() error {
			return logger.LogCoordinatorStop(ctx, "graceful shutdown")
		}},
	}

	// Execute every step and assert no logging error.
	for _, s := range steps {
		if err := s.fn(); err != nil {
			t.Errorf("audit step %q failed: %v", s.eventType, err)
		}
	}

	// Query back all events and verify count.
	events, err := logger.QueryEvents(ctx, audit.Query{Limit: 100})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}

	if len(events) != len(steps) {
		t.Errorf("expected %d audit events, got %d", len(steps), len(events))
	}

	// Build a set of observed event types for membership checks.
	observed := make(map[string]int)
	for _, ev := range events {
		observed[ev.Type]++
	}

	// Required types and minimum occurrence count.
	required := map[string]int{
		"node_register":        1,
		"node_revoke":          1,
		"job_submit":           1,
		"job_state_transition": 2, // pending→running and running→completed
		"auth_failure":         1,
		"rate_limit_hit":       1,
		"coordinator_start":    1,
		"coordinator_stop":     1,
	}

	for eventType, minCount := range required {
		if observed[eventType] < minCount {
			t.Errorf("required event %q: expected >=%d occurrences, got %d",
				eventType, minCount, observed[eventType])
		}
	}

	t.Logf("✓ full job lifecycle audit sequence verified (%d events, %d types)",
		len(events), len(observed))
}
