// Package bedrock provides an embedding.Provider backed by AWS Bedrock.
// Uses AWS SDK v2 with default credential chain (IAM role, env vars, ~/.aws/credentials).
//
// Register via init():
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/embedding/bedrock"
//
// Supported models:
//   - amazon.titan-embed-text-v2:0 (1024 dims, recommended)
//   - amazon.titan-embed-text-v1:2 (1536 dims, legacy)
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

var modelDimensions = map[string]int{
	"amazon.titan-embed-text-v2:0": 1024,
	"amazon.titan-embed-text-v1:2": 1536,
}

func init() {
	goembedding.RegisterWithMeta("bedrock", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		region := cfg["region"]
		if region == "" {
			region = "us-east-1"
		}

		model := cfg["model"]
		if model == "" {
			model = "amazon.titan-embed-text-v2:0"
		}

		dims, ok := modelDimensions[model]
		if !ok {
			return nil, fmt.Errorf("bedrock embedding: unsupported model %q (supported: amazon.titan-embed-text-v2:0, amazon.titan-embed-text-v1:2)", model)
		}

		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(region),
		)
		if err != nil {
			return nil, fmt.Errorf("bedrock embedding: failed to load AWS config: %w", err)
		}

		client := bedrockruntime.NewFromConfig(awsCfg)

		return newProvider(client, region, model, dims), nil
	}, goembedding.ProviderMeta{
		Name:        "AWS Bedrock",
		Description: "Amazon Titan embeddings — IAM auth, no API key needed",
		ConfigFields: []goembedding.ConfigField{
			{Key: "region", Label: "AWS Region", Type: "string", Default: "us-east-1", Placeholder: "us-east-1"},
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "amazon.titan-embed-text-v2:0"},
		},
		Models: []goembedding.ModelInfo{
			{ID: "amazon.titan-embed-text-v2:0", Name: "Titan Text Embeddings V2", Dimensions: 1024},
			{ID: "amazon.titan-embed-text-v1:2", Name: "Titan Text Embeddings V1", Dimensions: 1536},
		},
	})
}

// bedrockClient abstracts the AWS Bedrock Runtime API for testing.
type bedrockClient interface {
	InvokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// Compile-time check that the real client satisfies the interface.
var _ bedrockClient = (*bedrockruntime.Client)(nil)

// provider implements embedding.Provider using AWS Bedrock.
type provider struct {
	client bedrockClient
	region string
	model  string
	dims   int
}

func newProvider(client bedrockClient, region, model string, dims int) *provider {
	return &provider{
		client: client,
		region: region,
		model:  model,
		dims:   dims,
	}
}

// Embed generates vector embeddings for the given texts.
// Bedrock InvokeModel accepts one text at a time, so we loop for batch inputs.
func (p *provider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	result := make([][]float64, len(texts))
	for i, text := range texts {
		vec, err := p.embedSingle(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("bedrock embedding: failed on input %d: %w", i, err)
		}
		result[i] = vec
	}

	return result, nil
}

func (p *provider) embedSingle(ctx context.Context, text string) ([]float64, error) {
	var reqBody []byte
	var err error

	if p.model == "amazon.titan-embed-text-v2:0" {
		reqBody, err = json.Marshal(titanV2Request{
			InputText:  text,
			Dimensions: p.dims,
			Normalize:  true,
		})
	} else {
		reqBody, err = json.Marshal(titanV1Request{
			InputText: text,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	contentType := "application/json"
	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     &p.model,
		ContentType: &contentType,
		Accept:      &contentType,
		Body:        reqBody,
	})
	if err != nil {
		return nil, fmt.Errorf("InvokeModel failed: %w", err)
	}

	var resp titanResponse
	if err := json.Unmarshal(output.Body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(resp.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding in response")
	}

	return resp.Embedding, nil
}

// Dimensions returns the vector dimensionality for this model.
func (p *provider) Dimensions() int {
	return p.dims
}

// ModelName returns the model identifier.
func (p *provider) ModelName() string {
	return p.model
}

// Validate checks that AWS credentials are valid and the model is accessible.
func (p *provider) Validate(ctx context.Context) error {
	_, err := p.Embed(ctx, []string{"test"})
	return err
}

// Titan V2 request body — supports configurable dimensions and normalization.
type titanV2Request struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions"`
	Normalize  bool   `json:"normalize"`
}

// Titan V1 request body — fixed dimensions, no normalize parameter.
type titanV1Request struct {
	InputText string `json:"inputText"`
}

// titanResponse is the response body for both Titan V1 and V2.
type titanResponse struct {
	Embedding           []float64 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}
