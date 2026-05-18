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

// fakeControlClient implements bedrockControlClient with canned
// responses + the awsCfg the constructor was called with. Lets
// ListModels tests pin both (a) the constructor sees the provider's
// stashed awsCfg (proving the access_keys path round-trips end to end
// — regression for PR #222's gap) and (b) the response parser
// correctly merges foundation models and inference profiles.
type fakeControlClient struct {
	fmOut *bedrockcp.ListFoundationModelsOutput
	fmErr error
	ipOut *bedrockcp.ListInferenceProfilesOutput
	ipErr error
}

func (f *fakeControlClient) ListFoundationModels(_ context.Context, _ *bedrockcp.ListFoundationModelsInput, _ ...func(*bedrockcp.Options)) (*bedrockcp.ListFoundationModelsOutput, error) {
	return f.fmOut, f.fmErr
}

func (f *fakeControlClient) ListInferenceProfiles(_ context.Context, _ *bedrockcp.ListInferenceProfilesInput, _ ...func(*bedrockcp.Options)) (*bedrockcp.ListInferenceProfilesOutput, error) {
	return f.ipOut, f.ipErr
}

// withFakeControlClient swaps the package-level constructor for the
// duration of the test, recording the aws.Config it was handed so the
// test can assert ListModels routed the provider's awsCfg through
// instead of building a fresh one via LoadDefaultConfig.
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

// strPtr returns &s for AWS SDK input fields that take *string.
func strPtr(s string) *string { return &s }

// TestListModels_UsesProviderAwsCfg pins the regression: ListModels
// must build its control-plane client from the provider's stashed
// awsCfg (the one the factory resolved from the project's auth_method)
// — never from a fresh LoadDefaultConfig that would ignore dashboard-
// supplied access keys.
func TestListModels_UsesProviderAwsCfg(t *testing.T) {
	wantCfg := aws.Config{
		Region:      "us-west-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIA-fixture", "secret-fixture", ""),
	}
	fake := &fakeControlClient{
		fmOut: &bedrockcp.ListFoundationModelsOutput{},
		ipOut: &bedrockcp.ListInferenceProfilesOutput{},
	}
	seenCfg, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &BedrockProvider{awsCfg: wantCfg, region: "us-west-2"}
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

// TestListModels_MergesFoundationModelsAndInferenceProfiles exercises
// the response-parsing loop: every non-empty foundation model and
// every inference profile must surface as a RemoteModel row, with
// display name + lifecycle propagated when present.
func TestListModels_MergesFoundationModelsAndInferenceProfiles(t *testing.T) {
	fake := &fakeControlClient{
		fmOut: &bedrockcp.ListFoundationModelsOutput{
			ModelSummaries: []bedrocktypes.FoundationModelSummary{
				{
					ModelId:        strPtr("anthropic.claude-sonnet-4-6-v1:0"),
					ModelName:      strPtr("Claude Sonnet 4.6"),
					ModelLifecycle: &bedrocktypes.FoundationModelLifecycle{Status: bedrocktypes.FoundationModelLifecycleStatusActive},
				},
				{
					ModelId:   strPtr("amazon.nova-pro-v1:0"),
					ModelName: strPtr("Amazon Nova Pro"),
				},
				{ModelId: strPtr("")}, // empty ID — must be dropped
				{ModelId: nil},        // nil ID — must be dropped
			},
		},
		ipOut: &bedrockcp.ListInferenceProfilesOutput{
			InferenceProfileSummaries: []bedrocktypes.InferenceProfileSummary{
				{
					InferenceProfileId:   strPtr("us.anthropic.claude-sonnet-4-6-v1:0"),
					InferenceProfileName: strPtr("US Anthropic Claude Sonnet 4.6"),
					Status:               bedrocktypes.InferenceProfileStatusActive,
				},
			},
		},
	}
	_, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &BedrockProvider{awsCfg: aws.Config{}, region: "us-east-1"}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// 2 foundation models + 1 inference profile (empty/nil IDs dropped).
	if len(got) != 3 {
		t.Fatalf("got %d models, want 3: %+v", len(got), got)
	}

	byID := make(map[string]int)
	for i, m := range got {
		byID[m.ID] = i
	}
	if i, ok := byID["anthropic.claude-sonnet-4-6-v1:0"]; !ok {
		t.Error("missing anthropic.claude-sonnet-4-6-v1:0")
	} else {
		if got[i].DisplayName != "Claude Sonnet 4.6" {
			t.Errorf("display name = %q", got[i].DisplayName)
		}
		if got[i].Lifecycle != string(bedrocktypes.FoundationModelLifecycleStatusActive) {
			t.Errorf("lifecycle = %q", got[i].Lifecycle)
		}
	}
	if _, ok := byID["amazon.nova-pro-v1:0"]; !ok {
		t.Error("missing amazon.nova-pro-v1:0")
	}
	if i, ok := byID["us.anthropic.claude-sonnet-4-6-v1:0"]; !ok {
		t.Error("missing us.anthropic.claude-sonnet-4-6-v1:0 (inference profile)")
	} else if got[i].Lifecycle != string(bedrocktypes.InferenceProfileStatusActive) {
		t.Errorf("inference profile lifecycle = %q", got[i].Lifecycle)
	}
}

// TestListModels_FoundationModelsErrorPropagates — the FoundationModels
// call is the primary signal; any error there fails the whole call so
// the dashboard surfaces an actionable upstream error instead of an
// empty list.
func TestListModels_FoundationModelsErrorPropagates(t *testing.T) {
	fake := &fakeControlClient{fmErr: errors.New("AccessDeniedException: bedrock:ListFoundationModels")}
	_, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &BedrockProvider{awsCfg: aws.Config{}, region: "us-east-1"}
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error to propagate")
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

// TestListModels_InferenceProfilesErrorIsNonFatal documents the
// fallback: some regions / IAM identities can list foundation models
// but not inference profiles. The list_models loop swallows the IP
// error and returns whatever foundation models it has, instead of
// failing the whole call and leaving the user with no live picker.
func TestListModels_InferenceProfilesErrorIsNonFatal(t *testing.T) {
	fake := &fakeControlClient{
		fmOut: &bedrockcp.ListFoundationModelsOutput{
			ModelSummaries: []bedrocktypes.FoundationModelSummary{
				{ModelId: strPtr("anthropic.claude-sonnet-4-6-v1:0")},
			},
		},
		ipErr: errors.New("AccessDeniedException: bedrock:ListInferenceProfiles"),
	}
	_, restore := withFakeControlClient(t, fake)
	defer restore()

	p := &BedrockProvider{awsCfg: aws.Config{}, region: "us-east-1"}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("inference-profile error should be non-fatal, got: %v", err)
	}
	if len(got) != 1 || got[0].ID != "anthropic.claude-sonnet-4-6-v1:0" {
		t.Errorf("got %+v, want one foundation model row", got)
	}
}
