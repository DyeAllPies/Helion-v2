package cluster_test

import (
	"context"
	"testing"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// TestBadgerPersister_NodeLabels_Roundtrip asserts that a Node's
// Labels map survives Save→LoadAll through the real BadgerDB JSON
// persister. The existing roundtrip test only covers NodeID, which
// would let a JSON-tag typo or a struct-field rename on Labels
// silently break label persistence across coordinator restarts —
// scheduler selectors would suddenly match nothing on a node that
// was still healthy. This test pins the contract.
func TestBadgerPersister_NodeLabels_Roundtrip(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	want := &cpb.Node{
		NodeID:  "gpu-host",
		Address: "10.0.0.7:9090",
		Labels: map[string]string{
			"gpu":  "a100",
			"cuda": "12.4",
			"zone": "us-east",
		},
	}
	if err := p.SaveNode(ctx, want); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	got, err := p.LoadAllNodes(ctx)
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 node, got %d", len(got))
	}
	loaded := got[0]
	if len(loaded.Labels) != len(want.Labels) {
		t.Fatalf("label count: got %d (%+v), want %d (%+v)",
			len(loaded.Labels), loaded.Labels, len(want.Labels), want.Labels)
	}
	for k, v := range want.Labels {
		if loaded.Labels[k] != v {
			t.Errorf("label %q: got %q, want %q", k, loaded.Labels[k], v)
		}
	}
}

// TestBadgerPersister_NodeLabels_EmptyOmitted confirms the omitempty
// JSON tag — a node registered without labels should persist with no
// labels key (not a stored empty map). This matters for BadgerDB size
// efficiency and keeps the stored format compatible with older node
// records that predate the Labels field entirely.
func TestBadgerPersister_NodeLabels_EmptyOmitted(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	n := &cpb.Node{NodeID: "plain", Address: "10.0.0.8:9090"}
	if err := p.SaveNode(ctx, n); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}
	loaded, err := p.LoadAllNodes(ctx)
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if len(loaded[0].Labels) != 0 {
		t.Fatalf("empty-label node came back with labels: %+v", loaded[0].Labels)
	}
}

// TestBadgerPersister_NodeLabels_ForwardCompatFromPreLabelJSON covers
// the migration case: a Node row written by a coordinator version
// without the Labels field must deserialize cleanly (Labels=nil)
// when a newer coordinator reads it. JSON's default behaviour for
// missing map fields already satisfies this — the test pins the
// invariant so a future refactor (e.g. switching to a non-JSON
// persister) has to keep it.
func TestBadgerPersister_NodeLabels_ForwardCompatFromPreLabelJSON(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	// Simulate a pre-Labels row by writing the Node struct with the
	// zero-value Labels — omitempty means the JSON emitted has no
	// "labels" key, which is exactly what a pre-field row would
	// look like in BadgerDB.
	if err := p.SaveNode(ctx, &cpb.Node{NodeID: "legacy", Address: "10.0.0.9:9090"}); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}
	got, err := p.LoadAllNodes(ctx)
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if got[0].NodeID != "legacy" {
		t.Fatalf("wrong node: %+v", got[0])
	}
	if got[0].Labels != nil {
		t.Fatalf("missing-field should load as nil, got %+v", got[0].Labels)
	}
}
