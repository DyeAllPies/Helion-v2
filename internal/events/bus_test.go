package events_test

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

func TestBus_PublishSubscribe(t *testing.T) {
	bus := events.NewBus(10, nil)
	sub := bus.Subscribe("job.*")
	defer sub.Cancel()

	bus.Publish(events.JobSubmitted("j1", "echo", 50))

	select {
	case e := <-sub.C:
		if e.Type != events.TopicJobSubmitted {
			t.Errorf("type = %q, want %q", e.Type, events.TopicJobSubmitted)
		}
		if e.Data["job_id"] != "j1" {
			t.Errorf("job_id = %v, want j1", e.Data["job_id"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestBus_TopicFiltering(t *testing.T) {
	bus := events.NewBus(10, nil)
	jobSub := bus.Subscribe("job.*")
	nodeSub := bus.Subscribe("node.*")
	defer jobSub.Cancel()
	defer nodeSub.Cancel()

	bus.Publish(events.JobCompleted("j1", "n1", 100))
	bus.Publish(events.NodeRegistered("n1", "10.0.0.1:8080"))

	// Job subscriber should get job event.
	select {
	case e := <-jobSub.C:
		if e.Type != events.TopicJobCompleted {
			t.Errorf("job sub got %q, want %q", e.Type, events.TopicJobCompleted)
		}
	case <-time.After(time.Second):
		t.Fatal("job sub timed out")
	}

	// Node subscriber should get node event.
	select {
	case e := <-nodeSub.C:
		if e.Type != events.TopicNodeRegistered {
			t.Errorf("node sub got %q, want %q", e.Type, events.TopicNodeRegistered)
		}
	case <-time.After(time.Second):
		t.Fatal("node sub timed out")
	}

	// Job subscriber should NOT get the node event.
	select {
	case e := <-jobSub.C:
		t.Errorf("job sub got unexpected event: %q", e.Type)
	case <-time.After(50 * time.Millisecond):
		// expected: no event
	}
}

func TestBus_WildcardAll(t *testing.T) {
	bus := events.NewBus(10, nil)
	sub := bus.Subscribe("*")
	defer sub.Cancel()

	bus.Publish(events.JobSubmitted("j1", "echo", 50))
	bus.Publish(events.NodeStale("n1"))

	// Should receive both events.
	for i := 0; i < 2; i++ {
		select {
		case <-sub.C:
		case <-time.After(time.Second):
			t.Fatalf("timed out on event %d", i+1)
		}
	}
}

func TestBus_ExactMatch(t *testing.T) {
	bus := events.NewBus(10, nil)
	sub := bus.Subscribe("job.completed")
	defer sub.Cancel()

	bus.Publish(events.JobFailed("j1", "err", 1, 1))
	bus.Publish(events.JobCompleted("j2", "n1", 100))

	// Should only get the completed event.
	select {
	case e := <-sub.C:
		if e.Type != events.TopicJobCompleted {
			t.Errorf("got %q, want job.completed", e.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestBus_Cancel_StopsReceiving(t *testing.T) {
	bus := events.NewBus(10, nil)
	sub := bus.Subscribe("job.*")
	sub.Cancel()

	bus.Publish(events.JobSubmitted("j1", "echo", 50))

	if bus.SubscriberCount() != 0 {
		t.Errorf("subscriber count = %d, want 0 after cancel", bus.SubscriberCount())
	}
}

func TestBus_SubscriberCount(t *testing.T) {
	bus := events.NewBus(10, nil)
	if bus.SubscriberCount() != 0 {
		t.Errorf("initial count = %d, want 0", bus.SubscriberCount())
	}

	s1 := bus.Subscribe("job.*")
	s2 := bus.Subscribe("node.*")
	if bus.SubscriberCount() != 2 {
		t.Errorf("count = %d, want 2", bus.SubscriberCount())
	}

	s1.Cancel()
	if bus.SubscriberCount() != 1 {
		t.Errorf("count after cancel = %d, want 1", bus.SubscriberCount())
	}

	s2.Cancel()
	if bus.SubscriberCount() != 0 {
		t.Errorf("count after both cancel = %d, want 0", bus.SubscriberCount())
	}
}

func TestBus_MultipleTopics(t *testing.T) {
	bus := events.NewBus(10, nil)
	sub := bus.Subscribe("job.completed", "job.failed")
	defer sub.Cancel()

	bus.Publish(events.JobSubmitted("j1", "echo", 50)) // not subscribed
	bus.Publish(events.JobCompleted("j2", "n1", 100))  // subscribed
	bus.Publish(events.JobFailed("j3", "err", 1, 1))   // subscribed

	// Should get 2 events.
	for i := 0; i < 2; i++ {
		select {
		case <-sub.C:
		case <-time.After(time.Second):
			t.Fatalf("timed out on event %d", i+1)
		}
	}

	// No more events.
	select {
	case e := <-sub.C:
		t.Errorf("unexpected event: %q", e.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNewEvent_HasIDAndTimestamp(t *testing.T) {
	e := events.NewEvent("test.topic", map[string]any{"key": "val"})
	if e.ID == "" {
		t.Error("expected non-empty ID")
	}
	if e.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if e.Type != "test.topic" {
		t.Errorf("type = %q, want test.topic", e.Type)
	}
}

func TestTopicConstructors_AllHaveCorrectType(t *testing.T) {
	cases := []struct {
		event    events.Event
		wantType string
	}{
		{events.JobSubmitted("j", "echo", 50), events.TopicJobSubmitted},
		{events.JobTransition("j", "pending", "running", "n"), events.TopicJobTransition},
		{events.JobCompleted("j", "n", 100), events.TopicJobCompleted},
		{events.JobFailed("j", "err", 1, 1), events.TopicJobFailed},
		{events.JobRetrying("j", 2, time.Now()), events.TopicJobRetrying},
		{events.NodeRegistered("n", "addr"), events.TopicNodeRegistered},
		{events.NodeStale("n"), events.TopicNodeStale},
		{events.NodeRevoked("n", "reason"), events.TopicNodeRevoked},
		{events.WorkflowCompleted("wf"), events.TopicWorkflowCompleted},
		{events.WorkflowFailed("wf", "step"), events.TopicWorkflowFailed},
	}
	for _, tc := range cases {
		if tc.event.Type != tc.wantType {
			t.Errorf("event type = %q, want %q", tc.event.Type, tc.wantType)
		}
		if tc.event.ID == "" {
			t.Errorf("event %q has empty ID", tc.wantType)
		}
	}
}
