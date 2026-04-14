package cluster_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// TestRegistry_PersistsLabels_OnFirstRegister asserts that labels
// supplied in RegisterRequest survive through to the persisted Node
// snapshot (which the scheduler later consumes for selector matches).
func TestRegistry_PersistsLabels_OnFirstRegister(t *testing.T) {
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()
	_, _ = r.Register(ctx, &pb.RegisterRequest{
		NodeId:  "gpu-1",
		Address: "127.0.0.1:9090",
		Labels: map[string]string{
			"gpu":  "a100",
			"cuda": "12.4",
			"zone": "us-east",
		},
	})

	nodes := r.Snapshot()
	var got *pb.RegisterResponse // appease unused-import guard in some configs
	_ = got
	if len(nodes) != 1 {
		t.Fatalf("nodes: %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Labels["gpu"] != "a100" || n.Labels["cuda"] != "12.4" || n.Labels["zone"] != "us-east" {
		t.Fatalf("labels: %+v", n.Labels)
	}
}

// TestRegistry_ReRegister_ReplacesLabels asserts that a node that
// re-registers with a different label set (e.g. new driver version
// bumped `cuda` from 11.8 to 12.4) sees its labels replaced, not
// merged — the scheduler must see the *current* report, not a stale
// union.
func TestRegistry_ReRegister_ReplacesLabels(t *testing.T) {
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()

	_, _ = r.Register(ctx, &pb.RegisterRequest{
		NodeId: "n", Address: "a:1",
		Labels: map[string]string{"cuda": "11.8", "deprecated": "yes"},
	})
	_, _ = r.Register(ctx, &pb.RegisterRequest{
		NodeId: "n", Address: "a:1",
		Labels: map[string]string{"cuda": "12.4"},
	})

	nodes := r.Snapshot()
	if len(nodes) != 1 {
		t.Fatalf("nodes: %d", len(nodes))
	}
	n := nodes[0]
	if n.Labels["cuda"] != "12.4" {
		t.Fatalf("cuda label not replaced: %q", n.Labels["cuda"])
	}
	if _, stale := n.Labels["deprecated"]; stale {
		t.Fatal("stale label survived re-registration")
	}
}

// TestRegistry_DropsInvalidLabels asserts the sanitiser:
//   - NUL in key or value → dropped
//   - '=' in key → dropped (would break env-var round-trips)
//   - empty key → dropped
//   - oversize key / value → dropped
// The node is still accepted; only the offending labels are stripped.
func TestRegistry_DropsInvalidLabels(t *testing.T) {
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()
	_, _ = r.Register(ctx, &pb.RegisterRequest{
		NodeId: "n", Address: "a:1",
		Labels: map[string]string{
			"":           "empty-key",
			"has\x00nul": "bad-key",
			"has=equal":  "bad-key",
			"\x01ctl":    "bad-key",
			"ok":         "ok-value",
			"bigkey":     strings.Repeat("v", 300), // value too long
			"goodkey":    "goodvalue",
		},
	})

	nodes := r.Snapshot()
	if len(nodes) != 1 {
		t.Fatalf("nodes: %d", len(nodes))
	}
	n := nodes[0]
	if n.Labels["ok"] != "ok-value" || n.Labels["goodkey"] != "goodvalue" {
		t.Fatalf("valid labels dropped: %+v", n.Labels)
	}
	for _, bad := range []string{"", "has\x00nul", "has=equal", "\x01ctl", "bigkey"} {
		if _, stuck := n.Labels[bad]; stuck {
			t.Errorf("bad label %q survived sanitisation", bad)
		}
	}
}

// TestRegistry_EmptyLabels_Accepted asserts a node with no labels is
// still fine (the majority of non-ML worker nodes will register empty).
func TestRegistry_EmptyLabels_Accepted(t *testing.T) {
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	_, err := r.Register(context.Background(), &pb.RegisterRequest{
		NodeId: "n", Address: "a:1",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	nodes := r.Snapshot()
	if len(nodes[0].Labels) != 0 {
		t.Fatalf("unexpected labels on empty register: %+v", nodes[0].Labels)
	}
}

// TestRegistry_AuditDetail_IncludesLabels verifies the node.registered
// audit line carries a sorted, shape-stable rendering of the labels
// map so operators can grep historical records for things like
// "labels=...gpu=a100..." — the primary way SECURITY.md §6 says the
// audit trail gets used. Uses MemPersister so we can read back the
// audit entries directly.
func TestRegistry_AuditDetail_IncludesLabels(t *testing.T) {
	p := cluster.NewMemPersister()
	r := cluster.NewRegistry(p, 500*time.Millisecond, nil)
	_, _ = r.Register(context.Background(), &pb.RegisterRequest{
		NodeId:  "gpu-host",
		Address: "10.0.0.7:9090",
		Labels:  map[string]string{"gpu": "a100", "cuda": "12.4"},
	})
	// Drain async audit writes; the registry uses appendAuditAsync.
	// The test helper MemPersister captures them under a lock we
	// can't reach from here, so wait briefly then Mu()/MuUnlock().
	time.Sleep(50 * time.Millisecond)

	p.Mu()
	defer p.MuUnlock()
	found := false
	for _, a := range p.Audits {
		if a["event_type"] != "node.registered" {
			continue
		}
		detail := a["detail"]
		// Keys rendered in sorted order → cuda before gpu.
		if !strings.Contains(detail, "labels={cuda=12.4,gpu=a100}") {
			t.Fatalf("expected sorted labels in audit detail, got: %q", detail)
		}
		found = true
	}
	if !found {
		t.Fatalf("no node.registered audit entry; got: %+v", p.Audits)
	}
}
