// internal/persistence/list_test.go
//
// Prefix-scan tests for List[T].

package persistence_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestList(t *testing.T) {
	s := openFresh(t)

	addrs := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}
	for _, addr := range addrs {
		if err := persistence.Put(s, persistence.NodeKey(addr), sv(addr)); err != nil {
			t.Fatalf("Put node %q: %v", addr, err)
		}
	}
	// Noise: a job entry must NOT appear in the nodes scan.
	if err := persistence.Put(s, persistence.JobKey("job-001"), sv("job-001")); err != nil {
		t.Fatalf("Put job: %v", err)
	}

	nodes, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != len(addrs) {
		t.Fatalf("List: got %d nodes, want %d", len(nodes), len(addrs))
	}

	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		seen[n.Value] = true
	}
	for _, addr := range addrs {
		if !seen[addr] {
			t.Errorf("List: missing %q", addr)
		}
	}
}

func TestListEmptyPrefix(t *testing.T) {
	s := openFresh(t)
	result, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatalf("List on empty prefix: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("List on empty prefix: got %d results, want 0", len(result))
	}
}

func TestListPrefixIsolation(t *testing.T) {
	s := openFresh(t)

	if err := persistence.Put(s, persistence.NodeKey("n1"), sv("n1")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, persistence.JobKey("j1"), sv("j1")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, persistence.JobKey("j2"), sv("j2")); err != nil {
		t.Fatal(err)
	}

	nodes, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes scan: got %d, want 1", len(nodes))
	}

	jobs, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixJobs))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Errorf("jobs scan: got %d, want 2", len(jobs))
	}
}
