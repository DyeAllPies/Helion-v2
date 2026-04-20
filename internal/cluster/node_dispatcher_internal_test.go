// internal/cluster/node_dispatcher_internal_test.go
//
// Pure-function tests for NodeDispatcher helpers that don't
// require a real node (bindingsToProto) + NewGRPCNodeDispatcher
// constructor sanity check.

package cluster

import (
	"crypto/tls"
	"testing"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestNewGRPCNodeDispatcher_StampsTLSConfig(t *testing.T) {
	cfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only config
	d := NewGRPCNodeDispatcher(cfg)
	if d == nil {
		t.Fatal("nil dispatcher")
	}
	if d.tlsCfg != cfg {
		t.Error("tls config not stamped")
	}
}

func TestBindingsToProto_EmptyInput_NilOutput(t *testing.T) {
	// Invariant: nil/empty in → nil out, so a job with no
	// inputs produces an absent (not empty) repeated field on
	// the wire.
	if got := bindingsToProto(nil); got != nil {
		t.Errorf("nil in: got %v, want nil", got)
	}
	if got := bindingsToProto([]cpb.ArtifactBinding{}); got != nil {
		t.Errorf("empty in: got %v, want nil", got)
	}
}

func TestBindingsToProto_FieldsForwarded(t *testing.T) {
	in := []cpb.ArtifactBinding{
		{
			Name:      "input-0",
			URI:       "artifacts://abc",
			LocalPath: "data/input.bin",
			SHA256:    "deadbeef",
		},
		{
			Name:      "output-0",
			URI:       "artifacts://xyz",
			LocalPath: "out.bin",
		},
	}
	got := bindingsToProto(in)
	if len(got) != 2 {
		t.Fatalf("length: got %d, want 2", len(got))
	}
	if got[0].Name != "input-0" || got[0].Uri != "artifacts://abc" ||
		got[0].LocalPath != "data/input.bin" || got[0].Sha256 != "deadbeef" {
		t.Errorf("binding[0] not forwarded verbatim: %+v", got[0])
	}
	if got[1].Sha256 != "" {
		t.Errorf("binding[1] sha256 must stay empty when not set: got %q", got[1].Sha256)
	}
}

// ── dispatchRPCTimeout ──────────────────────────────────────

func TestDispatchRPCTimeout_ServiceJob_FloorTimeout(t *testing.T) {
	job := &cpb.Job{
		ID:      "svc-1",
		Service: &cpb.ServiceSpec{Port: 8080},
	}
	got := dispatchRPCTimeout(job)
	if got != minDispatchRPCTimeout {
		t.Errorf("service job: got %v, want %v", got, minDispatchRPCTimeout)
	}
}

func TestDispatchRPCTimeout_NoDeclaredTimeout_FloorTimeout(t *testing.T) {
	job := &cpb.Job{ID: "j-1"}
	got := dispatchRPCTimeout(job)
	if got != minDispatchRPCTimeout {
		t.Errorf("no-timeout job: got %v, want %v", got, minDispatchRPCTimeout)
	}
}

func TestDispatchRPCTimeout_BatchJob_TimeoutPlusBuffer(t *testing.T) {
	job := &cpb.Job{ID: "j-1", TimeoutSeconds: 120}
	got := dispatchRPCTimeout(job)
	// 120s + 30s buffer = 150s
	if got.Seconds() != 150 {
		t.Errorf("batch job: got %v seconds, want 150", got.Seconds())
	}
}
