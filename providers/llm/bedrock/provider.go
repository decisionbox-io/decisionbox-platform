// Package bedrock provides an llm.Provider for AWS Bedrock.
// Bedrock hosts Claude, Llama, Mistral, Qwen, DeepSeek, and other models
// behind a single IAM-authenticated endpoint.
//
// Dispatch is catalog-driven: each model in the provider's catalog
// declares its wire (Anthropic Messages vs. OpenAI /chat/completions),
// and the dispatch switch routes the request accordingly. Models not
// in the catalog can be routed via the optional wire_override config
// key (project.llm.config.wire_override); newly-released models in a
// known family fall through to the prefix-based FamilyInferrer.
//
// Configuration:
//
//	LLM_PROVIDER=bedrock
//	LLM_MODEL=us.anthropic.claude-opus-4-7-v1:0  (or any catalog alias)
//	region in project LLM config (default: us-east-1)
//	wire_override=anthropic|openai-compat  (optional)
//
// Authentication: AWS credentials resolved by libs/awscreds. Supports
// IAM Role (ambient SDK chain), Access Keys (project blob in
// "credentials_json"), and Assume Role (project "role_arn" + optional
// "external_id" against ambient base creds).
package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/decisionbox-io/decisionbox/libs/awscreds"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// providerName is the registry key. Kept as a constant so dispatch
// errors and meta lookups all read from one place.
const providerName = "bedrock"

// bedrockDefaultTimeout is the historical default HTTP timeout for the
// Bedrock InvokeModel call. Five minutes covers Claude Opus on
// reasonably-sized prompts; longer generations should opt in via the
// LLM_TIMEOUT env var or per-project timeout_seconds.
const bedrockDefaultTimeout = 300 * time.Second

func init() {
	gollm.RegisterWithMeta(providerName, factory, gollm.ProviderMeta{
		Name:        "AWS Bedrock",
		Description: "AWS-managed AI platform — Claude, Qwen, DeepSeek, Mistral, Llama with IAM auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "region", Label: "AWS Region", Type: "string", Default: "us-east-1"},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "us.anthropic.claude-sonnet-4-6-v1:0",
				Placeholder: "us.anthropic.claude-sonnet-4-6-v1:0",
				Description: "Pick a catalogued model or type any Bedrock model ID. Cross-region inference profiles (us./eu./apac./global.) are recognised automatically.",
			},
			{
				Key:   "wire_override",
				Label: "Wire override",
				Type:  "string",
				Description: "Leave on auto unless your model is not yet in the catalog. " +
					"Bedrock supports Anthropic (Claude) and OpenAI Chat Completions (Qwen/DeepSeek/Mistral/Llama).",
				Options: []gollm.ConfigOption{
					{Value: "", Label: "Auto — use model catalog"},
					{Value: string(gollm.WireAnthropic), Label: "Anthropic Messages (Claude)"},
					{Value: string(gollm.WireOpenAICompat), Label: "OpenAI Chat Completions"},
				},
			},
		},
		AuthMethods: []gollm.AuthMethod{
			{
				ID: awscreds.MethodIAMRole, Name: "IAM Role",
				Description: "Automatic — EC2 instance profile, EKS pod role, environment variables. No credentials needed.",
			},
			{
				ID: awscreds.MethodAccessKeys, Name: "Access Keys",
				Description: "AWS access key pair for cross-cloud or local access.",
				Fields: []gollm.ConfigField{
					{Key: awscreds.FieldCredentials, Label: "Access Key ID : Secret Access Key", Required: true, Type: "credential", Placeholder: "AKIA...:wJalr..."}, //nolint:gosec // example placeholder
				},
			},
			{
				ID: awscreds.MethodAssumeRole, Name: "Assume Role",
				Description: "Assume an IAM role via STS. For cross-account access.",
				Fields: []gollm.ConfigField{
					{Key: awscreds.FieldRoleARN, Label: "Role ARN", Required: true, Type: "string", Placeholder: "arn:aws:iam::123456789012:role/BedrockRole"},
					{Key: awscreds.FieldExternalID, Label: "External ID", Type: "string", Description: "Required if the role trust policy requires an external ID."},
				},
			},
		},
		Models:                 buildBedrockCatalog(),
		DefaultMaxOutputTokens: 16384,
		FamilyInferrer:         inferBedrockWire,
		// Bedrock supports tool_use natively on the Anthropic wire.
		// OpenAI-compat Bedrock models inherit tool support from the
		// openaicompat helper; whether the upstream *model* implements
		// function calling reliably varies, but the catalog flag is
		// per-provider not per-model.
		SupportsTools: true,
	})
}

// factory is split out from init() so provider_test can call it
// directly with a synthetic config without going through
// gollm.NewProvider.
func factory(cfg gollm.ProviderConfig) (gollm.Provider, error) {
	region := cfg["region"]
	if region == "" {
		region = "us-east-1"
	}
	model := cfg["model"]
	if model == "" {
		return nil, fmt.Errorf("bedrock: model is required")
	}

	wireOverride := gollm.WireUnknown
	if raw := cfg["wire_override"]; raw != "" {
		parsed := gollm.ParseWire(raw)
		// Bedrock dispatches on Anthropic and OpenAICompat only.
		// google-native parses as a valid Wire but there's no
		// implementation on Bedrock, so reject it here rather than
		// failing at first Chat.
		if parsed != gollm.WireAnthropic && parsed != gollm.WireOpenAICompat {
			return nil, fmt.Errorf(
				"bedrock: invalid wire_override %q; use one of: %s, %s",
				raw, gollm.WireAnthropic, gollm.WireOpenAICompat,
			)
		}
		wireOverride = parsed
	}

	timeout := gollm.ResolveHTTPTimeout(cfg, bedrockDefaultTimeout)

	awsCfg, err := awscreds.Load(context.Background(), awscreds.Config{
		Method:      cfg["auth_method"],
		Region:      region,
		Credentials: cfg[awscreds.FieldCredentials],
		RoleARN:     cfg[awscreds.FieldRoleARN],
		ExternalID:  cfg[awscreds.FieldExternalID],
		SessionName: "decisionbox-llm",
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock: %w", err)
	}

	client := bedrockruntime.NewFromConfig(awsCfg)

	return &BedrockProvider{
		client:       client,
		region:       region,
		model:        model,
		wireOverride: wireOverride,
		httpClient:   &http.Client{Timeout: timeout},
	}, nil
}

// BedrockProvider implements llm.Provider for AWS Bedrock. Routes per
// wire resolved from the registered ProviderMeta catalog.
type BedrockProvider struct {
	client       bedrockClient
	region       string
	model        string
	wireOverride gollm.Wire
	httpClient   *http.Client
}

// Validate checks that AWS credentials are valid and the configured model is
// reachable. Makes a minimal request (max_tokens=1) so it exercises the
// same dispatch path as a real call.
func (p *BedrockProvider) Validate(ctx context.Context) error {
	_, err := p.Chat(ctx, gollm.ChatRequest{
		Model:     p.model,
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err != nil {
		return fmt.Errorf("bedrock: validation failed: %w", err)
	}
	return nil
}

// Chat sends a conversation to AWS Bedrock, dispatching on the wire format
// resolved from the catalog (or the configured wire_override).
func (p *BedrockProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	return p.dispatch(ctx, req)
}

