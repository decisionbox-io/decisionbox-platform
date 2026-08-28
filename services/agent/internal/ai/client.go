package ai

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// Client provides LLM operations for the discovery agent.
type Client struct {
	provider     gollm.Provider
	providerName string
	model        string
	projectID    string
	runID        string
	debugLogger  *debug.Logger
	testMode     bool
	promptCount  int
	currentStep  int
	currentPhase string

	// policyChecker is captured once per Client so the hot LLM path
	// does not re-acquire the policy registry RWMutex on every call.
	// Nil until the first emitLLMUsage call — lazy init avoids a
	// package-init ordering problem with the cloud policy plugin
	// which registers itself in its own init().
	policyChecker policy.Checker

	// contextWindowObserver, when set, is invoked with (model, window)
	// whenever a context-overflow 400 reveals the model's true context
	// window. The orchestrator uses it to self-calibrate: re-budget the
	// rest of the run and persist the learned window per project+model
	// so a later run for the same model does not repeat the overflow.
	// Nil for callers that don't self-calibrate (tests, non-discovery).
	contextWindowObserver func(model string, window int)
}

// New creates a new AI client backed by an llm.Provider.
func New(provider gollm.Provider, model string) (*Client, error) {
	logger.WithField("model", model).Info("LLM client initialized")

	return &Client{
		provider: provider,
		model:    model,
	}, nil
}

// SetProvenance records the project/run/provider that will be attached
// to each LLM observability event emitted from this client. Callers
// that do not set this still get structured logs but the control-plane
// attribution falls back to empty values.
//
// SetProvenance also primes the policy checker cache so the first
// LLM call doesn't pay the registry lookup cost.
func (c *Client) SetProvenance(projectID, runID, providerName string) {
	c.projectID = projectID
	c.runID = runID
	c.providerName = providerName
	c.policyChecker = policy.GetChecker()
}

// ChatResult holds the full result of an LLM call (for storage/fine-tuning).
type ChatResult struct {
	Content    string
	TokensIn   int
	TokensOut  int
	DurationMs int64
}

// Chat sends a user prompt with an optional system prompt and returns the full result.
func (c *Client) Chat(ctx context.Context, userPrompt string, systemPrompt string, maxTokens int) (*ChatResult, error) {
	start := time.Now()
	messages := []gollm.Message{{Role: "user", Content: userPrompt}}
	resp, err := c.CreateMessage(ctx, messages, systemPrompt, maxTokens)
	if err != nil {
		return nil, err
	}
	return &ChatResult{
		Content:    resp.Content,
		TokensIn:   resp.Usage.InputTokens,
		TokensOut:  resp.Usage.OutputTokens,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// CreateMessage sends a message to the LLM and returns the full response.
func (c *Client) CreateMessage(ctx context.Context, messages []gollm.Message, systemPrompt string, maxTokens int) (*gollm.ChatResponse, error) {
	return c.CreateMessageWithTools(ctx, messages, systemPrompt, maxTokens, nil, "")
}

// CreateMessageWithTools is CreateMessage with native tool-calling: tools is the
// set of tools the model may call and toolChoice forces the decision ("" / "auto"
// = model decides, "any" / "required" = must call a tool, "none" = must not, or a
// specific tool name). The returned ChatResponse carries ToolCalls when the model
// invoked tools (StopReason "tool_use"). Providers whose ProviderMeta.SupportsTools
// is false reject a non-empty tools slice with gollm.ErrToolsNotSupported, so
// callers must gate on tool support before passing tools.
func (c *Client) CreateMessageWithTools(ctx context.Context, messages []gollm.Message, systemPrompt string, maxTokens int, tools []gollm.ToolDefinition, toolChoice string) (*gollm.ChatResponse, error) {
	return c.createMessage(ctx, messages, systemPrompt, maxTokens, tools, toolChoice, nil)
}

// createMessage is the single low-level LLM call: it builds the request
// (optionally carrying a ResponseFormat for structured output), retries on
// transient upstream failures, and records observability. All public Chat /
// CreateMessage* helpers funnel through here so retry, usage accounting, and
// debug logging behave identically regardless of tools or structured output.
func (c *Client) createMessage(ctx context.Context, messages []gollm.Message, systemPrompt string, maxTokens int, tools []gollm.ToolDefinition, toolChoice string, format *gollm.ResponseFormat) (*gollm.ChatResponse, error) {
	startTime := time.Now()

	if c.testMode {
		c.savePrompt(messages, systemPrompt)
	}

	if maxTokens == 0 {
		maxTokens = 4096
	}

	req := gollm.ChatRequest{
		Model:          c.model,
		SystemPrompt:   systemPrompt,
		Messages:       messages,
		MaxTokens:      maxTokens,
		Tools:          tools,
		ToolChoice:     toolChoice,
		ResponseFormat: format,
	}

	logger.WithFields(logger.Fields{
		"model":         req.Model,
		"max_tokens":    req.MaxTokens,
		"message_count": len(messages),
	}).Debug("Sending LLM request")

	// chatWithRetry wraps provider.Chat with bounded exponential
	// backoff on transient upstream failures (499 / 429 / 5xx /
	// network). Without this, a single Vertex shared-serving
	// cancellation in mid-discovery killed the whole run.
	resp, err := chatWithRetry(ctx, c.provider, req)

	// Adaptive context-overflow retry. A "maximum context length" 400 is
	// deterministic (chatWithRetry returns it immediately, no backoff), but
	// the error states the model's true window + input size, so a single
	// re-issue with a correctly-reduced max_tokens fits. Bounded to one extra
	// call; every other 4xx is untouched. When the error reveals the model's
	// real window, notify the observer so the run can self-calibrate.
	if err != nil && isContextLengthError(err) {
		if learned := windowFromContextLengthError(err); learned > 0 && c.contextWindowObserver != nil {
			c.contextWindowObserver(req.Model, learned)
		}
		if nm, ok := reducedMaxTokensForContextOverflow(req.MaxTokens, err); ok {
			logger.WithFields(logger.Fields{
				"model":          req.Model,
				"old_max_tokens": req.MaxTokens,
				"new_max_tokens": nm,
			}).Warn("LLM context-length overflow; retrying once with reduced max_tokens")
			req.MaxTokens = nm
			resp, err = chatWithRetry(ctx, c.provider, req)
		}
	}

	latencyMs := time.Since(startTime).Milliseconds()

	promptContent := ""
	for _, msg := range messages {
		promptContent += fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content)
	}

	if err != nil {
		// Observability: record the failed call. Provider may have
		// returned partial usage even on error; report what we have.
		c.emitLLMUsage(ctx, 0, 0, latencyMs, err)
		if c.debugLogger != nil {
			c.debugLogger.LogLLM(ctx, c.currentStep, c.currentPhase, c.model,
				systemPrompt, promptContent, "", 0, 0,
				latencyMs, err)
		}
		return nil, err
	}

	logger.WithFields(logger.Fields{
		"input_tokens":  resp.Usage.InputTokens,
		"output_tokens": resp.Usage.OutputTokens,
		"stop_reason":   resp.StopReason,
	}).Debug("LLM response received")

	c.emitLLMUsage(ctx, resp.Usage.InputTokens, resp.Usage.OutputTokens, latencyMs, nil)

	if c.debugLogger != nil {
		c.debugLogger.LogLLM(ctx, c.currentStep, c.currentPhase, c.model,
			systemPrompt, promptContent, resp.Content,
			resp.Usage.InputTokens, resp.Usage.OutputTokens,
			latencyMs, nil)
	}

	return resp, nil
}

// SupportsStructuredOutput reports whether the active provider honours
// ChatRequest.ResponseFormat by constraining output to a JSON Schema
// (platform#340). Callers gate on this before building a schema so an
// unsupported provider (e.g. vertex-ai, azure-foundry) transparently falls
// back to plain prompting + tolerant parsing. Returns false when the provider
// name is unknown or SetProvenance was never called.
func (c *Client) SupportsStructuredOutput() bool {
	meta, ok := gollm.GetProviderMeta(c.providerName)
	return ok && meta.SupportsStructuredOutput
}

// CreateMessageWithFormat is CreateMessage with structured output: format
// constrains the reply to a JSON Schema on providers that support it. The
// format is attached only when SupportsStructuredOutput() is true, so passing a
// non-nil format to an unsupported provider is a safe no-op (identical wire to
// a plain call). The reply's Content still carries the JSON object, so callers
// parse it exactly as they would an unconstrained response.
func (c *Client) CreateMessageWithFormat(ctx context.Context, messages []gollm.Message, systemPrompt string, maxTokens int, format *gollm.ResponseFormat) (*gollm.ChatResponse, error) {
	if format != nil && !c.SupportsStructuredOutput() {
		format = nil
	}
	return c.createMessage(ctx, messages, systemPrompt, maxTokens, nil, "", format)
}

// ChatWithFormat is Chat with structured output — the single-prompt convenience
// wrapper over CreateMessageWithFormat, mirroring Chat's ChatResult return.
func (c *Client) ChatWithFormat(ctx context.Context, userPrompt string, systemPrompt string, maxTokens int, format *gollm.ResponseFormat) (*ChatResult, error) {
	start := time.Now()
	messages := []gollm.Message{{Role: "user", Content: userPrompt}}
	resp, err := c.CreateMessageWithFormat(ctx, messages, systemPrompt, maxTokens, format)
	if err != nil {
		return nil, err
	}
	return &ChatResult{
		Content:    resp.Content,
		TokensIn:   resp.Usage.InputTokens,
		TokensOut:  resp.Usage.OutputTokens,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// emitLLMUsage is the single observability sink for every LLM call.
// Writes a structured log event (always, self-hosted and cloud alike)
// and fires the policy ObserveLLMTokens hook (a no-op on self-hosted
// via the Noop checker; on cloud, the policy plugin batches events
// and flushes to the control plane).
func (c *Client) emitLLMUsage(ctx context.Context, inTok, outTok int, latencyMs int64, callErr error) {
	fields := logger.Fields{
		"provider":      c.providerName,
		"model":         c.model,
		"project_id":    c.projectID,
		"run_id":        c.runID,
		"input_tokens":  inTok,
		"output_tokens": outTok,
		"latency_ms":    latencyMs,
		"phase":         c.currentPhase,
		"step":          c.currentStep,
	}
	if callErr != nil {
		fields["error"] = callErr.Error()
		logger.WithFields(fields).Warn("LLM call failed")
	} else {
		logger.WithFields(fields).Info("LLM call usage")
	}

	checker := c.policyChecker
	if checker == nil {
		checker = policy.GetChecker()
	}
	checker.ObserveLLMTokens(ctx, "", policy.LLMUsageEvent{
		ProjectID:    c.projectID,
		RunID:        c.runID,
		Provider:     c.providerName,
		Model:        c.model,
		InputTokens:  inTok,
		OutputTokens: outTok,
		LatencyMs:    int(latencyMs),
		OccurredAt:   time.Now().UTC(),
	})
}

// ExtractText returns the text content from a response.
func (c *Client) ExtractText(resp *gollm.ChatResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Content
}

func (c *Client) ModelName() string { return c.model }

// ProviderName returns the registered provider ID (e.g. "ollama",
// "openai", "bedrock") set via SetProvenance. Empty when SetProvenance
// was never called. Callers that need to look up catalog metadata for
// the active (provider, model) pair — e.g. the SQL fixer resolving
// MaxOutputTokens against the central registry — read it through here.
func (c *Client) ProviderName() string { return c.providerName }

func (c *Client) SetTestMode(enabled bool)        { c.testMode = enabled }
func (c *Client) SetDebugLogger(dl *debug.Logger) { c.debugLogger = dl }

// SetContextWindowObserver registers a callback invoked with (model, window)
// whenever a context-overflow 400 reveals the model's true context window.
// Nil (the default) disables self-calibration for callers that don't need it.
func (c *Client) SetContextWindowObserver(fn func(model string, window int)) {
	c.contextWindowObserver = fn
}
func (c *Client) SetStep(step int)                { c.currentStep = step }
func (c *Client) SetPhase(phase string)           { c.currentPhase = phase }

func (c *Client) savePrompt(messages []gollm.Message, systemPrompt string) {
	c.promptCount++
	promptDir := "test-prompts"
	if err := os.MkdirAll(promptDir, 0750); err != nil {
		logger.WithField("error", err).Debug("failed to create prompt dir")
		return
	}

	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%03d-%s-prompt.txt", c.promptCount, timestamp)

	var content string
	content += fmt.Sprintf("=== PROMPT #%d ===\n", c.promptCount)
	if systemPrompt != "" {
		content += "SYSTEM:\n" + systemPrompt + "\n---\n"
	}
	for i, msg := range messages {
		content += fmt.Sprintf("[Message %d - %s]\n%s\n", i+1, msg.Role, msg.Content)
	}

	if err := os.WriteFile(filepath.Join(promptDir, filename), []byte(content), 0600); err != nil {
		logger.WithField("error", err).Debug("failed to write prompt file")
	}
}
