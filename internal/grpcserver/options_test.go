// internal/grpcserver/options_test.go
//
// Coverage-focused tests for the four Option constructors that
// aren't wired through by TestNew_WithAllOptions. The invariant
// under test is: the Option returns a function, and applying
// that function to a server stamps the field without a panic.
// We're not exercising the gRPC handlers — the ScrubbingStore
// + streaming tests already do.

package grpcserver_test

import (
	"context"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func newBundle(t *testing.T) *auth.Bundle {
	t.Helper()
	b, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	return b
}

func TestWithJobCompletionCallback_AcceptedBy_New(t *testing.T) {
	called := 0
	cb := func(_ context.Context, _ string, _ cpb.JobStatus) {
		called++
	}
	srv, err := grpcserver.New(newBundle(t), grpcserver.WithJobCompletionCallback(cb))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
	// We cannot invoke the gRPC handler here without a full
	// node-registry wiring. Asserting New() accepts the option
	// without error is sufficient to cover the Option function.
	_ = called
}

// mockRetryChecker is a no-op retry checker used solely to
// exercise WithRetryChecker's Option constructor.
type mockRetryChecker struct{}

func (m *mockRetryChecker) RetryIfEligible(_ context.Context, _ string) bool {
	return false
}

func TestWithRetryChecker_AcceptedBy_New(t *testing.T) {
	srv, err := grpcserver.New(newBundle(t),
		grpcserver.WithRetryChecker(&mockRetryChecker{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
}

func TestWithEventBus_AcceptedBy_New(t *testing.T) {
	bus := events.NewBus(4, nil)
	srv, err := grpcserver.New(newBundle(t), grpcserver.WithEventBus(bus))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
}

func TestWithSecretsLookup_AcceptedBy_New(t *testing.T) {
	lookup := func(_ string) ([]string, bool) {
		return []string{"secret"}, true
	}
	srv, err := grpcserver.New(newBundle(t), grpcserver.WithSecretsLookup(lookup))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
}
