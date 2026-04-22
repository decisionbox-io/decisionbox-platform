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

func init() {
	gollm.RegisterWithMeta("ollama", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		host := cfg["host"]
		if host == "" {
			host = "http://localhost:11434"
		}

		model := cfg["model"]
		if model == "" {
			return nil, fmt.Errorf("ollama: model is required")
		}

		return NewOllamaProvider(host, model)
	}, gollm.ProviderMeta{
		Name:        "Ollama (Local)",
		Description: "Run open-source models locally via Ollama",
		ConfigFields: []gollm.ConfigField{
			{Key: "host", Label: "Ollama Host", Type: "string", Default: "http://localhost:11434", Placeholder: "http://localhost:11434"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "qwen2.5:7b", Placeholder: "qwen2.5:7b"},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"_default": {InputPerMillion: 0, OutputPerMillion: 0},
		},
		// Max output tokens per Ollama model tag. GetMaxOutputTokens does exact match
		// then falls back to "_default". Values come from each model family's
		// documented generation limits. Coverage is scoped to the biggest / most
		// common Qwen, Gemma, DeepSeek, and Meta (Llama) models — everything else
		// picks up the 16384 default. Both bare names (qwen3) and ":latest"
		// aliases are listed because user configs can contain either form.
		MaxOutputTokens: map[string]int{
			// Qwen 3.6 / 3.5 — model card lists max_tokens=81920; 64K matches the
			// hosted Qwen-Plus tier and leaves headroom without overcommitting.
			"qwen3.6":         65536,
			"qwen3.6:latest":  65536,
			"qwen3.6:35b-a3b": 65536,
			"qwen3.5":         65536,
			"qwen3.5:latest":  65536,
			"qwen3.5:122b":    65536,

			// DeepSeek R1 — reasoning chains need the long tail
			// (R1 default 32K, max 64K with reasoner).
			"deepseek-r1":        32768,
			"deepseek-r1:latest": 32768,
			"deepseek-r1:14b":    32768,
			"deepseek-r1:32b":    32768,
			"deepseek-r1:70b":    32768,
			"deepseek-r1:671b":   32768,

			// Qwen 3 — tech report recommends 32768 for standard output.
			"qwen3":           32768,
			"qwen3:latest":    32768,
			"qwen3:30b-a3b":   32768,
			"qwen3:32b":       32768,
			"qwen3:235b":      32768,
			"qwen3:235b-a22b": 32768,

			// DeepSeek V3.
			"deepseek-v3":        16384,
			"deepseek-v3:latest": 16384,
			"deepseek-v3.2":      16384,

			// Qwen 2.5 — model card sets max_new_tokens=16384.
			"qwen2.5":            16384,
			"qwen2.5:latest":     16384,
			"qwen2.5:32b":        16384,
			"qwen2.5:72b":        16384,
			"qwen2.5-coder":      16384,
			"qwen2.5-coder:32b":  16384,

			// Gemma 3 — paid-tier providers expose 16384 output.
			"gemma3":        16384,
			"gemma3:latest": 16384,
			"gemma3:27b":    16384,

			// Llama 4 — huge context window but practical output is 8K.
			"llama4":          8192,
			"llama4:latest":   8192,
			"llama4:scout":    8192,
			"llama4:maverick": 8192,

			// Llama 3.x — 128K context, 8K is the common practical generation cap.
			"llama3.3":        8192,
			"llama3.3:latest": 8192,
			"llama3.3:70b":    8192,
			"llama3.1":        8192,
			"llama3.1:latest": 8192,
			"llama3.1:70b":    8192,
			"llama3.1:405b":   8192,

			// Gemma 2 — 8K context, so output caps at 8K.
			"gemma2":        8192,
			"gemma2:latest": 8192,
			"gemma2:27b":    8192,

			// Modern floor for any Ollama model not listed above. Raised from 8192
			// because the old cap truncated long analyses on capable models.
			"_default": 16384,
		},
	})
}

// OllamaProvider implements llm.Provider using a local Ollama instance.
type OllamaProvider struct {
	client ollamaClient
	model  string
}

// NewOllamaProvider creates a new Ollama LLM provider.
func NewOllamaProvider(host, model string) (*OllamaProvider, error) {
	parsedURL, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("ollama: invalid host URL: %w", err)
	}

	client := ollamaapi.NewClient(parsedURL, &http.Client{Timeout: 5 * time.Minute})

	return &OllamaProvider{
		client: client,
		model:  model,
	}, nil
}

// Validate checks that Ollama is reachable and the model is available.
// Uses the List API — no inference cost.
func (p *OllamaProvider) Validate(ctx context.Context) error {
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
	model := req.Model
	if model == "" {
		model = p.model
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
