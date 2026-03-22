package bedrock

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// bedrockClient abstracts the AWS Bedrock Runtime API for testing.
// The real implementation is *bedrockruntime.Client.
type bedrockClient interface {
	InvokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// Compile-time check that the real client satisfies the interface.
var _ bedrockClient = (*bedrockruntime.Client)(nil)
