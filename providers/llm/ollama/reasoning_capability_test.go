package ollama

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
	ollamamodel "github.com/ollama/ollama/types/model"
)

// thinkingShowResponse is a /api/show reply advertising the "thinking"
// capability — what an uncatalogued reasoning model (e.g. a freshly pulled
// qwen3) returns.
func thinkingShowResponse() *ollamaapi.ShowResponse {
	return &ollamaapi.ShowResponse{
		Capabilities: []ollamamodel.Capability{
			ollamamodel.CapabilityCompletion,
			ollamamodel.CapabilityThinking,
		},
	}
}

// TestChat_ReasoningUncataloguedThinkingCapability is the core Item-4 gate: a
// model the catalog does NOT flag as reasoning, but whose /api/show reports the
// "thinking" capability, gets native Think enabled when the operator asks for
// reasoning — detected from the model itself, no catalog entry required.
func TestChat_ReasoningUncataloguedThinkingCapability(t *testing.T) {
	const uncataloguedModel = "qwen2.5:7b" // catalog: non-reasoning
	if gollm.IsReasoningModel("ollama", uncataloguedModel) {
		t.Fatalf("test premise broken: %s is catalog-flagged reasoning", uncataloguedModel)
	}
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
		showResp: thinkingShowResponse(),
	}
	p := newMockOllamaProvider(mock, uncataloguedModel)

	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		ReasoningEffort: gollm.ReasoningEffortOn,
		Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	think := mock.lastChatReq.Think
	if think == nil || think.Value != true {
		t.Errorf("Think = %+v, want {true} (capability-detected reasoning)", think)
	}
	if mock.showCalls != 1 {
		t.Errorf("showCalls = %d, want 1 (capability probed once)", mock.showCalls)
	}
}

// TestChat_ReasoningCapabilityCached asserts the /api/show probe is cached: two
// reasoning Chats issue exactly one Show.
func TestChat_ReasoningCapabilityCached(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
		showResp: thinkingShowResponse(),
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	for i := 0; i < 2; i++ {
		if _, err := p.Chat(context.Background(), gollm.ChatRequest{
			ReasoningEffort: gollm.ReasoningEffortOn,
			Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat %d: %v", i, err)
		}
	}
	if mock.showCalls != 1 {
		t.Errorf("showCalls = %d, want 1 (probe cached across calls)", mock.showCalls)
	}
}

// TestChat_DefaultEffortNoShowProbe proves the byte-identical no-op path never
// probes: the default effort (today) issues no /api/show and no Think.
func TestChat_DefaultEffortNoShowProbe(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
		showResp: thinkingShowResponse(),
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if mock.showCalls != 0 {
		t.Errorf("showCalls = %d, want 0 (default effort must not probe capabilities)", mock.showCalls)
	}
	if mock.lastChatReq.Think != nil {
		t.Errorf("Think = %+v, want nil for default effort", mock.lastChatReq.Think)
	}
}

// TestChat_ThinkingBackstopRetry exercises the retry-without-think backstop:
// the model reports thinking support, so Think=true is sent, but the server
// rejects it — the provider must retry once without Think and succeed.
func TestChat_ThinkingBackstopRetry(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp:         ollamaapi.ChatResponse{Done: true, DoneReason: "stop", Message: ollamaapi.Message{Content: "ok"}},
		showResp:         thinkingShowResponse(),
		failWhenThinking: true,
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		ReasoningEffort: gollm.ReasoningEffortOn,
		Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat should recover via backstop, got: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want ok", resp.Content)
	}
	if mock.chatCalls != 2 {
		t.Errorf("chatCalls = %d, want 2 (initial + retry-without-think)", mock.chatCalls)
	}
	if mock.lastChatReq.Think != nil {
		t.Errorf("retry Think = %+v, want nil", mock.lastChatReq.Think)
	}
}

// TestChat_ShowFailureFallsBackToNoThink asserts a transient /api/show failure
// degrades safely: no Think is sent (so no 400), and the result isn't cached
// (a later call retries the probe).
func TestChat_ShowFailureFallsBackToNoThink(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
		showErr:  context.DeadlineExceeded,
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")

	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		ReasoningEffort: gollm.ReasoningEffortOn,
		Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if mock.lastChatReq.Think != nil {
		t.Errorf("Think = %+v, want nil on Show failure", mock.lastChatReq.Think)
	}
	// Not cached: a second call probes again.
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		ReasoningEffort: gollm.ReasoningEffortOn,
		Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	if mock.showCalls != 2 {
		t.Errorf("showCalls = %d, want 2 (a failed probe must not be cached)", mock.showCalls)
	}
}
