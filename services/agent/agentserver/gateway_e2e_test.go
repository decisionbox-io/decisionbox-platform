//go:build e2e

// Package agentserver e2e tests exercise the real DecisionBox AI gateway
// (ai.decisionbox.io or a local instance) through the same OpenAI-compatible
// provider layer the agent and dashboard use. They are gated behind the `e2e`
// build tag and the DBX_GATEWAY_URL / DBX_GATEWAY_KEY env vars so they only run
// when an operator points them at a real, routed gateway:
//
//	DBX_GATEWAY_URL=https://ai.decisionbox.io/v1 \
//	DBX_GATEWAY_KEY=dbx-live-... \
//	go test -tags e2e -run TestE2E_Gateway ./services/agent/agentserver/ -v
//
// They verify issue #11's gateway-facing acceptance: the gateway lists its
// alias models for the dashboard pickers (part 2), and the chat aliases
// dispatch end-to-end through the OpenAI-compatible wire (part 4's routing).
package agentserver

import (
	"context"
	"os"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
)

func gatewayEnv(t *testing.T) (url, key string) {
	t.Helper()
	url = os.Getenv("DBX_GATEWAY_URL")
	key = os.Getenv("DBX_GATEWAY_KEY")
	if url == "" || key == "" {
		t.Skip("set DBX_GATEWAY_URL and DBX_GATEWAY_KEY to run the gateway e2e")
	}
	return url, key
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestE2E_Gateway_EmbeddingPickerListsAlias verifies the dashboard embedding
// picker surfaces the gateway's embed alias — the provider's ListModels must
// return decisionbox-embed-model even though it doesn't match text-embedding-*.
func TestE2E_Gateway_EmbeddingPickerListsAlias(t *testing.T) {
	url, key := gatewayEnv(t)

	prov, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"credentials_json": key,
		"base_url":         url,
		"model":            "decisionbox-embed-model",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	lister, ok := prov.(goembedding.ModelLister)
	if !ok {
		t.Fatal("openai embedding provider must implement ModelLister")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := lister.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if !contains(ids, "decisionbox-embed-model") {
		t.Fatalf("embedding picker did not list the gateway alias; got %v", ids)
	}
}

// TestE2E_Gateway_LLMPickerListsAliases verifies the gateway returns its chat
// aliases for the LLM picker.
func TestE2E_Gateway_LLMPickerListsAliases(t *testing.T) {
	url, key := gatewayEnv(t)

	prov, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": key,
		"base_url":         url,
		"model":            "decisionbox-analysis-model",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	lister, ok := prov.(gollm.ModelLister)
	if !ok {
		t.Fatal("openai llm provider must implement ModelLister")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := lister.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"decisionbox-analysis-model", "decisionbox-blurb-model"} {
		if !contains(ids, want) {
			t.Errorf("LLM picker did not list %q; got %v", want, ids)
		}
	}
}

// TestE2E_Gateway_ChatThroughAlias dispatches a real chat completion through
// the gateway's analysis alias over the OpenAI-compatible wire — the routing
// the agent uses when its LLM provider points at the gateway.
func TestE2E_Gateway_ChatThroughAlias(t *testing.T) {
	url, key := gatewayEnv(t)

	prov, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": key,
		"base_url":         url,
		"model":            "decisionbox-analysis-model",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := prov.Chat(ctx, gollm.ChatRequest{
		Model:     "decisionbox-analysis-model",
		Messages:  []gollm.Message{{Role: "user", Content: "Reply with the single word: ok"}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("Chat through gateway alias: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("empty response content from the gateway analysis alias")
	}
	t.Logf("gateway analysis alias replied: %q (model=%s)", resp.Content, resp.Model)
}
