// internal/persistence/raw_test.go
//
// PutRaw / GetRaw tests for non-protobuf values (certificates).

package persistence_test

import (
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestPutRawGetRaw(t *testing.T) {
	s := openFresh(t)

	der := []byte{0x30, 0x82, 0x01, 0x0A, 0x02, 0x82}
	key := persistence.CertKey("node-001")

	if err := s.PutRaw(key, der); err != nil {
		t.Fatalf("PutRaw: %v", err)
	}
	got, err := s.GetRaw(key)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(got) != string(der) {
		t.Errorf("GetRaw = %x, want %x", got, der)
	}
}

func TestGetRawMissing(t *testing.T) {
	s := openFresh(t)
	_, err := s.GetRaw(persistence.CertKey("nobody"))
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("GetRaw missing key: got %v, want ErrNotFound", err)
	}
}

func TestRawAndProtoCoexist(t *testing.T) {
	s := openFresh(t)

	rawKey := persistence.CertKey("node-raw")
	protoKey := persistence.NodeKey("10.0.0.1:8080")

	if err := s.PutRaw(rawKey, []byte("cert-der")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, protoKey, sv("10.0.0.1:8080")); err != nil {
		t.Fatal(err)
	}

	raw, err := s.GetRaw(rawKey)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(raw) != "cert-der" {
		t.Errorf("GetRaw = %q, want %q", raw, "cert-der")
	}

	got, err := persistence.Get[*wrapperspb.StringValue](s, protoKey)
	if err != nil {
		t.Fatalf("Get proto: %v", err)
	}
	if got.Value != "10.0.0.1:8080" {
		t.Errorf("Get proto = %q, want %q", got.Value, "10.0.0.1:8080")
	}
}
