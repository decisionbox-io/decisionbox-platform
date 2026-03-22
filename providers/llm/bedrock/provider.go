// Package bedrock provides an llm.Provider for AWS Bedrock.
// Bedrock hosts Claude, Llama, Mistral, and other models with AWS-native auth.
//
// Routes based on model prefix:
//   - anthropic.* → Anthropic Messages API format
//   - meta.* → Meta Llama format (future)
//   - mistral.* → Mistral format (future)
//
// Configuration:
//
//	LLM_PROVIDER=bedrock
//	LLM_MODEL=anthropic.claude-sonnet-4-20250514-v1:0
//	region in project LLM config (default: us-east-1)
//
// Authentication: AWS credentials (IAM role, env vars, or ~/.aws/credentials).
package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
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
			client:     client,
			region:     region,
			model:      model,
			httpClient: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		}, nil
	}, gollm.ProviderMeta{
		Name:        "AWS Bedrock",
		Description: "AWS-managed AI platform — Claude, Llama, Mistral with IAM auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "region", Label: "AWS Region", Type: "string", Default: "us-east-1"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "anthropic.claude-sonnet-4-20250514-v1:0", Placeholder: "anthropic.claude-sonnet-4-20250514-v1:0"},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"claude-sonnet-4":   {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4":     {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
		},
	})
}

// BedrockProvider implements llm.Provider for AWS Bedrock.
// Routes to different API formats based on model prefix.
type BedrockProvider struct {
	client     bedrockClient
	region     string
	model      string
	httpClient *http.Client
}

// Validate checks that AWS credentials are valid and the model is accessible.
// Makes a minimal request (max_tokens=1) to verify auth and model access.
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

// Chat sends a conversation to AWS Bedrock.
// Routes to the correct format based on model prefix.
func (p *BedrockProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}

	if isAnthropic(req.Model) {
		return p.claudeChat(ctx, req)
	}

	return nil, fmt.Errorf("bedrock: unsupported model %q — currently supports anthropic.* models. Meta Llama and Mistral support coming soon", req.Model)
}

// isAnthropic returns true if the model is an Anthropic Claude model.
func isAnthropic(model string) bool {
	return strings.HasPrefix(model, "anthropic.") || strings.Contains(model, "claude")
}
