// Package litellm provides an llm.Provider backed by a LiteLLM proxy.
//
// LiteLLM (https://github.com/BerriAI/litellm) is an OpenAI-compatible
// proxy that fronts many upstream models behind one endpoint and one
// key. It speaks the OpenAI /chat/completions and /models wire, so this
// provider reuses the shared openaicompat helpers — it is a first-class
// sibling of the bare `openai` provider with a dedicated config form,
// live model listing, dispatch-any-model handling, and per-project
// custom TLS (CA upload / skip-verify) for private-CA HTTPS proxies.
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/llm/litellm"
//
// Configuration:
//
//	base_url=https://litellm.internal:4000   (LiteLLM proxy root)
//	credentials_json=<master or virtual key>  (optional; sent as Bearer)
//	model=<any model the proxy is configured for>
//	tls_ca_cert / tls_skip_verify             (optional custom TLS)
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

const providerName = "litellm"

// litellmDefaultTimeout mirrors the OpenAI provider's default — LiteLLM
// forwards to upstreams that can take minutes on long-form generations.
// Operators override via LLM_TIMEOUT or per-project timeout_seconds.
const litellmDefaultTimeout = 5 * time.Minute

func init() {
	gollm.RegisterWithMeta(providerName, factory, gollm.ProviderMeta{
		Name:        "LiteLLM",
		Description: "LiteLLM proxy — an OpenAI-compatible gateway that routes to any configured upstream model",
		ConfigFields: append(append([]gollm.ConfigField{
			{
				Key:         "base_url",
				Label:       "LiteLLM proxy URL",
				Required:    true,
				Type:        "string",
				Placeholder: "https://litellm.internal:4000",
				Description: "The LiteLLM proxy root URL. The OpenAI-compatible /v1 routes are derived from it.",
			},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Placeholder: "gpt-4o",
				Description: "Any model name the LiteLLM proxy is configured to route. Load models to pick from the proxy's live list.",
			},
		}, gollm.ContextWindowConfigFields()...), gollm.TLSConfigFields()...),
		AuthMethods: []gollm.AuthMethod{
			{
				ID: "api_key", Name: "API Key",
				Description: "LiteLLM master or virtual key, sent as a Bearer token.",
				Fields: []gollm.ConfigField{
					{Key: "credentials_json", Label: "LiteLLM key", Required: true, Type: "credential", Placeholder: "sk-..."},
				},
			},
			{
				// Open proxies need no key. A credential-less auth method
				// lets the dashboard skip the key field entirely (the
				// credentials phase gates on a credential field existing),
				// so "Load models" works and no stale key is ever sent.
				ID: "none", Name: "No authentication",
				Description: "For a LiteLLM proxy that requires no key.",
			},
		},
		// LiteLLM routes any configured model name through one OpenAI-
		// compatible path, so every model it exposes is dispatchable
		// regardless of whether it is in a catalog — mirror Ollama.
		DispatchAnyModelID: true,
		// The proxy expects the exact model name it exposes; the picker
		// must save the live ID, not a catalog canonical.
		PreferLiveModelID: true,
		// No fixed catalog — rely on live listing + the unknown-model
		// defaults. Unknown models get 128K input / 64K output (#338).
		DefaultMaxInputTokens:  131072,
		DefaultMaxOutputTokens: 64000,
		// LiteLLM speaks the OpenAI-compatible wire, which supports
		// function calling; whether a given proxied model implements it
		// reliably varies, as with the bare openai provider.
		SupportsTools: true,
	})
}

func factory(cfg gollm.ProviderConfig) (gollm.Provider, error) {
	baseURL := strings.TrimSpace(cfg["base_url"])
	if baseURL == "" {
		return nil, fmt.Errorf("litellm: base_url is required (the LiteLLM proxy URL)")
	}

	// model is optional at construction time: the dashboard's "Load
	// models" flow constructs the provider without a model picked so it
	// can call ListModels(). Chat() checks for an empty model at call
	// time. Matches the Ollama provider's list-only-construction pattern.
	model := cfg["model"]

	// The LiteLLM key is optional — some proxies run open. Sent as a
	// Bearer header only when present. When the project explicitly chose
	// the "none" auth method, drop any credential the caller still merged
	// in (a saved llm-credentials secret or the LLM_API_KEY env fallback):
	// an open proxy must not receive a stale/global key, which could leak
	// it or be rejected.
	apiKey := cfg["credentials_json"]
	if cfg["auth_method"] == "none" {
		apiKey = ""
	}

	timeout := gollm.ResolveHTTPTimeout(cfg, litellmDefaultTimeout)
	client, err := gollm.HTTPClientFor(cfg, timeout)
	if err != nil {
		return nil, fmt.Errorf("litellm: %w", err)
	}

	p := NewLiteLLMProvider(apiKey, model, baseURL, client)
	// Operator-set output cap: proxied models (e.g. qwen3-32b) may cap
	// generation below the 64K default, so clamp every request to it.
	p.maxOutputTokens = gollm.MaxOutputOverride(cfg)
	return p, nil
}

// LiteLLMProvider implements llm.Provider against a LiteLLM proxy using
// the OpenAI-compatible wire.
type LiteLLMProvider struct {
	apiKey  string
	model   string
	baseURL string // normalised, ends with /v1 (no trailing slash)
	client  *http.Client
	// maxOutputTokens, when > 0, caps every request's max_tokens (the
	// operator's max_output_tokens override). 0 means no cap.
	maxOutputTokens int
}

// NewLiteLLMProvider creates a provider. baseURL is the LiteLLM proxy
// root; the OpenAI-compatible /v1 routes are derived from it. A nil
// client falls back to a default client with litellmDefaultTimeout.
func NewLiteLLMProvider(apiKey, model, baseURL string, client *http.Client) *LiteLLMProvider {
	if client == nil {
		client = &http.Client{Timeout: litellmDefaultTimeout}
	}
	return &LiteLLMProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: normalizeBaseURL(baseURL),
		client:  client,
	}
}

// normalizeBaseURL turns a LiteLLM proxy root into the OpenAI-compatible
// base: it strips a trailing slash and appends /v1 unless the URL
// already ends in /v1. LiteLLM serves both the /v1-prefixed and bare
// routes; pinning /v1 matches the OpenAI surface the wire expects.
func normalizeBaseURL(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if b == "" {
		return b
	}
	if strings.HasSuffix(b, "/v1") {
		return b
	}
	return b + "/v1"
}

// setAuth attaches the Bearer key when one is configured.
func (p *LiteLLMProvider) setAuth(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

// Validate checks connectivity + credentials by listing models.
// GET /v1/models — no token cost. Exercises the custom-TLS transport,
// so a private-CA misconfiguration surfaces here.
func (p *LiteLLMProvider) Validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("litellm: failed to create request: %w", err)
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("litellm: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("litellm: validation failed (status %d): %s", resp.StatusCode, gollm.SanitizeErrorBody(body, 500))
	}
	return nil
}

// Chat sends a conversation to the LiteLLM proxy and returns the response.
func (p *LiteLLMProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	if model == "" {
		return nil, fmt.Errorf("litellm: chat requires a model — neither ChatRequest.Model nor provider model is set (list-only construction)")
	}

	// Apply the operator's output-token cap (if any) before building the
	// request, so a proxied model whose real limit is below the default
	// isn't sent an oversized max_tokens (rejected with a 4xx upstream).
	req.MaxTokens = gollm.ClampMaxTokens(req.MaxTokens, p.maxOutputTokens)

	body := openaicompat.BuildRequestBody(model, req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("litellm: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("litellm: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setAuth(httpReq)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("litellm: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("litellm: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		if apiErr := openaicompat.ExtractAPIError(respBody); apiErr != nil {
			// Sanitize the parsed message too — a proxy that echoes the
			// Authorization header or key in its error body must not leak
			// it into logs / the dashboard.
			return nil, fmt.Errorf("litellm: API error (%d): %s - %s",
				httpResp.StatusCode, apiErr.Type, gollm.SanitizeErrorBody([]byte(apiErr.Message), 500))
		}
		return nil, fmt.Errorf("litellm: API error (%d): %s", httpResp.StatusCode, gollm.SanitizeErrorBody(respBody, 500))
	}

	resp, err := openaicompat.ParseResponseBody(respBody)
	if err != nil {
		return nil, fmt.Errorf("litellm: %w", err)
	}
	return resp, nil
}
