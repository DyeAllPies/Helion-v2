// internal/api/server_test.go
//
// Tests for Server.Serve and Server.Shutdown lifecycle.

package api_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// ── Serve / Shutdown ──────────────────────────────────────────────────────────

func TestServe_StartsAndShutdown_NoError(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)

	// Pick a free port by listening and immediately closing.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()

	// Give the server time to start.
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("Serve returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Serve did not return after Shutdown")
	}
}

func TestShutdown_NilHTTPSrv_ReturnsNil(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// Never called Serve, so httpSrv is nil.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown with nil httpSrv: %v", err)
	}
}
