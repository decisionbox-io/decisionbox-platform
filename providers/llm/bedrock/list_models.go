package bedrock

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	bedrockcp "github.com/aws/aws-sdk-go-v2/service/bedrock"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// bedrockControlClient is the slice of the Bedrock control-plane API
// that ListModels consumes. Defined here so tests can substitute a
// fake without spinning up a real bedrockcp.Client.
type bedrockControlClient interface {
	ListFoundationModels(ctx context.Context, in *bedrockcp.ListFoundationModelsInput, opts ...func(*bedrockcp.Options)) (*bedrockcp.ListFoundationModelsOutput, error)
	ListInferenceProfiles(ctx context.Context, in *bedrockcp.ListInferenceProfilesInput, opts ...func(*bedrockcp.Options)) (*bedrockcp.ListInferenceProfilesOutput, error)
}

// newControlClient builds a control-plane client from a resolved
// aws.Config. Held in a package var so tests can swap it for a fake
// without dragging in a real bedrockcp.NewFromConfig call. Production
// code never reassigns this.
var newControlClient = func(cfg aws.Config) bedrockControlClient {
	return bedrockcp.NewFromConfig(cfg)
}

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
	client := newControlClient(p.awsCfg)

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
