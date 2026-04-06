package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockChannel is a test Channel implementation.
type mockChannel struct {
	channelType string
	notifyFn    func(ctx context.Context, event Event) error
	validateFn  func(ctx context.Context) error
	mu          sync.Mutex
	events      []Event
}

func newMockChannel(t string) *mockChannel {
	return &mockChannel{channelType: t}
}

func (m *mockChannel) Type() string { return m.channelType }

func (m *mockChannel) Notify(ctx context.Context, event Event) error {
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
	if m.notifyFn != nil {
		return m.notifyFn(ctx, event)
	}
	return nil
}

func (m *mockChannel) ValidateConfig(ctx context.Context) error {
	if m.validateFn != nil {
		return m.validateFn(ctx)
	}
	return nil
}

func (m *mockChannel) receivedEvents() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// resetRegistry clears the global registry between tests.
func resetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	channels = make(map[string]Channel)
	channelMetas = make(map[string]ChannelMeta)
}

func TestRegister(t *testing.T) {
	resetRegistry()
	ch := newMockChannel("test-channel")
	meta := ChannelMeta{Name: "Test Channel", Description: "A test channel"}

	Register(ch, meta)

	metas := RegisteredChannels()
	if len(metas) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(metas))
	}
	if metas[0].Type != "test-channel" {
		t.Errorf("expected type 'test-channel', got %q", metas[0].Type)
	}
	if metas[0].Name != "Test Channel" {
		t.Errorf("expected name 'Test Channel', got %q", metas[0].Name)
	}
}

func TestRegisterNilPanics(t *testing.T) {
	resetRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil channel")
		}
	}()
	Register(nil, ChannelMeta{})
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetRegistry()
	ch := newMockChannel("dup")
	Register(ch, ChannelMeta{})

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate registration")
		}
	}()
	Register(newMockChannel("dup"), ChannelMeta{})
}

func TestGetChannel(t *testing.T) {
	resetRegistry()
	ch := newMockChannel("slack")
	Register(ch, ChannelMeta{Name: "Slack"})

	got := GetChannel("slack")
	if got == nil {
		t.Fatal("expected channel, got nil")
	}
	if got.Type() != "slack" {
		t.Errorf("expected type 'slack', got %q", got.Type())
	}

	if GetChannel("nonexistent") != nil {
		t.Error("expected nil for unregistered channel")
	}
}

func TestRegisteredChannelsEmpty(t *testing.T) {
	resetRegistry()
	metas := RegisteredChannels()
	if len(metas) != 0 {
		t.Errorf("expected 0 channels, got %d", len(metas))
	}
}

func TestNotifyAll(t *testing.T) {
	resetRegistry()
	ch1 := newMockChannel("ch1")
	ch2 := newMockChannel("ch2")
	Register(ch1, ChannelMeta{Name: "Channel 1"})
	Register(ch2, ChannelMeta{Name: "Channel 2"})

	event := Event{
		Type:          EventDiscoveryCompleted,
		ProjectID:     "proj_123",
		ProjectName:   "Test Project",
		RunID:         "run_456",
		InsightsTotal: 5,
		InsightsHigh:  2,
		Timestamp:     time.Now(),
	}

	NotifyAll(context.Background(), event)

	// Give goroutines time to complete
	time.Sleep(50 * time.Millisecond)

	for _, ch := range []*mockChannel{ch1, ch2} {
		events := ch.receivedEvents()
		if len(events) != 1 {
			t.Errorf("channel %s: expected 1 event, got %d", ch.Type(), len(events))
			continue
		}
		if events[0].ProjectID != "proj_123" {
			t.Errorf("channel %s: expected project_id 'proj_123', got %q", ch.Type(), events[0].ProjectID)
		}
	}
}

func TestNotifyAllNoChannels(t *testing.T) {
	resetRegistry()
	// Should not panic with no channels registered
	NotifyAll(context.Background(), Event{Type: EventDiscoveryCompleted})
}

func TestNotifyAllErrorDoesNotBlock(t *testing.T) {
	resetRegistry()
	ch := newMockChannel("failing")
	ch.notifyFn = func(_ context.Context, _ Event) error {
		return context.DeadlineExceeded
	}
	Register(ch, ChannelMeta{Name: "Failing"})

	// Should not block even if channel returns an error
	done := make(chan struct{})
	go func() {
		NotifyAll(context.Background(), Event{Type: EventDiscoveryFailed, Error: "test error"})
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("NotifyAll blocked despite channel error")
	}
}

func TestMultipleChannelTypes(t *testing.T) {
	resetRegistry()
	Register(newMockChannel("slack"), ChannelMeta{
		Name:        "Slack",
		Description: "Slack notifications",
		Fields:      []ConfigField{{Key: "bot_token", Scope: "global"}},
	})
	Register(newMockChannel("teams"), ChannelMeta{
		Name:        "Teams",
		Description: "Teams notifications",
		Fields:      []ConfigField{{Key: "webhook_url", Scope: "global"}},
	})

	metas := RegisteredChannels()
	if len(metas) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(metas))
	}

	types := map[string]bool{}
	for _, m := range metas {
		types[m.Type] = true
	}
	if !types["slack"] || !types["teams"] {
		t.Errorf("expected slack and teams, got %v", types)
	}
}
