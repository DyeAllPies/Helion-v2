// internal/persistence/crud_test.go
//
// Basic Put/Get/Delete round-trip tests.

package persistence_test

import (
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestPutGet(t *testing.T) {
	s := openFresh(t)

	want := sv("10.0.0.1:8080")
	key := persistence.NodeKey(want.Value)

	if err := persistence.Put(s, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !proto.Equal(want, got) {
		t.Errorf("Get = %q, want %q", got.Value, want.Value)
	}
}

func TestPutOverwrite(t *testing.T) {
	s := openFresh(t)
	key := persistence.NodeKey("10.0.0.1:8080")

	if err := persistence.Put(s, key, sv("first")); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := persistence.Put(s, key, sv("second")); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	got, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Value != "second" {
		t.Errorf("Get after overwrite = %q, want %q", got.Value, "second")
	}
}

func TestGetMissingKey(t *testing.T) {
	s := openFresh(t)
	_, err := persistence.Get[*wrapperspb.StringValue](s, persistence.NodeKey("ghost:9999"))
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Get missing key: got %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := openFresh(t)
	key := persistence.NodeKey("10.0.0.2:8080")

	if err := persistence.Put(s, key, sv("alive")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := persistence.Delete(s, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

func TestDeleteNonExistent(t *testing.T) {
	s := openFresh(t)
	if err := persistence.Delete(s, persistence.NodeKey("nobody:0")); err != nil {
		t.Errorf("Delete non-existent key: %v", err)
	}
}
