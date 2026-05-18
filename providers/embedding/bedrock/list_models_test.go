package bedrock

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	bedrockcp "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

// fakeControlClient implements bedrockControlClient with a canned
// foundation-models response. Lets ListModels tests pin both (a) the
// constructor sees the provider's stashed awsCfg (proving the
// access_keys path round-trips end to end — regression for PR #222's
// gap) and (b) the response parser correctly enriches with the
// hardcoded modelDimensions table.
type fakeControlClient struct {
	fmOut *bedrockcp.ListFoundationModelsOutput
	fmErr error
}

func (f *fakeControlClient) ListFoundationModels(_ context.Context, _ *bedrockcp.ListFoundationModelsInput, _ ...func(*bedrockcp.Options)) (*bedrockcp.ListFoundationModelsOutput, error) {
	return f.fmOut, f.fmErr
}

func withFakeControlClient(t *testing.T, fake *fakeControlClient) (*aws.Config, func()) {
	t.Helper()
	prev := newControlClient
	var seen aws.Config
	newControlClient = func(cfg aws.Config) bedrockControlClient {
		seen = cfg
		return fake
	}
	return &seen, func() { newControlClient = prev }
}

func strPtr(s string) *string { return &s }

// TestListModels_UsesProviderAwsCfg pins the regression: ListModels
// must build its control-plane client from the provider's stashed
// awsCfg (the one the factory resolved from auth_method) — never from
// a fresh LoadDefaultConfig that would ignore dashboard-supplied
// access keys.
func TestListModels_UsesProviderAwsCfg(t *testing.T) {
	wantCfg := aws.Config{
		Region:      "us-west-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIA-fixture", "secret-fixture", ""),
	}
	fake := &fakeControlClient{fmOut: &bedrockcp.ListFoundationModelsOutput{}}
	seenCfg, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &provider{awsCfg: wantCfg, region: "us-west-2"}
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if seenCfg.Region != "us-west-2" {
		t.Errorf("control client constructed with region = %q, want us-west-2", seenCfg.Region)
	}
	if seenCfg.Credentials == nil {
		t.Fatal("control client constructed with nil Credentials provider — factory awsCfg was not threaded through")
	}
	got, err := seenCfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve from control-client cfg: %v", err)
	}
	if got.AccessKeyID != "AKIA-fixture" || got.SecretAccessKey != "secret-fixture" {
		t.Errorf("control client got credentials = %+v, want AKIA-fixture/secret-fixture (provider awsCfg lost)", got)
	}
}

// TestListModels_EmbeddingsAreEnrichedWithDimensions exercises the
// response-parsing loop: every non-empty embedding model surfaces as
// a RemoteModel row, and IDs in modelDimensions get their dimension
// attached. Models NOT in modelDimensions come back with Dimensions=0
// (the dashboard renders "dimensions unknown") rather than being
// dropped — operators can see new embedding models the moment AWS
// ships them, even if our catalog hasn't been updated yet.
func TestListModels_EmbeddingsAreEnrichedWithDimensions(t *testing.T) {
	fake := &fakeControlClient{
		fmOut: &bedrockcp.ListFoundationModelsOutput{
			ModelSummaries: []bedrocktypes.FoundationModelSummary{
				{
					ModelId:        strPtr("amazon.titan-embed-text-v2:0"),
					ModelName:      strPtr("Titan Text Embeddings V2"),
					ModelLifecycle: &bedrocktypes.FoundationModelLifecycle{Status: bedrocktypes.FoundationModelLifecycleStatusActive},
				},
				{
					ModelId: strPtr("cohere.embed-english-v3"), // not in modelDimensions
				},
				{ModelId: strPtr("")}, // empty ID must be dropped
				{ModelId: nil},        // nil ID must be dropped
			},
		},
	}
	_, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &provider{awsCfg: aws.Config{}, region: "us-east-1"}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2 (empty/nil IDs dropped): %+v", len(got), got)
	}

	byID := make(map[string]int)
	for i, m := range got {
		byID[m.ID] = i
	}
	if i, ok := byID["amazon.titan-embed-text-v2:0"]; !ok {
		t.Error("missing amazon.titan-embed-text-v2:0")
	} else {
		if got[i].Dimensions != 1024 {
			t.Errorf("Titan v2 dims = %d, want 1024 (modelDimensions enrichment lost)", got[i].Dimensions)
		}
		if got[i].DisplayName != "Titan Text Embeddings V2" {
			t.Errorf("display name = %q", got[i].DisplayName)
		}
		if got[i].Lifecycle != string(bedrocktypes.FoundationModelLifecycleStatusActive) {
			t.Errorf("lifecycle = %q", got[i].Lifecycle)
		}
	}
	if i, ok := byID["cohere.embed-english-v3"]; !ok {
		t.Error("missing cohere.embed-english-v3 (uncatalogued models must still surface)")
	} else if got[i].Dimensions != 0 {
		t.Errorf("uncatalogued model dims = %d, want 0", got[i].Dimensions)
	}
}

// TestNewControlClient_RealConstructor pins that the default
// newControlClient builds a real bedrockcp.Client and conforms to the
// interface ListModels consumes. Other tests override newControlClient
// to avoid hitting the SDK, so this is the one place the production
// lambda body is actually executed — guards against drift if anyone
// edits the constructor in a way that fails to satisfy the interface.
func TestNewControlClient_RealConstructor(t *testing.T) {
	// c is already typed as bedrockControlClient (newControlClient's
	// return type); the nil check below is the runtime guarantee. The
	// compile-time guarantee lives at the package var declaration —
	// newControlClient's type signature won't compile if
	// bedrockcp.NewFromConfig stops satisfying bedrockControlClient.
	c := newControlClient(aws.Config{})
	if c == nil {
		t.Fatal("newControlClient returned nil")
	}
}

// TestListModels_FoundationModelsErrorPropagates — embedding's
// ListModels has no fallback (there's no inference-profile equivalent
// for embeddings), so any control-plane error must surface.
func TestListModels_FoundationModelsErrorPropagates(t *testing.T) {
	fake := &fakeControlClient{fmErr: errors.New("AccessDeniedException: bedrock:ListFoundationModels")}
	_, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &provider{awsCfg: aws.Config{}, region: "us-east-1"}
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}
