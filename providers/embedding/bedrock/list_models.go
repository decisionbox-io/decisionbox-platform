package bedrock

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
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
// We re-load AWS config rather than reusing bedrockruntime's client
// because that client doesn't expose its underlying cfg. The
// control-plane API is free and doesn't consume embedding quota.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(p.region))
	if err != nil {
		return nil, fmt.Errorf("bedrock embedding: list models: load aws config: %w", err)
	}
	client := bedrockcp.NewFromConfig(awsCfg)

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
