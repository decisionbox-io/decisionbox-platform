package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// capturingChecker records the usage events the client emits.
type capturingChecker struct {
	policy.NoopChecker
	events []policy.LLMUsageEvent
}

func (c *capturingChecker) ObserveLLMTokens(_ context.Context, _ string, ev policy.LLMUsageEvent) {
	c.events = append(c.events, ev)
}

// TestCreateMessage_RefusalIsAnError pins that a safety refusal (Anthropic
// stop_reason "refusal", no content) surfaces as ErrModelRefused instead of
// an empty reply the caller would re-ask — re-sending the same
// conversation is refused again.
func TestCreateMessage_RefusalIsAnError(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{StopReason: gollm.StopReasonRefusal}
	c, _ := New(provider, "m")
	c.policyChecker = &capturingChecker{}

	_, err := c.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "hi"}}, "sys", 100)
	if !errors.Is(err, ErrModelRefused) {
		t.Fatalf("err = %v, want ErrModelRefused", err)
	}
}

// A provider reports a refusal as an error carrying the call's usage (a
// refused call still bills its input). The client must record that usage —
// not an all-zero event — and must not re-send the request.
func TestCreateMessage_ProviderRefusalKeepsUsageAndIsNotRetried(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.Error = fmt.Errorf("bedrock/anthropic: %w", gollm.RefusalError(gollm.StopReasonRefusal, "m", gollm.Usage{InputTokens: 120}))
	c, _ := New(provider, "m")
	chk := &capturingChecker{}
	c.policyChecker = chk

	_, err := c.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "hi"}}, "sys", 100)
	if !errors.Is(err, ErrModelRefused) {
		t.Fatalf("err = %v, want ErrModelRefused", err)
	}
	if len(provider.Calls) != 1 {
		t.Fatalf("refusal sent %d requests, want 1", len(provider.Calls))
	}
	if len(chk.events) != 1 || chk.events[0].InputTokens != 120 {
		t.Fatalf("usage events = %+v, want one with the refused call's 120 input tokens", chk.events)
	}
}

// TestUsageEvent_CarriesCacheReasoningAndPhase pins that the usage event
// carries the cache split, reasoning tokens and the phase the call ran in,
// so cost per phase can be computed.
func TestUsageEvent_CarriesCacheReasoningAndPhase(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    "ok",
		StopReason: "end_turn",
		Usage: gollm.Usage{InputTokens: 1000, OutputTokens: 40, CacheReadTokens: 900,
			CacheWriteTokens: 60, ReasoningTokens: 25},
	}
	c, _ := New(provider, "m")
	chk := &capturingChecker{}
	c.policyChecker = chk
	c.SetPhase("exploration")

	if _, err := c.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "hi"}}, "sys", 100); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(chk.events) != 1 {
		t.Fatalf("events = %d, want 1", len(chk.events))
	}
	ev := chk.events[0]
	if ev.InputTokens != 1000 || ev.OutputTokens != 40 || ev.CacheReadTokens != 900 ||
		ev.CacheWriteTokens != 60 || ev.ReasoningTokens != 25 || ev.Phase != "exploration" {
		t.Errorf("event = %+v", ev)
	}
}
