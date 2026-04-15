package cluster_test

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestServiceRegistry_UpsertGetDelete(t *testing.T) {
	r := cluster.NewServiceRegistry()
	if _, ok := r.Get("missing"); ok {
		t.Fatal("empty registry should miss on Get")
	}
	r.Upsert(cpb.ServiceEndpoint{
		JobID: "j1", NodeID: "n1", NodeAddress: "10.0.0.1:9090",
		Port: 8080, HealthPath: "/healthz", Ready: true,
	})
	ep, ok := r.Get("j1")
	if !ok || ep.NodeID != "n1" || ep.Port != 8080 {
		t.Fatalf("Get(j1) = %+v, ok=%v", ep, ok)
	}
	if ep.UpdatedAt.IsZero() {
		t.Error("Upsert should stamp UpdatedAt when zero")
	}
	r.Delete("j1")
	if _, ok := r.Get("j1"); ok {
		t.Fatal("Delete should have removed j1")
	}
	if r.Count() != 0 {
		t.Fatalf("Count after delete = %d", r.Count())
	}
}

func TestServiceRegistry_Upsert_IgnoresEmptyJobID(t *testing.T) {
	r := cluster.NewServiceRegistry()
	r.Upsert(cpb.ServiceEndpoint{JobID: "", NodeID: "n"})
	if r.Count() != 0 {
		t.Fatalf("empty job_id should be ignored, Count=%d", r.Count())
	}
}

func TestServiceRegistry_Upsert_KeepsCallerTime(t *testing.T) {
	r := cluster.NewServiceRegistry()
	ts := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	r.Upsert(cpb.ServiceEndpoint{JobID: "j", UpdatedAt: ts})
	ep, _ := r.Get("j")
	if !ep.UpdatedAt.Equal(ts) {
		t.Fatalf("UpdatedAt overwritten: got %v want %v", ep.UpdatedAt, ts)
	}
}
