package logstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/logstore"
)

func TestMemLogStore_AppendAndGet(t *testing.T) {
	s := logstore.NewMemLogStore()
	ctx := context.Background()

	_ = s.Append(ctx, logstore.LogEntry{JobID: "j1", Seq: 1, Data: "hello", Timestamp: time.Now()})
	_ = s.Append(ctx, logstore.LogEntry{JobID: "j1", Seq: 2, Data: "world", Timestamp: time.Now()})

	entries, err := s.Get(ctx, "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Data != "hello" {
		t.Errorf("first entry = %q, want hello", entries[0].Data)
	}
	if entries[1].Data != "world" {
		t.Errorf("second entry = %q, want world", entries[1].Data)
	}
}

func TestMemLogStore_GetEmpty(t *testing.T) {
	s := logstore.NewMemLogStore()
	entries, err := s.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestMemLogStore_SeparateJobs(t *testing.T) {
	s := logstore.NewMemLogStore()
	ctx := context.Background()

	_ = s.Append(ctx, logstore.LogEntry{JobID: "j1", Seq: 1, Data: "j1-data"})
	_ = s.Append(ctx, logstore.LogEntry{JobID: "j2", Seq: 1, Data: "j2-data"})

	e1, _ := s.Get(ctx, "j1")
	e2, _ := s.Get(ctx, "j2")

	if len(e1) != 1 || e1[0].Data != "j1-data" {
		t.Errorf("j1 entries wrong: %v", e1)
	}
	if len(e2) != 1 || e2[0].Data != "j2-data" {
		t.Errorf("j2 entries wrong: %v", e2)
	}
}
