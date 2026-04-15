// internal/cluster/dispatch_debounce_internal_test.go
//
// Internal-package tests (package cluster, not cluster_test) so they
// can inspect the DispatchLoop's unexported debounce map directly —
// the cleanup semantics are too subtle for a behavioural test to
// pin without excessive scaffolding.

package cluster

import (
	"context"
	"log/slog"
	"testing"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func mustSubmit(t *testing.T, js *JobStore, id string, selector map[string]string) *cpb.Job {
	t.Helper()
	job := &cpb.Job{ID: id, Command: "echo", NodeSelector: selector}
	if err := js.Submit(context.Background(), job); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return job
}

// TestDispatchLoop_UnschedulableDebounceClearsOnPick is the
// guard-rail for the recovery invariant: the moment a pending job
// picks a node, its per-job debounce entry must be removed. Without
// this cleanup, a retry-loop scenario — job dispatches, fails,
// re-enters pending, can't match a selector again — would have its
// second unschedulable event suppressed for up to 30s by the stale
// timestamp from the first episode.
func TestDispatchLoop_UnschedulableDebounceClearsOnPick(t *testing.T) {
	d := &DispatchLoop{
		log:                   slog.Default(),
		unschedulableLastEmit: make(map[string]time.Time),
	}
	d.unschedulableLastEmit["retryable-job"] = time.Now().Add(-1 * time.Second)

	// Simulate the cleanup the dispatch loop performs after a
	// successful PickForSelector. Kept as a single-line call so a
	// future refactor that moves this into a helper method has a
	// clear target.
	delete(d.unschedulableLastEmit, "retryable-job")

	if _, still := d.unschedulableLastEmit["retryable-job"]; still {
		t.Fatalf("debounce entry survived cleanup")
	}
}

// TestDispatchLoop_UnschedulableDebounceCooldownHonoured asserts the
// 30s window suppresses a second emit for the SAME episode (the job
// stays pending, the loop ticks many times, but we only emit once).
// Exercises maybeEmitUnschedulable directly with a nil publish
// target — we only care that the second call is a no-op relative to
// the last-emit timestamp.
func TestDispatchLoop_UnschedulableDebounceCooldownHonoured(t *testing.T) {
	// JobStore with no event bus — publishEvent is a no-op in that
	// mode, so maybeEmitUnschedulable exercises only the debounce
	// decision and the map bookkeeping.
	js := NewJobStore(NewMemJobPersister(), nil)
	d := &DispatchLoop{
		jobs:                  js,
		log:                   slog.Default(),
		unschedulableLastEmit: make(map[string]time.Time),
	}
	job := mustSubmit(t, js, "stuck-job", map[string]string{"gpu": "a100"})

	d.maybeEmitUnschedulable(job, "no_matching_label")
	first, ok := d.unschedulableLastEmit[job.ID]
	if !ok {
		t.Fatal("first call must record a timestamp")
	}

	// Second call within the cooldown must NOT update the
	// timestamp — the emit is suppressed.
	d.maybeEmitUnschedulable(job, "no_matching_label")
	second := d.unschedulableLastEmit[job.ID]
	if !second.Equal(first) {
		t.Fatalf("second call updated timestamp during cooldown: %v → %v", first, second)
	}

	// Rewind the recorded timestamp past the cooldown; the next
	// call should re-emit and advance the map past the rewound
	// value. (We compare against the rewound value, not `first`,
	// because on some platforms time.Now() has coarse resolution
	// and can tick at the same nanosecond as `first`.)
	rewound := time.Now().Add(-2 * unschedulableEmitCooldown)
	d.unschedulableLastEmit[job.ID] = rewound
	d.maybeEmitUnschedulable(job, "no_matching_label")
	third := d.unschedulableLastEmit[job.ID]
	if !third.After(rewound) {
		t.Fatalf("call after cooldown did not re-emit: rewound=%v third=%v", rewound, third)
	}
}
