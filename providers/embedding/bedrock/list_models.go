package bedrock

import (
	"context"
	"fmt"

	bedrockcp "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// Compile-time check: provider satisfies the optional ModelLister
// capability so the API's live-list endpoint can enumerate Bedrock
// embedding models for the dashboard.
var _ goembedding.ModelLister = (*provider)(nil)

// ListModels calls the Bedrock control-plane ListFoundationModels API
// (not bedrockruntime) filtered to models that emit the EMBEDDING
// output modality. Returns the live set of embedding models the
// caller's IAM identity can actually invoke in the configured region.
//
// Reuses the provider's awsCfg so the control-plane client inherits
// whichever credentials the factory built from auth_method
// (access_keys / assume_role / iam_role). A fresh LoadDefaultConfig
// here would silently ignore dashboard-supplied access keys and fall
// through to the SDK's ambient chain — exactly the regression that
// surfaced as "no EC2 IMDS role found" on local Docker setups whose
// shell has no AWS_* env vars.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	client := bedrockcp.NewFromConfig(p.awsCfg)

	resp, err := client.ListFoundationModels(ctx, &bedrockcp.ListFoundationModelsInput{
		ByOutputModality: bedrocktypes.ModelModalityEmbedding,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock embedding: list foundation models: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(resp.ModelSummaries))
	for _, s := range resp.ModelSummaries {
		if s.ModelId == nil || *s.ModelId == "" {
			continue
		}
		id := *s.ModelId
		name := id
		if s.ModelName != nil && *s.ModelName != "" {
			name = *s.ModelName
		}
		lifecycle := ""
		if s.ModelLifecycle != nil {
			lifecycle = string(s.ModelLifecycle.Status)
		}
		out = append(out, goembedding.RemoteModel{
			ID:          id,
			DisplayName: name,
			// Bedrock's ListFoundationModels doesn't carry vector
			// dimensions; the dashboard surfaces "dimensions unknown"
			// for live rows we can't size, and our factory rejects
			// unsupported model IDs at construction time so the
			// missing dim doesn't reach Qdrant.
			Dimensions: modelDimensions[id],
			Lifecycle:  lifecycle,
		})
	}
	return out, nil
}
