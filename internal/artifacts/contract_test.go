package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

// TestStoreContract_IdenticalAcrossBackends runs one Store-interface
// contract sequence over every backend that ships in this repo and
// asserts they agree on observable behaviour:
//
//	Put unique-key  → ok
//	Get unique-key  → exact payload
//	Stat            → Size + SHA-256 match
//	Delete          → ok
//	Get after delete  → ErrNotFound
//	Stat after delete → ErrNotFound
//	Delete twice    → ErrNotFound on second call
//	Put empty payload → ok; Get returns empty bytes
//
// The feature 11 surface has per-backend tests for each of the above
// already. What this test adds is an explicit **contract lock** — if
// a future refactor of `S3Store` starts returning a wrapped
// `fmt.Errorf` instead of `ErrNotFound`, or if `LocalStore` starts
// accepting empty payloads differently, the divergence fails here
// rather than being noticed only when the Stager's higher-level code
// behaves subtly wrong against one backend. The per-backend suites
// would still pass because each is self-consistent.
//
// The live-MinIO backend has its own integration test file and
// can't be covered here (it requires network + skip logic); the two
// code-level implementations (LocalStore + S3Store via fakeS3) are
// what differ in implementation and so what matters to pin.
func TestStoreContract_IdenticalAcrossBackends(t *testing.T) {
	backends := []struct {
		name string
		// open returns a fresh Store per-subtest; t.Cleanup handles
		// any teardown the backend needs.
		open func(t *testing.T) Store
	}{
		{
			name: "local",
			open: func(t *testing.T) Store {
				t.Helper()
				s, err := NewLocalStore(filepath.Join(t.TempDir(), "store"))
				if err != nil {
					t.Fatalf("NewLocalStore: %v", err)
				}
				return s
			},
		},
		{
			name: "s3-fake",
			open: func(t *testing.T) Store {
				t.Helper()
				return newS3StoreWithClient("helion", newFakeS3())
			},
		},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			s := b.open(t)
			ctx := context.Background()

			// Round-trip a non-empty payload and verify Stat agrees
			// with what Put said.
			payload := []byte("contract-check payload")
			uri, err := s.Put(ctx, "contract/roundtrip", bytes.NewReader(payload), int64(len(payload)))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}

			rc, err := s.Get(ctx, uri)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			got, _ := io.ReadAll(rc)
			_ = rc.Close()
			if !bytes.Equal(got, payload) {
				t.Fatalf("Get payload mismatch: got %q", got)
			}

			md, err := s.Stat(ctx, uri)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if md.Size != int64(len(payload)) || md.SHA256 != sha(payload) {
				t.Fatalf("Stat metadata: %+v", md)
			}

			// Delete and confirm Get + Stat both fall through to
			// ErrNotFound on both backends. This is the sentinel
			// each caller (Stager, validators, the registry) leans
			// on — divergence here silently breaks `errors.Is`
			// checks downstream.
			if err := s.Delete(ctx, uri); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := s.Get(ctx, uri); !errors.Is(err, ErrNotFound) {
				t.Errorf("Get after Delete: want ErrNotFound, got %v", err)
			}
			if _, err := s.Stat(ctx, uri); !errors.Is(err, ErrNotFound) {
				t.Errorf("Stat after Delete: want ErrNotFound, got %v", err)
			}

			// Delete of a missing key must report ErrNotFound on
			// both backends — the fakeS3 mirrors the doc-accepted
			// LocalStore behaviour, so a future S3 refactor that
			// silently swallowed 404s would drift the contract.
			if err := s.Delete(ctx, uri); !errors.Is(err, ErrNotFound) {
				t.Errorf("Delete missing: want ErrNotFound, got %v", err)
			}

			// Empty payload: both backends must accept it and both
			// must return empty bytes on Get. The Stat size is 0 on
			// both.
			emptyURI, err := s.Put(ctx, "contract/empty", bytes.NewReader(nil), 0)
			if err != nil {
				t.Fatalf("Put empty: %v", err)
			}
			rc, err = s.Get(ctx, emptyURI)
			if err != nil {
				t.Fatalf("Get empty: %v", err)
			}
			gotEmpty, _ := io.ReadAll(rc)
			_ = rc.Close()
			if len(gotEmpty) != 0 {
				t.Errorf("empty Get returned %d bytes", len(gotEmpty))
			}
			mdEmpty, err := s.Stat(ctx, emptyURI)
			if err != nil {
				t.Fatalf("Stat empty: %v", err)
			}
			if mdEmpty.Size != 0 {
				t.Errorf("empty Stat size: %d", mdEmpty.Size)
			}
			// SHA-256 of the empty input is the well-known constant.
			// Both backends must agree.
			if mdEmpty.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
				t.Errorf("empty SHA-256: got %q, want the well-known empty-hash constant", mdEmpty.SHA256)
			}
		})
	}
}
