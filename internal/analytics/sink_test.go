// internal/analytics/sink_test.go

package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// ── Helper extraction tests ──────────────────────────────────────────────

func TestExtractString(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		key  string
		want string
	}{
		{"present", map[string]any{"job_id": "j1"}, "job_id", "j1"},
		{"missing", map[string]any{"job_id": "j1"}, "node_id", ""},
		{"nil map", nil, "job_id", ""},
		{"wrong type", map[string]any{"job_id": 42}, "job_id", ""},
		{"empty string", map[string]any{"job_id": ""}, "job_id", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractString(tt.data, tt.key)
			if got != tt.want {
				t.Errorf("extractString(%v, %q) = %q, want %q", tt.data, tt.key, got, tt.want)
			}
		})
	}
}

func TestExtractInt(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		key  string
		want int
	}{
		{"int", map[string]any{"priority": 50}, "priority", 50},
		{"int32", map[string]any{"priority": int32(50)}, "priority", 50},
		{"int64", map[string]any{"priority": int64(50)}, "priority", 50},
		{"float64", map[string]any{"priority": float64(50)}, "priority", 50},
		{"uint32", map[string]any{"priority": uint32(50)}, "priority", 50},
		{"missing", map[string]any{}, "priority", 0},
		{"nil map", nil, "priority", 0},
		{"wrong type", map[string]any{"priority": "high"}, "priority", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInt(tt.data, tt.key)
			if got != tt.want {
				t.Errorf("extractInt(%v, %q) = %d, want %d", tt.data, tt.key, got, tt.want)
			}
		})
	}
}

func TestExtractInt64(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		key  string
		want int64
	}{
		{"int64", map[string]any{"duration_ms": int64(1500)}, "duration_ms", 1500},
		{"int", map[string]any{"duration_ms": 1500}, "duration_ms", 1500},
		{"float64", map[string]any{"duration_ms": float64(1500)}, "duration_ms", 1500},
		{"missing", map[string]any{}, "duration_ms", 0},
		{"nil map", nil, "duration_ms", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInt64(tt.data, tt.key)
			if got != tt.want {
				t.Errorf("extractInt64(%v, %q) = %d, want %d", tt.data, tt.key, got, tt.want)
			}
		})
	}
}

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("nilIfEmpty(\"\") should return nil")
	}
	if nilIfEmpty("hello") != "hello" {
		t.Error("nilIfEmpty(\"hello\") should return \"hello\"")
	}
}

// ── Config defaults ──────────────────────────────────────────────────────

func TestSinkConfig_Defaults(t *testing.T) {
	cfg := SinkConfig{}.withDefaults()
	if cfg.BatchSize != 100 {
		t.Errorf("default BatchSize = %d, want 100", cfg.BatchSize)
	}
	if cfg.FlushInterval != 500*time.Millisecond {
		t.Errorf("default FlushInterval = %v, want 500ms", cfg.FlushInterval)
	}
	if cfg.BufferLimit != 10_000 {
		t.Errorf("default BufferLimit = %d, want 10000", cfg.BufferLimit)
	}
}

func TestSinkConfig_Overrides(t *testing.T) {
	cfg := SinkConfig{
		BatchSize:     50,
		FlushInterval: time.Second,
		BufferLimit:   5000,
	}.withDefaults()
	if cfg.BatchSize != 50 {
		t.Errorf("BatchSize = %d, want 50", cfg.BatchSize)
	}
	if cfg.FlushInterval != time.Second {
		t.Errorf("FlushInterval = %v, want 1s", cfg.FlushInterval)
	}
	if cfg.BufferLimit != 5000 {
		t.Errorf("BufferLimit = %d, want 5000", cfg.BufferLimit)
	}
}

// ── Buffer logic tests ──────────────────────────────────────────────────

func TestSink_Append_BuffersEvents(t *testing.T) {
	s := &Sink{
		cfg: SinkConfig{BufferLimit: 100}.withDefaults(),
		buf: make([]events.Event, 0, 100),
		log: nil,
	}
	// Use slog default for the test.
	s.log = testLogger()

	evt := events.JobSubmitted("j1", "echo", 50)
	s.append(evt)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) != 1 {
		t.Fatalf("buf len = %d, want 1", len(s.buf))
	}
	if s.buf[0].ID != evt.ID {
		t.Errorf("buffered event ID = %q, want %q", s.buf[0].ID, evt.ID)
	}
}

func TestSink_Append_DropsOldestWhenFull(t *testing.T) {
	s := &Sink{
		cfg: SinkConfig{BufferLimit: 3}.withDefaults(),
		buf: make([]events.Event, 0, 3),
		log: testLogger(),
	}

	e1 := events.JobSubmitted("j1", "echo", 1)
	e2 := events.JobSubmitted("j2", "echo", 2)
	e3 := events.JobSubmitted("j3", "echo", 3)
	e4 := events.JobSubmitted("j4", "echo", 4)

	s.append(e1)
	s.append(e2)
	s.append(e3)
	s.append(e4) // should drop e1

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) != 3 {
		t.Fatalf("buf len = %d, want 3", len(s.buf))
	}
	// e2 should be first, e4 should be last.
	if s.buf[0].ID != e2.ID {
		t.Errorf("buf[0] = %q, want %q (e2)", s.buf[0].ID, e2.ID)
	}
	if s.buf[2].ID != e4.ID {
		t.Errorf("buf[2] = %q, want %q (e4)", s.buf[2].ID, e4.ID)
	}
}

func TestSink_Rebuffer_PrependsFailedBatch(t *testing.T) {
	s := &Sink{
		cfg: SinkConfig{BufferLimit: 10}.withDefaults(),
		buf: make([]events.Event, 0, 10),
		log: testLogger(),
	}

	// Current buffer has one event.
	existing := events.JobSubmitted("existing", "echo", 1)
	s.append(existing)

	// Rebuffer a failed batch of two events.
	failed := []events.Event{
		events.JobSubmitted("failed1", "echo", 1),
		events.JobSubmitted("failed2", "echo", 2),
	}
	s.rebuffer(failed)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) != 3 {
		t.Fatalf("buf len = %d, want 3", len(s.buf))
	}
	// Failed batch should be prepended.
	if s.buf[0].ID != failed[0].ID {
		t.Errorf("buf[0] = %q, want %q (failed1)", s.buf[0].ID, failed[0].ID)
	}
	if s.buf[1].ID != failed[1].ID {
		t.Errorf("buf[1] = %q, want %q (failed2)", s.buf[1].ID, failed[1].ID)
	}
	if s.buf[2].ID != existing.ID {
		t.Errorf("buf[2] = %q, want %q (existing)", s.buf[2].ID, existing.ID)
	}
}

func TestSink_Rebuffer_RespectsLimit(t *testing.T) {
	s := &Sink{
		cfg: SinkConfig{BufferLimit: 2}.withDefaults(),
		buf: make([]events.Event, 0, 2),
		log: testLogger(),
	}

	// Fill buffer to limit.
	s.append(events.JobSubmitted("a", "echo", 1))
	s.append(events.JobSubmitted("b", "echo", 2))

	// Rebuffer should not exceed limit.
	failed := []events.Event{events.JobSubmitted("c", "echo", 3)}
	s.rebuffer(failed)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) != 2 {
		t.Errorf("buf len = %d, want 2 (should not exceed limit)", len(s.buf))
	}
}

// ── Start/Stop lifecycle ─────────────────────────────────────────────────

func TestSink_StartStop_NoEvents(t *testing.T) {
	bus := events.NewBus(10, nil)
	// nil conn — we won't flush, so it's safe.
	s := NewSink(nil, bus, SinkConfig{
		BatchSize:     10,
		FlushInterval: 50 * time.Millisecond,
		BufferLimit:   100,
	}, nil)

	ctx := context.Background()
	s.Start(ctx)

	// Let the loop run briefly.
	time.Sleep(100 * time.Millisecond)

	// Stop should return without hanging.
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5s")
	}
}

func TestSink_BuffersEventsFromBus(t *testing.T) {
	bus := events.NewBus(10, nil)
	s := NewSink(nil, bus, SinkConfig{
		BatchSize:     1000, // large batch so no auto-flush
		FlushInterval: 10 * time.Second,
		BufferLimit:   100,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	// Publish some events.
	bus.Publish(events.JobSubmitted("j1", "echo", 50))
	bus.Publish(events.NodeRegistered("n1", "10.0.0.1:8080"))
	bus.Publish(events.JobCompleted("j1", "n1", 1500))

	// Give the loop time to consume.
	time.Sleep(200 * time.Millisecond)

	s.mu.Lock()
	count := len(s.buf)
	s.mu.Unlock()

	if count != 3 {
		t.Errorf("buffered %d events, want 3", count)
	}

	cancel()
	s.Stop()
}

// ── Concurrent safety ────────────────────────────────────────────────────

func TestSink_ConcurrentAppend(t *testing.T) {
	s := &Sink{
		cfg: SinkConfig{BufferLimit: 1000}.withDefaults(),
		buf: make([]events.Event, 0, 1000),
		log: testLogger(),
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			evt := events.JobSubmitted(
				fmt.Sprintf("j%d", n),
				"echo",
				uint32(n),
			)
			s.append(evt)
		}(i)
	}
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) != 100 {
		t.Errorf("buf len = %d, want 100 after concurrent appends", len(s.buf))
	}
}

func testLogger() *slog.Logger {
	return slog.Default()
}
