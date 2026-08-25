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
	"strconv"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
)

// ollamaDefaultTimeout is the default HTTP timeout for Ollama calls.
// At ~20 tokens/sec on a 31B-class local model, 5 minutes capped
// generation at ~6k tokens — below the working size of a
// reasoning-on long-form response. 15 minutes raises the ceiling
// to ~18k tokens with comfortable headroom, while still bounding a
// runaway. Operators override per-call via LLM_TIMEOUT or
// per-project timeout_seconds.
const ollamaDefaultTimeout = 15 * time.Minute

// ollamaDefaultMaxOutputTokens is the output-token cap applied to any
// model the catalog (catalog.go) does not list. Set generously (128k):
// Ollama treats num_predict as a soft ceiling — generation stops at EOS
// and an oversized value is never rejected upstream — so a high default
// keeps long-form generations (e.g. pack synthesis) from truncating on
// uncatalogued local models. Catalogued models still get their exact cap.
const ollamaDefaultMaxOutputTokens = 131072

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

		// num_ctx is the per-request context-window override sent to
		// Ollama. Zero (unset / empty / unparseable / negative) leaves
		// it off entirely so the server's OLLAMA_CONTEXT_LENGTH default
		// applies — avoids forcing a 128k KV cache load on hosts that
		// were running at 4k/8k. Operators raise it deliberately when
		// they want the model's full architectural window.
		numCtx, _ := strconv.Atoi(cfg["num_ctx"])
		if numCtx < 0 {
			numCtx = 0
		}

		timeout := gollm.ResolveHTTPTimeout(cfg, ollamaDefaultTimeout)
		httpClient, err := gollm.HTTPClientFor(cfg, timeout)
		if err != nil {
			return nil, fmt.Errorf("ollama: %w", err)
		}
		return NewOllamaProviderWithClient(host, model, httpClient, numCtx)
	}, gollm.ProviderMeta{
		Name:        "Ollama (Local)",
		Description: "Run open-source models locally via Ollama",
		ConfigFields: append([]gollm.ConfigField{
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
			{
				Key:         "num_ctx",
				Label:       "Context window (num_ctx)",
				Type:        "string",
				Placeholder: "32768",
				Description: "Optional per-request context window override (token count). Leave blank to use the Ollama server's OLLAMA_CONTEXT_LENGTH default. Setting a higher value than the server default forces a larger KV cache allocation and can OOM on tight VRAM.",
			},
		}, gollm.TLSConfigFields()...),
		Models: buildOllamaCatalog(),
		// Fallbacks for models the catalog does not list. Output uses the
		// generous local-model cap (see ollamaDefaultMaxOutputTokens);
		// context defaults to 128k — the de-facto window for current
		// Ollama models — instead of the conservative global fallback.
		DefaultMaxOutputTokens: ollamaDefaultMaxOutputTokens,
		DefaultMaxInputTokens:  ctx128K,
		// Ollama dispatches every model through one SDK path with no
		// wire switch, so any model the server has pulled (returned by
		// /api/tags) is dispatchable. Without this flag, live-only rows
		// for tags not in the catalog come back with Wire="" +
		// Dispatchable=false and the dashboard hides them under the
		// "unsupported wire" filter.
		DispatchAnyModelID: true,
		// Ollama strictly requires the EXACT model:tag the user pulled.
		// `ollama run qwen3` when only `qwen3:32b` is local returns 404.
		// So the picker must save the live ID (qwen3:32b), not the
		// catalog canonical (qwen3). FindModel keeps working at runtime
		// because the catalog row's aliases include the tagged forms,
		// so max-tokens enrichment still resolves.
		PreferLiveModelID: true,
		// Clamp the input window budgeting call-sites see to the
		// operator-configured num_ctx when it's lower than the
		// catalog. Without this, /ask would assemble prompts up to
		// the model's architectural window and then trip the
		// server's Truncate=false guard when the operator has
		// deliberately chosen a smaller context — the request would
		// fail instead of the prompt trimming gracefully.
		EffectiveInputWindow: ollamaEffectiveInputWindow,
	})
}

// ollamaEffectiveInputWindow returns the input-window cap budgeting
// callers should respect. It mirrors how Chat() resolves num_ctx:
// start from the catalog's MaxInputTokens for the model, then clamp
// to the operator's cfg["num_ctx"] when set and smaller. Zero /
// missing / unparseable cfg values leave the catalog value
// untouched.
func ollamaEffectiveInputWindow(model string, cfg gollm.ProviderConfig) int {
	base := gollm.GetMaxInputTokens("ollama", model)
	if cfg == nil {
		return base
	}
	cap, _ := strconv.Atoi(cfg["num_ctx"])
	if cap > 0 && cap < base {
		return cap
	}
	return base
}

// OllamaProvider implements llm.Provider using a local Ollama instance.
// httpTimeout is retained on the provider so callers and tests can
// inspect the effective deadline — the ollama SDK's *api.Client wraps
// the underlying *http.Client behind unexported fields.
type OllamaProvider struct {
	client      ollamaClient
	model       string
	httpTimeout time.Duration
	// numCtx, when > 0, is forwarded as `options["num_ctx"]` on every
	// Chat call. Zero leaves the field off entirely so the server's
	// OLLAMA_CONTEXT_LENGTH default applies — avoids OOMing on hosts
	// that were running at 4k/8k when a catalog model happens to
	// publish a much larger architectural window.
	numCtx int
}

// NewOllamaProvider creates a new Ollama LLM provider. A zero or
// negative timeout falls back to ollamaDefaultTimeout so callers that
// don't care (mainly tests) don't have to think about it. numCtx of
// zero or negative means "don't send num_ctx; use server default".
// Uses Go's default transport; for a TLS-fronted Ollama with a custom
// CA, build the client with gollm.HTTPClientFor and use
// NewOllamaProviderWithClient.
func NewOllamaProvider(host, model string, timeout time.Duration, numCtx int) (*OllamaProvider, error) {
	if timeout <= 0 {
		timeout = ollamaDefaultTimeout
	}
	return NewOllamaProviderWithClient(host, model, &http.Client{Timeout: timeout}, numCtx)
}

// NewOllamaProviderWithClient creates a provider using a caller-supplied
// *http.Client. The factory uses this to inject a client whose transport
// trusts a per-project custom CA or skips verification. A nil client
// falls back to a default client with ollamaDefaultTimeout.
func NewOllamaProviderWithClient(host, model string, httpClient *http.Client, numCtx int) (*OllamaProvider, error) {
	parsedURL, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("ollama: invalid host URL: %w", err)
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: ollamaDefaultTimeout}
	}
	client := ollamaapi.NewClient(parsedURL, httpClient)

	if numCtx < 0 {
		numCtx = 0
	}
	return &OllamaProvider{
		client:      client,
		model:       model,
		httpTimeout: httpClient.Timeout,
		numCtx:      numCtx,
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

	// num_ctx — sent only when the operator explicitly configured it.
	// Per-request num_ctx is an OVERRIDE on Ollama (not capped by
	// OLLAMA_CONTEXT_LENGTH), so a catalog-driven default would force
	// the server to allocate the model's full architectural window
	// (often 128k+) on every call — fatal on hosts that were running
	// the same model at 4k/8k for VRAM reasons. Leaving num_ctx off
	// here preserves the server's existing per-deployment behaviour.
	// Combined with Truncate=false below, an oversize prompt against
	// the server's default window now surfaces as a loud error
	// instead of silent prompt-history truncation.
	if p.numCtx > 0 {
		options["num_ctx"] = p.numCtx
	}

	// Non-streaming request
	stream := false
	// truncate=false makes the server return an error when the rendered
	// prompt exceeds num_ctx instead of silently trimming the chat
	// history. Pair with the explicit num_ctx above: silent truncation
	// at either end produces malformed output without any signal to
	// the caller.
	truncate := false
	ollamaReq := &ollamaapi.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   &stream,
		Options:  options,
		Truncate: &truncate,
		Think:    reasoningEffortToThinkValue(req.ReasoningEffort, gollm.IsReasoningModel("ollama", model)),
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
	if finalResp.PromptEvalCount > 0 {
		promptTokens = finalResp.PromptEvalCount
	}
	if finalResp.EvalCount > 0 {
		completionTokens = finalResp.EvalCount
	}

	// Determine stop reason
	stopReason := "end_turn"
	if finalResp.DoneReason != "" {
		stopReason = finalResp.DoneReason
	}

	content := strings.TrimSpace(finalResp.Message.Content)
	reasoning := finalResp.Message.Thinking

	return &gollm.ChatResponse{
		Content:    content,
		Model:      model,
		StopReason: stopReason,
		Usage: gollm.Usage{
			InputTokens:  promptTokens,
			OutputTokens: completionTokens,
		},
		Reasoning: reasoning,
	}, nil
}

// reasoningEffortToThinkValue maps the wire-neutral
// gollm.ChatRequest.ReasoningEffort to Ollama's Think field. Returns
// nil whenever the request would otherwise be rejected by the server,
// which falls into two cases:
//   - effort is empty or unknown — the caller is fine with the
//     model's default behaviour; omit the field.
//   - the model is not reasoning-capable and the caller asked for a
//     non-Off effort — Ollama returns HTTP 400 "<model> does not
//     support thinking" on `Think.Bool()==true`, including the
//     effort-string values ("low"/"medium"/"high"). Omit the field
//     so non-reasoning models silently ignore the request instead of
//     erroring.
//
// "Off" is always honoured: passing think=false is harmless on every
// model and lets a caller explicitly suppress reasoning without
// knowing in advance whether the model would have emitted it.
func reasoningEffortToThinkValue(effort string, modelIsReasoning bool) *ollamaapi.ThinkValue {
	switch effort {
	case gollm.ReasoningEffortDefault:
		return nil
	case gollm.ReasoningEffortOff:
		return &ollamaapi.ThinkValue{Value: false}
	case gollm.ReasoningEffortOn:
		if !modelIsReasoning {
			return nil
		}
		return &ollamaapi.ThinkValue{Value: true}
	case gollm.ReasoningEffortLow, gollm.ReasoningEffortMedium, gollm.ReasoningEffortHigh:
		if !modelIsReasoning {
			return nil
		}
		return &ollamaapi.ThinkValue{Value: effort}
	default:
		return nil
	}
}
