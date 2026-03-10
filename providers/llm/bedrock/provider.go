// Package bedrock provides an llm.Provider for AWS Bedrock.
// Bedrock hosts Claude, Llama, and other models with AWS-native auth.
//
// Status: STUB — registers the provider so it appears in the registry.
// Full implementation coming soon.
//
// Configuration:
//
//	LLM_PROVIDER=bedrock
//	LLM_MODEL=anthropic.claude-sonnet-4-20250514-v1:0
//	AWS_REGION=us-east-1
//	AWS_ACCESS_KEY_ID=...
//	AWS_SECRET_ACCESS_KEY=...
package bedrock

import (
	"context"
	"fmt"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func init() {
	gollm.Register("bedrock", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		region := cfg["region"]
		if region == "" {
			region = "us-east-1"
		}
		model := cfg["model"]
		if model == "" {
			return nil, fmt.Errorf("bedrock: model is required")
		}

		return &BedrockProvider{
			region: region,
			model:  model,
		}, nil
	})
}

// BedrockProvider implements llm.Provider for AWS Bedrock.
type BedrockProvider struct {
	region string
	model  string
}

// Chat sends a conversation to AWS Bedrock.
// STUB: returns an error with setup instructions.
func (p *BedrockProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return nil, fmt.Errorf(
		"bedrock provider is not yet implemented. "+
			"Use LLM_PROVIDER=claude or LLM_PROVIDER=openai for now. "+
			"AWS Bedrock support is coming soon. "+
			"Config: region=%s, model=%s",
		p.region, p.model,
	)
}
