// internal/persistence/ttl_test.go
//
// PutWithTTL: expiry behaviour.

package persistence_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TestPutWithTTL verifies a value is readable before its TTL elapses and
// returns ErrNotFound afterwards.
//
// BadgerDB's TTL has 1-second resolution. We set TTL=1s and sleep 2s.
// Skip with -short for fast CI runs.
func TestPutWithTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TTL sleep test in -short mode")
	}

	s := openFresh(t)
	key := persistence.TokenKey("jti-ttl-test")

	if err := persistence.PutWithTTL(s, key, sv("ephemeral"), 1*time.Second); err != nil {
		t.Fatalf("PutWithTTL: %v", err)
	}

	// Readable immediately.
	got, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	if got.Value != "ephemeral" {
		t.Errorf("Get before expiry = %q, want %q", got.Value, "ephemeral")
	}

	time.Sleep(2 * time.Second)

	_, err = persistence.Get[*wrapperspb.StringValue](s, key)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Get after expiry: got %v, want ErrNotFound", err)
	}
}
