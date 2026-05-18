package bedrock

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestBedrockProvider_Dispatch_CatalogAnthropic(t *testing.T) {
	// A catalogued Claude model should route to the Anthropic wire.
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse("ok", "anthropic.claude-sonnet-4-20250514-v1:0", "end_turn", 1, 1),
	}
	p := &BedrockProvider{
		client:     mock,
		model:      "anthropic.claude-sonnet-4-20250514-v1:0",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

// TestBedrockProvider_Dispatch_CatalogAnthropicViaCrossRegionAlias is
// the regression test for the Opus 4.7 pack-gen incident: the user
// pastes a cross-region inference profile ID (us./eu./apac./global.),
// which is *not* the canonical catalog ID, but the resolver must
// still find the entry via the Aliases list and dispatch on the
// Anthropic wire.
func TestBedrockProvider_Dispatch_CatalogAnthropicViaCrossRegionAlias(t *testing.T) {
	for _, model := range []string{
		"us.anthropic.claude-opus-4-7-v1:0",
		"eu.anthropic.claude-opus-4-7-v1:0",
		"apac.anthropic.claude-opus-4-7-v1:0",
		"jp.anthropic.claude-opus-4-7-v1:0",
		"au.anthropic.claude-opus-4-7-v1:0",
		"global.anthropic.claude-opus-4-7-v1:0",
		// Short forms users may paste from console UIs / docs.
		"us.anthropic.claude-opus-4-7",
		"global.anthropic.claude-opus-4-7-v1",
		"global.anthropic.claude-opus-4-7",
		// Family-only forms.
		"claude-opus-4-7",
		"opus-4-7",
	} {
		t.Run(model, func(t *testing.T) {
			mock := &mockBedrockClient{
				responseBody: buildAnthropicResponse("ok", model, "end_turn", 1, 1),
			}
			p := &BedrockProvider{
				client:     mock,
				model:      model,
				httpClient: &http.Client{},
			}
			resp, err := p.Chat(context.Background(), gollm.ChatRequest{
				Messages: []gollm.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("unexpected error for alias %q: %v", model, err)
			}
			if resp.Content != "ok" {
				t.Errorf("content = %q", resp.Content)
			}
		})
	}
}

func TestBedrockProvider_Dispatch_CatalogOpenAICompat(t *testing.T) {
	// A catalogued Qwen model should route to the OpenAICompat wire.
	openaiBody := []byte(`{"id":"x","model":"qwen.qwen3-next-80b-a3b",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi from qwen"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	mock := &mockBedrockClient{responseBody: openaiBody}
	p := &BedrockProvider{
		client:     mock,
		model:      "qwen.qwen3-next-80b-a3b",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hi from qwen" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Usage.InputTokens != 4 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestBedrockProvider_Dispatch_CatalogOpenAICompatViaCrossRegionAlias(t *testing.T) {
	openaiBody := []byte(`{"id":"x","model":"deepseek.r1-v1:0",
		"choices":[{"index":0,"message":{"role":"assistant","content":"r1 ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	for _, model := range []string{
		"us.deepseek.r1-v1:0",
		"global.deepseek.r1-v1:0",
		"deepseek.r1",
	} {
		t.Run(model, func(t *testing.T) {
			p := &BedrockProvider{
				client:     &mockBedrockClient{responseBody: openaiBody},
				model:      model,
				httpClient: &http.Client{},
			}
			resp, err := p.Chat(context.Background(), gollm.ChatRequest{
				Messages: []gollm.Message{{Role: "user", Content: "ping"}},
			})
			if err != nil {
				t.Fatalf("unexpected error for alias %q: %v", model, err)
			}
			if resp.Content != "r1 ok" {
				t.Errorf("content = %q", resp.Content)
			}
		})
	}
}

func TestBedrockProvider_Dispatch_UncataloguedActionableError(t *testing.T) {
	// An uncatalogued model without a wire_override and without a
	// recognisable family-prefix must return an error that names the
	// provider, the model, and the wire_override hint.
	p := &BedrockProvider{
		model:      "vendor.future-model-2099",
		httpClient: &http.Client{},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "vendor.future-model-2099",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for uncatalogued model")
	}
	msg := err.Error()
	for _, want := range []string{"bedrock", "vendor.future-model-2099", "wire_override"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestBedrockProvider_Dispatch_WireOverrideWhenUncatalogued(t *testing.T) {
	// An uncatalogued model with a wire_override should route per the override.
	openaiBody := []byte(`{"model":"vendor.future-2099",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	mock := &mockBedrockClient{responseBody: openaiBody}
	p := &BedrockProvider{
		client:       mock,
		model:        "vendor.future-2099",
		wireOverride: gollm.WireOpenAICompat,
		httpClient:   &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestBedrockProvider_Factory_RejectsGoogleNativeWireOverride(t *testing.T) {
	// google-native is a valid Wire value but no implementation exists
	// on Bedrock. The factory should reject it at save time rather than
	// letting the user hit a confusing dispatch-time error.
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model":         "vendor.gemini-on-bedrock",
		"wire_override": "google-native",
	})
	if err == nil {
		t.Fatal("expected factory to reject google-native wire_override on Bedrock")
	}
	if !strings.Contains(err.Error(), "invalid wire_override") {
		t.Errorf("error = %q, should mention invalid wire_override", err.Error())
	}
}

func TestBedrockProvider_Factory_MissingModel(t *testing.T) {
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestBedrockProvider_Factory_InvalidWireOverride(t *testing.T) {
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model":         "anthropic.claude-sonnet-4-20250514-v1:0",
		"wire_override": "bogus-wire",
	})
	if err == nil {
		t.Fatal("expected error for invalid wire_override")
	}
	if !strings.Contains(err.Error(), "invalid wire_override") {
		t.Errorf("error = %q", err.Error())
	}
	for _, want := range []string{"anthropic", "openai-compat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should list wire %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "google-native") {
		t.Errorf("error should not list google-native (not implemented on Bedrock): %q", err.Error())
	}
}

func TestBedrockProvider_Factory_AcceptsValidWireOverride(t *testing.T) {
	for _, wo := range []string{"anthropic", "openai-compat"} {
		prov, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
			"model":         "vendor.custom",
			"wire_override": wo,
		})
		if err != nil {
			t.Fatalf("wire_override=%q: unexpected error %v", wo, err)
		}
		if prov == nil {
			t.Fatalf("wire_override=%q: nil provider", wo)
		}
	}
}

func TestBedrockProvider_Factory_EmptyWireOverrideAllowed(t *testing.T) {
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model": "anthropic.claude-sonnet-4-20250514-v1:0",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBedrockProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatal("bedrock not registered")
	}
	if meta.Description == "" {
		t.Error("missing provider description")
	}
	if len(meta.Models) == 0 {
		t.Fatal("catalog empty")
	}
	if meta.DefaultMaxOutputTokens != 16384 {
		t.Errorf("DefaultMaxOutputTokens = %d, want 16384", meta.DefaultMaxOutputTokens)
	}
	// Spot-check the regression: every cross-region alias of Opus
	// 4.7 should resolve to the 128k cap, and the legacy provider
	// _default of 16384 should never apply to a catalogued Claude.
	for _, model := range []string{
		"anthropic.claude-opus-4-7-v1:0",
		"us.anthropic.claude-opus-4-7-v1:0",
		"us.anthropic.claude-opus-4-7",
		"global.anthropic.claude-opus-4-7",
		"opus-4-7",
		"claude-opus-4-7",
	} {
		if got := gollm.GetMaxOutputTokens(providerName, model); got != opus47Max {
			t.Errorf("GetMaxOutputTokens(%q) = %d, want %d", model, got, opus47Max)
		}
	}
	// Unknown model falls back to the provider default.
	if got := gollm.GetMaxOutputTokens(providerName, "vendor.unknown-2099"); got != 16384 {
		t.Errorf("GetMaxOutputTokens default = %d, want 16384", got)
	}
}

func TestBedrockProvider_ConfigFields(t *testing.T) {
	meta, _ := gollm.GetProviderMeta(providerName)

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	for _, want := range []string{"region", "model", "wire_override"} {
		if !keys[want] {
			t.Errorf("missing %s config field", want)
		}
	}
}

// TestBedrockProvider_Catalog_PricingMatches confirms each shipped
// Anthropic Claude model carries its current Anthropic-published list
// price. Catches a class of regressions where a copy-paste during a
// catalog update grafts the wrong pricing onto a new entry.
func TestBedrockProvider_Catalog_PricingMatches(t *testing.T) {
	meta, _ := gollm.GetProviderMeta(providerName)
	cases := []struct {
		model string
		in    float64
		out   float64
	}{
		{"anthropic.claude-opus-4-7-v1:0", opus47In, opus47Out},
		{"anthropic.claude-opus-4-6-v1:0", opus46In, opus46Out},
		{"anthropic.claude-opus-4-5-20251101-v1:0", opus45In, opus45Out},
		{"anthropic.claude-opus-4-1-20250805-v1:0", opus41In, opus41Out},
		{"anthropic.claude-opus-4-20250514-v1:0", opus4In, opus4Out},
		{"anthropic.claude-sonnet-4-6-v1:0", sonnetIn, sonnetOut},
		{"anthropic.claude-sonnet-4-5-20250929-v1:0", sonnetIn, sonnetOut},
		{"anthropic.claude-haiku-4-5-20251001-v1:0", haikuIn, haikuOut},
	}
	for _, tc := range cases {
		p, ok := meta.PricingFor(tc.model)
		if !ok {
			t.Errorf("%s: no pricing in catalog", tc.model)
			continue
		}
		if p.InputPerMillion != tc.in || p.OutputPerMillion != tc.out {
			t.Errorf("%s pricing = $%v/$%v, want $%v/$%v",
				tc.model, p.InputPerMillion, p.OutputPerMillion, tc.in, tc.out)
		}
	}
}

func TestBedrockProvider_Validate_UncataloguedUninferableModel(t *testing.T) {
	// amazon.nova-* has no wire implementation on Bedrock and the
	// FamilyInferrer does not recognise it. Validate must hit the
	// "not in catalog" error before any AWS call.
	p := &BedrockProvider{
		model:      "amazon.nova-2-lite-v1:0",
		httpClient: &http.Client{},
	}
	if err := p.Validate(context.Background()); err == nil {
		t.Error("Validate should fail for uncatalogued, uninferable model with no wire_override")
	}
}

func TestBedrockProvider_Dispatch_InferredWireForUncataloguedClaude(t *testing.T) {
	// A never-seen Claude variant with the canonical "anthropic." prefix
	// should be inferred as Anthropic wire and dispatch successfully,
	// even without a catalog entry or wire_override.
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse("ok", "anthropic.claude-99-new-v1:0", "end_turn", 1, 1),
	}
	p := &BedrockProvider{
		client:     mock,
		model:      "anthropic.claude-99-new-v1:0",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("inferred wire should allow dispatch, got %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

// legacy helper kept from original file for the mock tests.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestFactory_StashesAwsCfg_AccessKeys is the regression for the gap
// in PR #222: factory built an awsCfg from the access_keys auth method
// but didn't store it on BedrockProvider, so ListModels later called
// awsconfig.LoadDefaultConfig and threw the dashboard-supplied keys
// away. The fix stashes the factory's awsCfg on the provider so
// ListModels can reuse it.
//
// We assert by retrieving credentials directly from the stored config
// — if the factory stops persisting awsCfg, the static credentials are
// lost and Retrieve returns a different access key (or hits the SDK
// ambient chain, which produces an IMDS error on a non-EC2 host).
func TestFactory_StashesAwsCfg_AccessKeys(t *testing.T) {
	cfg := gollm.ProviderConfig{
		"region":           "us-east-1",
		"model":            "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"auth_method":      "access_keys",
		"credentials_json": "AKIA-fixture:secret-fixture",
	}
	prov, err := factory(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	bp, ok := prov.(*BedrockProvider)
	if !ok {
		t.Fatalf("provider type = %T, want *BedrockProvider", prov)
	}
	if bp.awsCfg.Credentials == nil {
		t.Fatal("awsCfg.Credentials is nil — factory dropped the credential provider")
	}
	creds, err := bp.awsCfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve from awsCfg: %v", err)
	}
	if creds.AccessKeyID != "AKIA-fixture" {
		t.Errorf("AccessKeyID = %q, want AKIA-fixture (factory did not stash the static credentials provider)", creds.AccessKeyID)
	}
	if creds.SecretAccessKey != "secret-fixture" {
		t.Errorf("SecretAccessKey = %q, want secret-fixture", creds.SecretAccessKey)
	}
	if bp.awsCfg.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", bp.awsCfg.Region)
	}
}
