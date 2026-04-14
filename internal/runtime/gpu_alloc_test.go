package runtime

import (
	"strings"
	"sync"
	"testing"
)

func TestGPUAllocator_ZeroRequest_NoOp(t *testing.T) {
	a := NewGPUAllocator(4)
	idx, err := a.Allocate("job-1", 0)
	if err != nil {
		t.Fatalf("Allocate(0): %v", err)
	}
	if len(idx) != 0 {
		t.Fatalf("Allocate(0) returned %d indices, want 0", len(idx))
	}
	if a.InUse() != 0 {
		t.Fatalf("InUse: %d", a.InUse())
	}
}

func TestGPUAllocator_SingleAllocation_LowestIndexFirst(t *testing.T) {
	a := NewGPUAllocator(4)
	idx, err := a.Allocate("job-1", 2)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if len(idx) != 2 {
		t.Fatalf("want 2 indices, got %+v", idx)
	}
	if idx[0] != 0 || idx[1] != 1 {
		t.Fatalf("expected lowest-index-first, got %+v", idx)
	}
}

func TestGPUAllocator_Release_FreesDevices(t *testing.T) {
	a := NewGPUAllocator(4)
	_, _ = a.Allocate("job-1", 2)
	if a.InUse() != 2 {
		t.Fatalf("InUse before release: %d", a.InUse())
	}
	a.Release("job-1")
	if a.InUse() != 0 {
		t.Fatalf("InUse after release: %d", a.InUse())
	}
}

func TestGPUAllocator_Insufficient_ReturnsError(t *testing.T) {
	a := NewGPUAllocator(2)
	_, _ = a.Allocate("greedy", 2)
	idx, err := a.Allocate("starving", 1)
	if err == nil {
		t.Fatalf("expected insufficient error, got indices %+v", idx)
	}
	if !strings.Contains(err.Error(), "insufficient") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Invariant: failed allocation must not have claimed anything.
	if a.InUse() != 2 {
		t.Fatalf("failed Allocate leaked devices: InUse=%d", a.InUse())
	}
}

func TestGPUAllocator_ReleaseUnknownJob_NoOp(t *testing.T) {
	a := NewGPUAllocator(2)
	_, _ = a.Allocate("real", 1)
	a.Release("never-existed")
	if a.InUse() != 1 {
		t.Fatalf("InUse after Release(unknown): %d", a.InUse())
	}
}

func TestGPUAllocator_ReuseAfterRelease(t *testing.T) {
	a := NewGPUAllocator(2)
	first, _ := a.Allocate("job-1", 2)
	a.Release("job-1")
	second, err := a.Allocate("job-2", 2)
	if err != nil {
		t.Fatalf("Allocate after Release: %v", err)
	}
	if len(second) != 2 || second[0] != first[0] || second[1] != first[1] {
		t.Fatalf("reuse mismatch: first=%+v second=%+v", first, second)
	}
}

func TestGPUAllocator_ZeroTotal_RequestFails(t *testing.T) {
	// CPU-only node: allocator is still installed but any positive
	// request must fail fast.
	a := NewGPUAllocator(0)
	_, err := a.Allocate("job", 1)
	if err == nil {
		t.Fatal("expected insufficient error on zero-total allocator")
	}
}

func TestGPUAllocator_Concurrent_AllocationsAreDistinct(t *testing.T) {
	// Invariant: no two concurrently-running jobs get the same
	// device index. Run 8 concurrent Allocates on a 16-GPU
	// allocator and verify every returned index is unique.
	a := NewGPUAllocator(16)
	var wg sync.WaitGroup
	results := make([][]int, 8)
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = a.Allocate(
				"concurrent-"+string(rune('a'+i)), 2)
		}(i)
	}
	wg.Wait()
	seen := make(map[int]string)
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("goroutine %d error: %v", i, errs[i])
		}
		for _, idx := range r {
			if owner, dup := seen[idx]; dup {
				t.Fatalf("index %d claimed twice: first by %q, now by goroutine %d", idx, owner, i)
			}
			seen[idx] = "goroutine-" + string(rune('a'+i))
		}
	}
	if len(seen) != 16 {
		t.Fatalf("expected all 16 devices claimed, got %d", len(seen))
	}
}

func TestVisibleDevicesEnv(t *testing.T) {
	cases := []struct {
		in   []int
		want string
	}{
		{nil, ""},
		{[]int{}, ""},
		{[]int{0}, "0"},
		{[]int{3, 7}, "3,7"},
		{[]int{1, 2, 3, 4}, "1,2,3,4"},
	}
	for _, c := range cases {
		if got := VisibleDevicesEnv(c.in); got != c.want {
			t.Errorf("VisibleDevicesEnv(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
