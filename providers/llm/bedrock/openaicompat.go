package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

// chatOpenAICompat sends a request to a model on Bedrock that speaks the
// OpenAI /chat/completions wire (Qwen, DeepSeek, Mistral, Llama variants).
// Unlike Claude-on-Bedrock — which exposes the Anthropic Messages body
// directly on InvokeModel — these models accept the plain OpenAI body.
// The response, served by the same InvokeModel endpoint, comes back in
// OpenAI shape and is parsed by the shared openaicompat helper.
//
// Bedrock InvokeModel ignores the "model" field in the body (the model is
// carried in the ModelId argument) but some backends reject the request
// outright if "model" is missing, so we leave the shared helper's default
// in place.
func (p *BedrockProvider) chatOpenAICompat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	body := openaicompat.BuildRequestBody(req.Model, req)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock/openai-compat: failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        reqBody,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock/openai-compat: InvokeModel failed: %w", err)
	}

	// Bedrock wraps the upstream response verbatim in output.Body when the
	// call succeeds. On a non-2xx the AWS SDK returns an error above, so
	// we do not need to inspect the body for error envelopes here — any
	// OpenAI-style "error" field would only appear if the upstream produced
	// one inside a 200 response, which is not a documented mode.
	resp, err := openaicompat.ParseResponseBody(output.Body)
	if err != nil {
		return nil, fmt.Errorf("bedrock/openai-compat: %w", err)
	}
	return resp, nil
}
