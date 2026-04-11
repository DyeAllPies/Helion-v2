// internal/persistence/audit_test.go
//
// AppendAudit + AuditKey ordering / isolation tests.

package persistence_test

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestAppendAuditOrdering(t *testing.T) {
	s := openFresh(t)

	if err := persistence.AppendAudit(s, "evt-1", sv("node.registered")); err != nil {
		t.Fatalf("AppendAudit e1: %v", err)
	}
	// Guarantee distinct nanosecond timestamps.
	time.Sleep(time.Millisecond)
	if err := persistence.AppendAudit(s, "evt-2", sv("job.dispatched")); err != nil {
		t.Fatalf("AppendAudit e2: %v", err)
	}

	events, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixAudit))
	if err != nil {
		t.Fatalf("List audit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("List audit: got %d, want 2", len(events))
	}
	if events[0].Value != "node.registered" {
		t.Errorf("events[0] = %q, want %q", events[0].Value, "node.registered")
	}
	if events[1].Value != "job.dispatched" {
		t.Errorf("events[1] = %q, want %q", events[1].Value, "job.dispatched")
	}
}

func TestAuditKeyOrdering(t *testing.T) {
	earlier := persistence.AuditKey(1_000_000_000, "a")
	later := persistence.AuditKey(2_000_000_000, "b")
	if string(earlier) >= string(later) {
		t.Errorf("AuditKey ordering violated:\n  earlier: %q\n  later:   %q",
			earlier, later)
	}
}

func TestAuditPrefixIsolation(t *testing.T) {
	s := openFresh(t)

	if err := persistence.AppendAudit(s, "evt", sv("event")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, persistence.NodeKey("n1"), sv("n1")); err != nil {
		t.Fatal(err)
	}

	nodes, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes after AppendAudit: got %d, want 1", len(nodes))
	}
}
