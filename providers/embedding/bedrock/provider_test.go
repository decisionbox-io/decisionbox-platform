package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// mockClient implements bedrockClient for testing.
type mockClient struct {
	invokeFunc func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

func (m *mockClient) InvokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	return m.invokeFunc(ctx, params, optFns...)
}

func TestRegistration(t *testing.T) {
	names := goembedding.RegisteredProviders()
	found := false
	for _, n := range names {
		if n == "bedrock" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected bedrock to be registered")
	}
}

func TestRegistrationMeta(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("bedrock")
	if !ok {
		t.Fatal("expected bedrock metadata to exist")
	}
	if meta.Name != "AWS Bedrock" {
		t.Errorf("expected Name='AWS Bedrock', got %s", meta.Name)
	}
	if len(meta.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(meta.Models))
	}
	if meta.Models[0].Dimensions != 1024 {
		t.Errorf("expected first model dims=1024, got %d", meta.Models[0].Dimensions)
	}
}

func TestFactoryUnsupportedModel(t *testing.T) {
	_, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"model": "unsupported-model",
	})
	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedSingleTextV2(t *testing.T) {
	expectedVec := []float64{0.1, 0.2, 0.3}
	model := "amazon.titan-embed-text-v2:0"

	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			if *params.ModelId != model {
				t.Errorf("expected model %s, got %s", model, *params.ModelId)
			}

			var req titanV2Request
			json.Unmarshal(params.Body, &req)
			if req.InputText != "hello world" {
				t.Errorf("expected inputText 'hello world', got %s", req.InputText)
			}
			if req.Dimensions != 1024 {
				t.Errorf("expected dimensions 1024, got %d", req.Dimensions)
			}
			if !req.Normalize {
				t.Error("expected normalize=true")
			}

			respBody, _ := json.Marshal(titanResponse{
				Embedding:           expectedVec,
				InputTextTokenCount: 2,
			})
			return &bedrockruntime.InvokeModelOutput{Body: respBody}, nil
		},
	}

	p := newProvider(client, "us-east-1", model, 1024)
	result, err := p.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(result[0]))
	}
	if result[0][0] != 0.1 {
		t.Errorf("expected first value 0.1, got %f", result[0][0])
	}
}

func TestEmbedSingleTextV1(t *testing.T) {
	model := "amazon.titan-embed-text-v1:2"

	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			// V1 request should NOT have dimensions or normalize
			var raw map[string]interface{}
			json.Unmarshal(params.Body, &raw)
			if _, hasDims := raw["dimensions"]; hasDims {
				t.Error("V1 request should not have dimensions field")
			}
			if _, hasNorm := raw["normalize"]; hasNorm {
				t.Error("V1 request should not have normalize field")
			}
			if raw["inputText"] != "test text" {
				t.Errorf("expected inputText 'test text', got %v", raw["inputText"])
			}

			respBody, _ := json.Marshal(titanResponse{
				Embedding:           make([]float64, 1536),
				InputTextTokenCount: 2,
			})
			return &bedrockruntime.InvokeModelOutput{Body: respBody}, nil
		},
	}

	p := newProvider(client, "us-east-1", model, 1536)
	result, err := p.Embed(context.Background(), []string{"test text"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 1536 {
		t.Fatalf("expected 1536 dims, got %d", len(result[0]))
	}
}

func TestEmbedBatch(t *testing.T) {
	callCount := 0
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			callCount++
			respBody, _ := json.Marshal(titanResponse{
				Embedding: make([]float64, 1024),
			})
			return &bedrockruntime.InvokeModelOutput{Body: respBody}, nil
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	result, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
	if callCount != 3 {
		t.Errorf("expected 3 InvokeModel calls (one per text), got %d", callCount)
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := newProvider(nil, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	result, err := p.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed empty failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for empty input, got %v", result)
	}
}

func TestEmbedInvokeError(t *testing.T) {
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized to perform: bedrock:InvokeModel")
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for InvokeModel failure")
	}
	if !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("expected AccessDeniedException, got: %v", err)
	}
}

func TestEmbedEmptyEmbedding(t *testing.T) {
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			respBody, _ := json.Marshal(titanResponse{
				Embedding: []float64{},
			})
			return &bedrockruntime.InvokeModelOutput{Body: respBody}, nil
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for empty embedding")
	}
	if !strings.Contains(err.Error(), "empty embedding") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedInvalidJSON(t *testing.T) {
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			return &bedrockruntime.InvokeModelOutput{Body: []byte("not json")}, nil
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbedBatchPartialFailure(t *testing.T) {
	callCount := 0
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			callCount++
			if callCount == 2 {
				return nil, fmt.Errorf("throttling exception")
			}
			respBody, _ := json.Marshal(titanResponse{
				Embedding: make([]float64, 1024),
			})
			return &bedrockruntime.InvokeModelOutput{Body: respBody}, nil
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	_, err := p.Embed(context.Background(), []string{"text1", "text2", "text3"})
	if err == nil {
		t.Fatal("expected error for partial batch failure")
	}
	if !strings.Contains(err.Error(), "input 1") {
		t.Errorf("expected error to reference input index 1, got: %v", err)
	}
}

func TestValidate(t *testing.T) {
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			respBody, _ := json.Marshal(titanResponse{
				Embedding: make([]float64, 1024),
			})
			return &bedrockruntime.InvokeModelOutput{Body: respBody}, nil
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateError(t *testing.T) {
	client := &mockClient{
		invokeFunc: func(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
			return nil, fmt.Errorf("invalid credentials")
		},
	}

	p := newProvider(client, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	err := p.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestModelName(t *testing.T) {
	p := newProvider(nil, "us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	if p.ModelName() != "amazon.titan-embed-text-v2:0" {
		t.Errorf("expected amazon.titan-embed-text-v2:0, got %s", p.ModelName())
	}
}

func TestDimensions(t *testing.T) {
	tests := []struct {
		model string
		dims  int
	}{
		{"amazon.titan-embed-text-v2:0", 1024},
		{"amazon.titan-embed-text-v1:2", 1536},
	}
	for _, tt := range tests {
		p := newProvider(nil, "us-east-1", tt.model, tt.dims)
		if p.Dimensions() != tt.dims {
			t.Errorf("model %s: expected %d dims, got %d", tt.model, tt.dims, p.Dimensions())
		}
	}
}

// Verify provider implements the interface at compile time.
var _ goembedding.Provider = (*provider)(nil)
