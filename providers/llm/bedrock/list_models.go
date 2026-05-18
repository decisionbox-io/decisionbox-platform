package bedrock

import (
	"context"
	"fmt"

	bedrockcp "github.com/aws/aws-sdk-go-v2/service/bedrock"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels calls the Bedrock control-plane ListFoundationModels API
// (not bedrockruntime). Returns every text-capable model in the region
// that supports ON_DEMAND or INFERENCE_PROFILE delivery.
//
// Reuses the provider's awsCfg so the control-plane client inherits
// whichever credentials the factory built from auth_method
// (access_keys / assume_role / iam_role). A fresh LoadDefaultConfig
// here would silently ignore dashboard-supplied access keys and fall
// through to the SDK's ambient chain — exactly the regression that
// surfaced as "no EC2 IMDS role found" on local Docker setups whose
// shell has no AWS_* env vars.
func (p *BedrockProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	client := bedrockcp.NewFromConfig(p.awsCfg)

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
