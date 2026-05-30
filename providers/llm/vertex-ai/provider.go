// Package vertexai provides an llm.Provider for Google Vertex AI.
// Vertex hosts three families of models, each speaking a different wire:
//
//   - Gemini via generateContent (WireGoogleNative)
//   - Claude via rawPredict publishers/anthropic/… (WireAnthropic)
//   - Llama / Qwen / DeepSeek / Mistral on MaaS via
//     /v1beta1/.../endpoints/openapi/chat/completions (WireOpenAICompat)
//
// Dispatch is catalog-driven: each model in the registered ProviderMeta
// catalog declares its wire, and the dispatch switch routes per-wire.
// Uncatalogued models can be routed via wire_override; freshly-released
// models in known families fall through to the prefix-based
// FamilyInferrer.
//
// Configuration:
//
//	LLM_PROVIDER=vertex-ai
//	LLM_MODEL=gemini-2.5-pro  (or claude-opus-4-7, or meta/llama-3.3-70b-instruct-maas)
//	VERTEX_PROJECT_ID=my-gcp-project
//	VERTEX_LOCATION=us-east5  (us-east5 for Claude, us-central1 for Gemini)
//	wire_override=google-native|anthropic|openai-compat  (optional)
//	endpoint_id=1234567890123456789  (optional — a user-deployed Vertex
//	  endpoint that serves the OpenAI chat-completions wire; blank uses
//	  the shared Model Garden MaaS endpoint)
//
// Authentication: GCP credentials resolved by libs/gcpcreds. Supports
// Application Default Credentials (ADC — on GKE Workload Identity,
// locally gcloud auth application-default login) and Service Account
// Key (project JSON in "credentials_json").
package vertexai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/gcpcreds"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// providerName is the registry key.
const providerName = "vertex-ai"

// vertexDefaultTimeout is the historical default HTTP timeout. Long
// Gemini / Claude generations on Vertex easily blow past the net/http
// 60s default, so 5 minutes is the floor; operators raise it via
// LLM_TIMEOUT or per-project timeout_seconds.
const vertexDefaultTimeout = 300 * time.Second

// newAuth is the constructor seam tests stub to bypass ADC. Production
// builds use newGCPAuth; tests swap in a fake that returns a fully
// formed *gcpAuth without touching Application Default Credentials so
// the factory's downstream code path is exercised.
var newAuth = newGCPAuth

func init() {
	gollm.RegisterWithMeta(providerName, factory, gollm.ProviderMeta{
		Name:        "Google Vertex AI",
		Description: "GCP-managed AI platform — Gemini, Claude, Llama, Qwen, DeepSeek, Mistral with GCP auth",
		ConfigFields: []gollm.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "location", Label: "Region", Type: "string", Default: "us-east5", Description: "GCP region (us-east5 for Claude, us-central1 for Gemini)"},
			{
				Key:         "endpoint_id",
				Label:       "Endpoint ID",
				Type:        "string",
				Placeholder: "1234567890123456789",
				Description: "Vertex endpoint ID for a user-deployed model that serves the " +
					"OpenAI chat-completions wire (e.g. a self-fine-tuned or quantised model). " +
					"Leave blank to use the shared Model Garden MaaS endpoint.",
			},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "gemini-2.5-pro",
				Placeholder: "gemini-2.5-pro or claude-opus-4-7",
				Description: "Pick a catalogued model or type any Vertex model ID.",
			},
			{
				Key:   "wire_override",
				Label: "Wire override",
				Type:  "string",
				Description: "Leave on auto unless your model is not yet in the catalog. " +
					"Vertex AI supports Google-native (Gemini), Anthropic (Claude), and OpenAI Chat Completions (MaaS).",
				Options: []gollm.ConfigOption{
					{Value: "", Label: "Auto — use model catalog"},
					{Value: string(gollm.WireGoogleNative), Label: "Google Gemini (native)"},
					{Value: string(gollm.WireAnthropic), Label: "Anthropic Messages (Claude)"},
					{Value: string(gollm.WireOpenAICompat), Label: "OpenAI Chat Completions"},
				},
			},
		},
		AuthMethods: []gollm.AuthMethod{
			{
				ID: gcpcreds.MethodADC, Name: "Application Default Credentials",
				Description: "Automatic — GKE Workload Identity, gcloud auth, VM service account. No credentials needed.",
			},
			{
				ID: gcpcreds.MethodSAKey, Name: "Service Account Key",
				Description: "GCP service account JSON key. Also supports Workload Identity Federation credential configs.",
				Fields: []gollm.ConfigField{
					{Key: gcpcreds.FieldCredentials, Label: "Service Account JSON", Required: true, Type: "credential"},
				},
			},
		},
		Models:                 buildVertexCatalog(),
		DefaultMaxOutputTokens: 16384,
		FamilyInferrer:         inferVertexWire,
		EffectiveInputWindow:   vertexEffectiveInputWindow,
	})
}

// vertexEffectiveInputWindow is the ProviderMeta.EffectiveInputWindow
// hook. When endpoint_id is set the configured model lives on a
// user-deployed endpoint, not in the publisher catalog — so its catalog
// context window must not be used for budgeting (a deployed-model name
// that collides with a catalog ID like "gemini-2.5-pro" would otherwise
// budget against Gemini's window and overflow the real endpoint). Fall
// back to the conservative provider / global default in that case;
// otherwise preserve the catalog-driven value GetEffectiveInputWindow
// would have returned with no hook.
func vertexEffectiveInputWindow(model string, cfg gollm.ProviderConfig) int {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		return gollm.DefaultMaxInputTokens
	}
	if strings.TrimSpace(cfg["endpoint_id"]) != "" {
		if meta.DefaultMaxInputTokens > 0 {
			return meta.DefaultMaxInputTokens
		}
		return gollm.DefaultMaxInputTokens
	}
	return meta.MaxInputTokensFor(model)
}

func factory(cfg gollm.ProviderConfig) (gollm.Provider, error) {
	projectID := cfg["project_id"]
	if projectID == "" {
		return nil, fmt.Errorf("vertex-ai: project_id is required")
	}
	location := cfg["location"]
	if location == "" {
		location = "us-east5"
	}
	// model is optional at construction time: the dashboard's
	// "Load models" flow constructs the provider without a model
	// picked so it can call ListModels(). Chat() / Validate()
	// check for an empty model at call time and return a clear
	// "constructed without a model (list-only)" error then.
	// Matches the Ollama provider's list-only-construction pattern.
	model := cfg["model"]

	wireOverride := gollm.WireUnknown
	if raw := cfg["wire_override"]; raw != "" {
		parsed := gollm.ParseWire(raw)
		if !parsed.Valid() {
			return nil, fmt.Errorf(
				"vertex-ai: invalid wire_override %q; use one of: %s, %s, %s",
				raw, gollm.WireGoogleNative, gollm.WireAnthropic, gollm.WireOpenAICompat,
			)
		}
		wireOverride = parsed
	}

	// A user-deployed endpoint is reached at
	// .../endpoints/{endpoint_id}/chat/completions, which only serves the
	// OpenAI chat-completions wire. Routing it through any other wire is a
	// contradiction, so reject a conflicting wire_override up front rather
	// than silently ignoring it. Trim so a pasted ID with surrounding
	// whitespace doesn't land in the URL, and a whitespace-only value is
	// treated as "no endpoint" (matching the dashboard's trimmed check).
	endpointID := strings.TrimSpace(cfg["endpoint_id"])
	if endpointID != "" && wireOverride != gollm.WireUnknown && wireOverride != gollm.WireOpenAICompat {
		return nil, fmt.Errorf(
			"vertex-ai: endpoint_id routes through the OpenAI chat-completions wire, "+
				"but wire_override=%s was set; clear wire_override or set it to %s",
			wireOverride, gollm.WireOpenAICompat,
		)
	}

	timeout := gollm.ResolveHTTPTimeout(cfg, vertexDefaultTimeout)
	ctx := context.Background()

	auth, err := newAuth(ctx, gcpcreds.Config{
		Method:          cfg["auth_method"],
		CredentialsJSON: cfg[gcpcreds.FieldCredentials],
	})
	if err != nil {
		return nil, err
	}

	return &VertexAIProvider{
		projectID:    projectID,
		location:     location,
		model:        model,
		endpointID:   endpointID,
		wireOverride: wireOverride,
		auth:         auth,
		httpClient:   &http.Client{Timeout: timeout},
		// A user-account ADC token needs a quota project on
		// aiplatform.googleapis.com; a service-account key already bills
		// to its own project and adding the header would force a
		// quota-project that the SA may lack serviceusage.services.use on
		// (HTTP 403). So send X-Goog-User-Project for ADC, not sa_key.
		useQuotaProjectHeader: cfg["auth_method"] != gcpcreds.MethodSAKey,
	}, nil
}

// VertexAIProvider implements llm.Provider for Google Vertex AI.
type VertexAIProvider struct {
	projectID string
	location  string
	model     string
	// endpointID, when set, names a user-deployed Vertex endpoint. Chat
	// requests then target .../endpoints/{endpointID}/chat/completions
	// (the OpenAI chat-completions wire) instead of the shared Model
	// Garden MaaS endpoint. Empty means MaaS.
	endpointID   string
	wireOverride gollm.Wire
	auth         *gcpAuth
	httpClient   *http.Client
	// useQuotaProjectHeader controls whether X-Goog-User-Project is sent
	// on aiplatform requests. True for ADC (user-account tokens need a
	// quota project), false for service-account keys (they bill to their
	// own project and the header can trigger a serviceusage 403).
	useQuotaProjectHeader bool

	// endpointHost caches the prediction host resolved for endpointID
	// (its dedicated DNS, or the shared aiplatform host when not
	// dedicated). Resolved lazily on first chat and reused thereafter;
	// the mutex guards concurrent Chat calls racing the first lookup.
	endpointHostMu sync.Mutex
	endpointHost   string
}

// Validate exercises the same dispatch path as a real Chat call with
// max_tokens=1 so credentials and model availability are both checked.
func (p *VertexAIProvider) Validate(ctx context.Context) error {
	// A user-deployed endpoint identifies its own model, so a blank
	// model is expected there — only the MaaS path requires one.
	if p.model == "" && p.endpointID == "" {
		return fmt.Errorf("vertex-ai: provider was constructed without a model (list-only); call NewProvider again with cfg[\"model\"] set before validating")
	}
	_, err := p.Chat(ctx, gollm.ChatRequest{
		Model:     p.model,
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err != nil {
		return fmt.Errorf("vertex-ai: validation failed: %w", err)
	}
	return nil
}

// Chat sends a conversation to Vertex AI, dispatching per the wire
// resolved from the catalog (or the configured wire_override).
func (p *VertexAIProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	if req.Model == "" && p.endpointID == "" {
		// List-only construction (no model picked at factory time and
		// no per-request model either) reaches here when a caller
		// accidentally tries to chat through a provider built for
		// ListModels(). Surface a clear error instead of letting the
		// dispatcher fail later on an unresolvable wire. A user-deployed
		// endpoint is exempt: it serves its own model, so a blank model
		// is forwarded verbatim and the endpoint resolves it.
		return nil, fmt.Errorf("vertex-ai: chat requires a model — neither ChatRequest.Model nor provider model is set (list-only construction)")
	}
	return p.dispatch(ctx, req)
}
