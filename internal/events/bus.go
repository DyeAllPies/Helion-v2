// internal/events/bus.go
//
// In-memory pub/sub event bus for real-time event distribution.
//
// The bus decouples event producers (job transitions, node lifecycle) from
// consumers (WebSocket streams, future webhooks). Events are ephemeral —
// the audit log remains the durable record.
//
// Concurrency: Publish is non-blocking. If a subscriber's channel is full,
// the event is dropped and a warning is logged. Subscribe/Unsubscribe hold
// a write lock briefly.

package events

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Event is a single event published on the bus.
type Event struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// Subscription is returned by Subscribe. Call Cancel() to unsubscribe.
type Subscription struct {
	C      <-chan Event // read events from this channel
	cancel func()
}

// Cancel removes this subscription from the bus.
func (s *Subscription) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Bus is an in-memory pub/sub event bus.
type Bus struct {
	mu         sync.RWMutex
	subs       map[uint64]*subscriber
	nextID     uint64
	bufferSize int
	log        *slog.Logger
}

type subscriber struct {
	ch     chan Event
	topics []string // glob patterns (e.g. "job.*", "node.stale")
}

// NewBus creates a new event bus. bufferSize controls the channel buffer
// per subscriber (default 256 if 0).
func NewBus(bufferSize int, log *slog.Logger) *Bus {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	if log == nil {
		log = slog.Default()
	}
	return &Bus{
		subs:       make(map[uint64]*subscriber),
		bufferSize: bufferSize,
		log:        log,
	}
}

// Publish sends an event to all subscribers whose topic patterns match.
// Non-blocking: if a subscriber's channel is full, the event is dropped.
func (b *Bus) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subs {
		if matchesAny(event.Type, sub.topics) {
			select {
			case sub.ch <- event:
			default:
				b.log.Warn("event bus: subscriber channel full, dropping event",
					slog.String("event_type", event.Type))
			}
		}
	}
}

// Subscribe registers a subscriber for the given topic patterns.
// Patterns support trailing wildcard: "job.*" matches "job.completed",
// "job.failed", etc. "*" matches everything.
func (b *Bus) Subscribe(topics ...string) *Subscription {
	ch := make(chan Event, b.bufferSize)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = &subscriber{ch: ch, topics: topics}
	b.mu.Unlock()

	return &Subscription{
		C: ch,
		cancel: func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
		},
	}
}

// SubscriberCount returns the number of active subscribers.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// matchesAny returns true if eventType matches any of the patterns.
func matchesAny(eventType string, patterns []string) bool {
	for _, p := range patterns {
		if matchGlob(p, eventType) {
			return true
		}
	}
	return false
}

// matchGlob matches a simple glob pattern against a string.
// Only trailing "*" is supported: "job.*" matches "job.completed".
// "*" alone matches everything.
func matchGlob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return strings.HasPrefix(s, prefix+".")
	}
	return pattern == s
}
