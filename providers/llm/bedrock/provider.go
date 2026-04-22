// Package bedrock provides an llm.Provider for AWS Bedrock.
// Bedrock hosts Claude, Llama, Mistral, Qwen, DeepSeek, and other models
// behind a single IAM-authenticated endpoint.
//
// Dispatch is catalog-driven: each model's wire (Anthropic Messages vs.
// OpenAI /chat/completions) is looked up in libs/go-common/llm/modelcatalog
// at request time. Models not in the catalog can be routed explicitly via
// the optional wire_override config key (project.llm.wire_override).
//
// Configuration:
//
//	LLM_PROVIDER=bedrock
//	LLM_MODEL=anthropic.claude-sonnet-4-20250514-v1:0
//	region in project LLM config (default: us-east-1)
//	wire_override=anthropic|openai-compat  (optional; required for models
//	                                        that are not yet in the catalog)
//
// Authentication: AWS credentials (IAM role, env vars, or ~/.aws/credentials).
package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

func init() {
	gollm.RegisterWithMeta("bedrock", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		region := cfg["region"]
		if region == "" {
			region = "us-east-1"
		}
		model := cfg["model"]
		if model == "" {
			return nil, fmt.Errorf("bedrock: model is required")
		}

		wireOverride := modelcatalog.Unknown
		if raw := cfg["wire_override"]; raw != "" {
			parsed := modelcatalog.ParseWire(raw)
			if !parsed.Valid() {
				return nil, fmt.Errorf(
					"bedrock: invalid wire_override %q; use one of: %s, %s, %s",
					raw, modelcatalog.Anthropic, modelcatalog.OpenAICompat, modelcatalog.GoogleNative,
				)
			}
			wireOverride = parsed
		}

		timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
		if timeoutSec == 0 {
			timeoutSec = 300
		}

		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(region),
		)
		if err != nil {
			return nil, fmt.Errorf("bedrock: failed to load AWS config: %w", err)
		}

		client := bedrockruntime.NewFromConfig(awsCfg)

		return &BedrockProvider{
			client:       client,
			region:       region,
			model:        model,
			wireOverride: wireOverride,
			httpClient:   &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		}, nil
	}, gollm.ProviderMeta{
		Name:        "AWS Bedrock",
		Description: "AWS-managed AI platform — Claude, Qwen, DeepSeek, Mistral, Llama with IAM auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "region", Label: "AWS Region", Type: "string", Default: "us-east-1"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "anthropic.claude-sonnet-4-20250514-v1:0", Placeholder: "anthropic.claude-sonnet-4-20250514-v1:0"},
			{Key: "wire_override", Label: "Wire override", Type: "string", Description: "Only for models not in the catalog. One of: anthropic, openai-compat."},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4-6": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4-5":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-1":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4":   {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4":     {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
		},
		MaxOutputTokens: map[string]int{
			"claude-opus-4-6":   128000,
			"claude-sonnet-4-6": 64000,
			"claude-sonnet-4-5": 64000,
			"claude-opus-4-5":   64000,
			"claude-opus-4-1":   32000,
			"claude-sonnet-4":   64000,
			"claude-opus-4":     32000,
			"claude-haiku-4-5":  64000,
			"_default":          16384,
		},
	})
}

// BedrockProvider implements llm.Provider for AWS Bedrock.
// Routes to different wire formats based on the model catalog.
type BedrockProvider struct {
	client       bedrockClient
	region       string
	model        string
	wireOverride modelcatalog.Wire
	httpClient   *http.Client
}

// Validate checks that AWS credentials are valid and the configured model is
// reachable. Makes a minimal request (max_tokens=1) so it exercises the
// same dispatch path as a real call.
func (p *BedrockProvider) Validate(ctx context.Context) error {
	_, err := p.Chat(ctx, gollm.ChatRequest{
		Model:     p.model,
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err != nil {
		return fmt.Errorf("bedrock: validation failed: %w", err)
	}
	return nil
}

// Chat sends a conversation to AWS Bedrock, dispatching on the wire format
// resolved from the model catalog (or the configured wire_override).
func (p *BedrockProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.dispatch(ctx, req)
}
