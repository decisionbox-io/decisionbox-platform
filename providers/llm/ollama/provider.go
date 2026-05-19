// Package ollama provides an llm.Provider backed by a local Ollama instance.
// Ollama runs open-source LLMs locally (Llama, Qwen, Mistral, etc.).
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
//
// Configuration:
//
//	LLM_PROVIDER=ollama
//	LLM_MODEL=qwen2.5:7b          (any Ollama model)
//	OLLAMA_HOST=http://localhost:11434  (optional, default localhost)
package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
)

// ollamaDefaultTimeout is the historical default HTTP timeout for
// Ollama calls. Local inference on consumer hardware regularly exceeds
// 60 s, so 5 minutes is the floor; operators raise it via
// LLM_TIMEOUT or per-project timeout_seconds.
const ollamaDefaultTimeout = 5 * time.Minute

func init() {
	gollm.RegisterWithMeta("ollama", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		host := cfg["host"]
		if host == "" {
			host = "http://localhost:11434"
		}

		// model is optional at construction time: the dashboard's "Load
		// models" flow constructs the provider without a model picked so
		// it can call ListModels(). Chat() / Validate() check for an
		// empty model at call time and return a clear error there.
		model := cfg["model"]

		return NewOllamaProvider(host, model, gollm.ResolveHTTPTimeout(cfg, ollamaDefaultTimeout))
	}, gollm.ProviderMeta{
		Name:        "Ollama (Local)",
		Description: "Run open-source models locally via Ollama",
		ConfigFields: []gollm.ConfigField{
			{Key: "host", Label: "Ollama Host", Type: "string", Default: "http://localhost:11434", Placeholder: "http://localhost:11434"},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "qwen2.5:7b",
				Placeholder: "qwen2.5:7b",
				Description: "Any Ollama model you have pulled (run 'ollama list' to see local models).",
			},
		},
		Models:                 buildOllamaCatalog(),
		DefaultMaxOutputTokens: 16384,
		// Ollama dispatches every model through one SDK path with no
		// wire switch, so any model the server has pulled (returned by
		// /api/tags) is dispatchable. Without this flag, live-only rows
		// like "gemma4:31b" come back with Wire="" + Dispatchable=false
		// and the dashboard hides them under the "unsupported wire"
		// filter.
		DispatchAnyModelID: true,
		// Ollama strictly requires the EXACT model:tag the user pulled.
		// `ollama run qwen3` when only `qwen3:32b` is local returns 404.
		// So the picker must save the live ID (qwen3:32b), not the
		// catalog canonical (qwen3). FindModel keeps working at runtime
		// because the catalog row's aliases include the tagged forms,
		// so max-tokens enrichment still resolves.
		PreferLiveModelID: true,
	})
}

// OllamaProvider implements llm.Provider using a local Ollama instance.
// httpTimeout is retained on the provider so callers and tests can
// inspect the effective deadline — the ollama SDK's *api.Client wraps
// the underlying *http.Client behind unexported fields.
type OllamaProvider struct {
	client      ollamaClient
	model       string
	httpTimeout time.Duration
}

// NewOllamaProvider creates a new Ollama LLM provider. A zero or
// negative timeout falls back to ollamaDefaultTimeout so callers that
// don't care (mainly tests) don't have to think about it.
func NewOllamaProvider(host, model string, timeout time.Duration) (*OllamaProvider, error) {
	parsedURL, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("ollama: invalid host URL: %w", err)
	}

	if timeout <= 0 {
		timeout = ollamaDefaultTimeout
	}
	client := ollamaapi.NewClient(parsedURL, &http.Client{Timeout: timeout})

	return &OllamaProvider{
		client:      client,
		model:       model,
		httpTimeout: timeout,
	}, nil
}

// Validate checks that Ollama is reachable and the model is available.
// Uses the List API — no inference cost.
func (p *OllamaProvider) Validate(ctx context.Context) error {
	if p.model == "" {
		return fmt.Errorf("ollama: provider was constructed without a model (list-only); call NewProvider again with cfg[\"model\"] set before validating")
	}
	list, err := p.client.List(ctx)
	if err != nil {
		return fmt.Errorf("ollama: cannot reach server: %w", err)
	}

	for _, m := range list.Models {
		// Ollama model names may include tag (e.g., "qwen2.5:7b")
		if m.Name == p.model || strings.HasPrefix(m.Name, p.model+":") {
			return nil
		}
	}

	available := make([]string, len(list.Models))
	for i, m := range list.Models {
		available[i] = m.Name
	}
	return fmt.Errorf("ollama: model %q not found (available: %v)", p.model, available)
}

// Chat sends a conversation to Ollama and returns the response.
func (p *OllamaProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if len(req.Tools) > 0 {
		// Ollama's tool-calling support is model-dependent and we don't
		// surface a catalog for it; reject explicitly so callers can
		// route the request to a tool-capable provider instead of
		// silently stripping tool defs.
		return nil, fmt.Errorf("ollama: %w", gollm.ErrToolsNotSupported)
	}
	for _, m := range req.Messages {
		if len(m.ToolResults) > 0 {
			return nil, fmt.Errorf("ollama: tool_results in message but tools not supported: %w", gollm.ErrToolsNotSupported)
		}
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	if model == "" {
		return nil, fmt.Errorf("ollama: chat requires a model — neither ChatRequest.Model nor provider model is set (list-only construction)")
	}

	// Convert messages
	messages := make([]ollamaapi.Message, 0, len(req.Messages)+1)

	// Add system prompt as first message if provided
	if req.SystemPrompt != "" {
		messages = append(messages, ollamaapi.Message{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	for _, msg := range req.Messages {
		messages = append(messages, ollamaapi.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Build options
	options := map[string]interface{}{}
	if req.Temperature > 0 {
		options["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}

	// Non-streaming request
	stream := false
	ollamaReq := &ollamaapi.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   &stream,
		Options:  options,
	}

	var finalResp ollamaapi.ChatResponse
	err := p.client.Chat(ctx, ollamaReq, func(resp ollamaapi.ChatResponse) error {
		finalResp = resp
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: chat failed: %w", err)
	}

	// Extract token counts from timing metrics
	promptTokens := 0
	completionTokens := 0
	if finalResp.Metrics.PromptEvalCount > 0 {
		promptTokens = finalResp.Metrics.PromptEvalCount
	}
	if finalResp.Metrics.EvalCount > 0 {
		completionTokens = finalResp.Metrics.EvalCount
	}

	// Determine stop reason
	stopReason := "end_turn"
	if finalResp.DoneReason != "" {
		stopReason = finalResp.DoneReason
	}

	content := strings.TrimSpace(finalResp.Message.Content)

	return &gollm.ChatResponse{
		Content:    content,
		Model:      model,
		StopReason: stopReason,
		Usage: gollm.Usage{
			InputTokens:  promptTokens,
			OutputTokens: completionTokens,
		},
	}, nil
}
