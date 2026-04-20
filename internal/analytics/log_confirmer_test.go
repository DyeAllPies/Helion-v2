// internal/analytics/log_confirmer_test.go
//
// Coverage for NewLogConfirmer + ConfirmLogBatch. Uses the
// queryFn hook on mockConn to inject behaviour.

package analytics

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/logstore"
	"github.com/jackc/pgx/v5"
)

// ── NewLogConfirmer ─────────────────────────────────────────

func TestNewLogConfirmer_ReturnsNonNil(t *testing.T) {
	mc := newMockConn()
	c := NewLogConfirmer(mc)
	if c == nil {
		t.Fatal("NewLogConfirmer returned nil")
	}
}

// ── ConfirmLogBatch ─────────────────────────────────────────

func TestConfirmLogBatch_EmptyCandidates_EmptyMap(t *testing.T) {
	// Empty input short-circuits without a DB round-trip.
	mc := newMockConn()
	c := NewLogConfirmer(mc)
	got, err := c.ConfirmLogBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty batch: got %d, want 0", len(got))
	}
}

func TestConfirmLogBatch_QueryError_Propagates(t *testing.T) {
	mc := newMockConn()
	mc.queryFn = func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return nil, errors.New("pg down")
	}
	c := NewLogConfirmer(mc)
	_, err := c.ConfirmLogBatch(context.Background(), []logstore.LogKey{
		{JobID: "j-1", Seq: 1},
	})
	if err == nil {
		t.Fatal("want error propagation, got nil")
	}
}

// ── Arg shape / key forwarding ─────────────────────────────

func TestConfirmLogBatch_SendsBothArrays(t *testing.T) {
	// Capture the args via queryFn.
	var capturedArgs []any
	mc := newMockConn()
	mc.queryFn = func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
		capturedArgs = args
		return &emptyRows{}, nil
	}
	c := NewLogConfirmer(mc)
	candidates := []logstore.LogKey{
		{JobID: "j-1", Seq: 1},
		{JobID: "j-2", Seq: 42},
	}
	_, err := c.ConfirmLogBatch(context.Background(), candidates)
	if err != nil {
		t.Fatalf("ConfirmLogBatch: %v", err)
	}
	if len(capturedArgs) != 2 {
		t.Fatalf("want 2 args (job_ids + seqs), got %d", len(capturedArgs))
	}
	ids, ok := capturedArgs[0].([]string)
	if !ok {
		t.Fatalf("first arg: got %T, want []string", capturedArgs[0])
	}
	if len(ids) != 2 || ids[0] != "j-1" || ids[1] != "j-2" {
		t.Errorf("job_ids: got %v", ids)
	}
	seqs, ok := capturedArgs[1].([]int64)
	if !ok {
		t.Fatalf("second arg: got %T, want []int64", capturedArgs[1])
	}
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 42 {
		t.Errorf("seqs: got %v", seqs)
	}
}
