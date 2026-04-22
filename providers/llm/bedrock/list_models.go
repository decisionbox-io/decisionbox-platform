package bedrock

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	bedrockcp "github.com/aws/aws-sdk-go-v2/service/bedrock"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels calls the Bedrock control-plane ListFoundationModels API
// (not bedrockruntime). Returns every text-capable model in the region
// that supports ON_DEMAND or INFERENCE_PROFILE delivery.
//
// We use a fresh AWS config loaded with the provider's region because
// the runtime client does not expose its underlying cfg.
func (p *BedrockProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(p.region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: list models: load aws config: %w", err)
	}
	client := bedrockcp.NewFromConfig(cfg)

	out := make([]gollm.RemoteModel, 0, 64)

	// Foundation models.
	fm, err := client.ListFoundationModels(ctx, &bedrockcp.ListFoundationModelsInput{})
	if err != nil {
		return nil, fmt.Errorf("bedrock: list foundation models: %w", err)
	}
	for _, s := range fm.ModelSummaries {
		id := ""
		if s.ModelId != nil {
			id = *s.ModelId
		}
		if id == "" {
			continue
		}
		name := id
		if s.ModelName != nil && *s.ModelName != "" {
			name = *s.ModelName
		}
		lifecycle := ""
		if s.ModelLifecycle != nil {
			lifecycle = string(s.ModelLifecycle.Status)
		}
		out = append(out, gollm.RemoteModel{ID: id, DisplayName: name, Lifecycle: lifecycle})
	}

	// Inference profiles (e.g. global. / us. prefixed IDs). These are
	// what a caller actually passes to InvokeModel for newer models.
	ip, err := client.ListInferenceProfiles(ctx, &bedrockcp.ListInferenceProfilesInput{})
	if err == nil { // non-fatal — some regions/accounts don't support it
		for _, s := range ip.InferenceProfileSummaries {
			id := ""
			if s.InferenceProfileId != nil {
				id = *s.InferenceProfileId
			}
			if id == "" {
				continue
			}
			name := id
			if s.InferenceProfileName != nil && *s.InferenceProfileName != "" {
				name = *s.InferenceProfileName
			}
			out = append(out, gollm.RemoteModel{ID: id, DisplayName: name, Lifecycle: string(s.Status)})
		}
	}

	return out, nil
}
