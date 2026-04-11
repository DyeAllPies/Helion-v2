// internal/persistence/testhelpers_test.go
//
// Shared helpers for the persistence test suite.
//
// Proto types used in tests
// ─────────────────────────
// The tests use *wrapperspb.StringValue — a real, fully-registered proto
// message from google.golang.org/protobuf/types/known/wrapperspb. It
// serialises with proto.Marshal / proto.Unmarshal exactly as the real
// helionpb types do, so test assertions translate 1:1 once the helionpb
// stubs are wired in.

package persistence_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// silence "imported and not used" when proto is only referenced via generic
// type parameters (which `go vet` treats as an import-use).
var _ = proto.Marshal

// openFresh opens a Store in a unique temp directory and registers a Cleanup
// that closes it after the test.
func openFresh(t *testing.T) *persistence.Store {
	t.Helper()
	s, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// sv is a shorthand for wrapperspb.String — our stand-in proto value.
func sv(v string) *wrapperspb.StringValue { return wrapperspb.String(v) }
