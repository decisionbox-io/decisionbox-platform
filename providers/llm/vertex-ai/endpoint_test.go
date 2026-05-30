package vertexai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// capturedReq records the request as the provider built it, before the
// test transport rewrites the host to the test server. This is how the
// routing tests assert which host a request targeted (the rewrite would
// otherwise mask it).
type capturedReq struct {
	method string
	host   string
	path   string
}

// captureTransport records every outgoing request then rewrites it to a
// single test server. Unlike rewriteTransport in mock_test.go it keeps
// the original host/path so tests can assert dedicated-DNS routing.
type captureTransport struct {
	target string
	mu     sync.Mutex
	reqs   []capturedReq
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.reqs = append(t.reqs, capturedReq{req.Method, req.URL.Host, req.URL.Path})
	t.mu.Unlock()
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.target, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func (t *captureTransport) snapshot() []capturedReq {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]capturedReq, len(t.reqs))
	copy(out, t.reqs)
	return out
}

func providerWithCapture(serverURL, model, endpointID string) (*VertexAIProvider, *captureTransport) {
	ct := &captureTransport{target: serverURL}
	p := &VertexAIProvider{
		projectID:  "test-project",
		location:   "us-central1",
		model:      model,
		endpointID: endpointID,
		auth:       &gcpAuth{tokenSource: &mockTokenSource{token: "test-token-123"}},
		httpClient: &http.Client{Timeout: 10 * time.Second, Transport: ct},
		// Simulate ADC (the default), where the quota-project header is sent.
		useQuotaProjectHeader: true,
	}
	return p, ct
}

// endpointLookupJSON is the subset of the Vertex endpoint resource the
// provider parses to resolve the prediction host.
func endpointLookupJSON(dedicated bool, dns string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"dedicatedEndpointEnabled": dedicated,
		"dedicatedEndpointDns":     dns,
	})
	return string(b)
}

// --- Host normalization ---

func TestNormalizeEndpointHost(t *testing.T) {
	const bare = "mg-endpoint-abc.us-central1-1234.prediction.vertexai.goog"
	tests := []struct {
		in   string
		want string
	}{
		{bare, bare},
		{"https://" + bare, bare},
		{"http://" + bare, bare},
		{"https://" + bare + "/", bare},
		{"  https://" + bare + "  ", bare},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeEndpointHost(tc.in); got != tc.want {
			t.Errorf("normalizeEndpointHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- Pure URL builders (no network) ---

func TestVertexAIProvider_ChatURLBuilders(t *testing.T) {
	t.Run("aiplatform_host", func(t *testing.T) {
		if got := (&VertexAIProvider{location: "us-central1"}).aiplatformHost(); got != "us-central1-aiplatform.googleapis.com" {
			t.Errorf("regional host = %q", got)
		}
		if got := (&VertexAIProvider{location: "global"}).aiplatformHost(); got != "aiplatform.googleapis.com" {
			t.Errorf("global host = %q", got)
		}
	})

	t.Run("maas_url", func(t *testing.T) {
		p := &VertexAIProvider{projectID: "proj", location: "us-central1"}
		want := "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/proj/locations/us-central1/endpoints/openapi/chat/completions"
		if got := p.maasChatURL(); got != want {
			t.Errorf("maasChatURL() = %q, want %q", got, want)
		}
		// The MaaS path always carries the /openapi/ segment.
		if !strings.Contains(p.maasChatURL(), "/endpoints/openapi/") {
			t.Errorf("MaaS URL %q missing /openapi/ segment", p.maasChatURL())
		}
	})

	t.Run("endpoint_url_on_given_host", func(t *testing.T) {
		p := &VertexAIProvider{projectID: "proj", location: "us-central1", endpointID: "mg-endpoint-abc"}
		// Shared aiplatform host.
		wantShared := "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/proj/locations/us-central1/endpoints/mg-endpoint-abc/chat/completions"
		if got := p.endpointChatURL("us-central1-aiplatform.googleapis.com"); got != wantShared {
			t.Errorf("endpointChatURL(shared) = %q, want %q", got, wantShared)
		}
		// Dedicated DNS host.
		wantDedicated := "https://mg-endpoint-abc.us-central1-1234.prediction.vertexai.goog/v1beta1/projects/proj/locations/us-central1/endpoints/mg-endpoint-abc/chat/completions"
		if got := p.endpointChatURL("mg-endpoint-abc.us-central1-1234.prediction.vertexai.goog"); got != wantDedicated {
			t.Errorf("endpointChatURL(dedicated) = %q, want %q", got, wantDedicated)
		}
		// The user-deployed path must never carry the /openapi/ segment.
		if strings.Contains(p.endpointChatURL("h"), "/endpoints/openapi/") {
			t.Errorf("endpoint URL must not contain /openapi/")
		}
	})
}

// TestVertexAIProvider_OpenAICompatURL_MaaSNoLookup pins that the MaaS
// path (endpoint_id blank) returns the URL without any HTTP call —
// resolving a host is only needed for user-deployed endpoints.
func TestVertexAIProvider_OpenAICompatURL_MaaSNoLookup(t *testing.T) {
	p, ct := providerWithCapture("http://127.0.0.1:1", "meta/llama-3.3-70b-instruct-maas", "")
	got, err := p.openAICompatURL(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "/endpoints/openapi/chat/completions") {
		t.Errorf("MaaS URL = %q", got)
	}
	if reqs := ct.snapshot(); len(reqs) != 0 {
		t.Errorf("MaaS path made %d HTTP calls, want 0", len(reqs))
	}
}

// --- Host resolution ---

func TestVertexAIProvider_ResolveEndpointHost(t *testing.T) {
	const endpointID = "mg-endpoint-306f661d"
	const dedicatedDNS = "mg-endpoint-306f661d.us-central1-114917953805.prediction.vertexai.goog"

	tests := []struct {
		name     string
		status   int
		body     string
		wantHost string
		wantErr  string
	}{
		{
			name:     "dedicated_with_dns",
			status:   http.StatusOK,
			body:     endpointLookupJSON(true, dedicatedDNS),
			wantHost: dedicatedDNS,
		},
		{
			// The aiplatform API documents dedicatedEndpointDns in URL
			// form (https://…); the host must come out scheme-stripped.
			name:     "dedicated_dns_with_scheme_is_normalized",
			status:   http.StatusOK,
			body:     endpointLookupJSON(true, "https://"+dedicatedDNS+"/"),
			wantHost: dedicatedDNS,
		},
		{
			name:     "not_dedicated_uses_shared_host",
			status:   http.StatusOK,
			body:     endpointLookupJSON(false, ""),
			wantHost: "us-central1-aiplatform.googleapis.com",
		},
		{
			name:    "dedicated_without_dns_is_actionable_error",
			status:  http.StatusOK,
			body:    endpointLookupJSON(true, ""),
			wantErr: "no DNS yet",
		},
		{
			name:    "lookup_404",
			status:  http.StatusNotFound,
			body:    `{"error":{"code":404,"message":"endpoint not found"}}`,
			wantErr: "endpoint lookup",
		},
		{
			name:    "malformed_json",
			status:  http.StatusOK,
			body:    `not json`,
			wantErr: "parse endpoint lookup",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("endpoint lookup method = %s, want GET", r.Method)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			p, _ := providerWithCapture(server.URL, "m", endpointID)
			host, err := p.resolveEndpointHost(context.Background())

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got host %q", tc.wantErr, host)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q missing %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
		})
	}
}

// TestVertexAIProvider_ResolveEndpointHost_CachesResult pins that the
// endpoint lookup happens once and the resolved host is reused — a chat
// loop must not re-fetch the endpoint resource on every turn.
func TestVertexAIProvider_ResolveEndpointHost_CachesResult(t *testing.T) {
	const dedicatedDNS = "mg-endpoint-x.us-central1-1.prediction.vertexai.goog"
	var calls int32
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(endpointLookupJSON(true, dedicatedDNS)))
	}))
	defer server.Close()

	p, _ := providerWithCapture(server.URL, "m", "mg-endpoint-x")
	for i := 0; i < 3; i++ {
		host, err := p.resolveEndpointHost(context.Background())
		if err != nil || host != dedicatedDNS {
			t.Fatalf("call %d: host=%q err=%v", i, host, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("endpoint lookup ran %d times, want 1 (result must be cached)", calls)
	}
}

// --- End-to-end chat routing ---

// TestVertexAI_OpenAICompatChat_RoutesToDedicatedDNS asserts that a chat
// against a user-deployed endpoint first looks the endpoint up, then
// sends the completion to the dedicated DNS host (not the shared
// aiplatform host) at .../endpoints/{id}/chat/completions, forwarding an
// arbitrary un-prefixed model name verbatim.
func TestVertexAI_OpenAICompatChat_RoutesToDedicatedDNS(t *testing.T) {
	const customModel = "my-finetuned-qwen-27b"
	const endpointID = "mg-endpoint-306f661d"
	const dedicatedDNS = "mg-endpoint-306f661d.us-central1-114917953805.prediction.vertexai.goog"

	var capturedModel, capturedQuotaProject string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet: // endpoint lookup
			_, _ = w.Write([]byte(endpointLookupJSON(true, dedicatedDNS)))
		case http.MethodPost: // chat completion
			capturedQuotaProject = r.Header.Get("X-Goog-User-Project")
			body, _ := io.ReadAll(r.Body)
			var decoded struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &decoded)
			capturedModel = decoded.Model
			_, _ = w.Write([]byte(`{"model":"Qwen/Qwen3.6-27B",
				"choices":[{"index":0,"message":{"role":"assistant","content":"custom says hi"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
		}
	}))
	defer server.Close()

	p, ct := providerWithCapture(server.URL, customModel, endpointID)
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    customModel,
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "custom says hi" {
		t.Errorf("content = %q", resp.Content)
	}
	if capturedModel != customModel {
		t.Errorf("request model = %q, want verbatim %q", capturedModel, customModel)
	}
	// The chat request must carry the quota-project header so user-ADC
	// callers without a configured quota project are not rejected.
	if capturedQuotaProject != "test-project" {
		t.Errorf("chat X-Goog-User-Project = %q, want %q", capturedQuotaProject, "test-project")
	}

	reqs := ct.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (lookup + chat), got %d: %+v", len(reqs), reqs)
	}
	// First: management lookup on the shared aiplatform host.
	if reqs[0].method != http.MethodGet || reqs[0].host != "us-central1-aiplatform.googleapis.com" {
		t.Errorf("lookup request = %+v, want GET on shared aiplatform host", reqs[0])
	}
	if reqs[0].path != "/v1beta1/projects/test-project/locations/us-central1/endpoints/"+endpointID {
		t.Errorf("lookup path = %q", reqs[0].path)
	}
	// Second: chat on the dedicated DNS host, no /openapi/ segment.
	if reqs[1].method != http.MethodPost || reqs[1].host != dedicatedDNS {
		t.Errorf("chat request = %+v, want POST on dedicated DNS %q", reqs[1], dedicatedDNS)
	}
	wantPath := "/v1beta1/projects/test-project/locations/us-central1/endpoints/" + endpointID + "/chat/completions"
	if reqs[1].path != wantPath {
		t.Errorf("chat path = %q, want %q", reqs[1].path, wantPath)
	}
	if strings.Contains(reqs[1].path, "/openapi/") {
		t.Errorf("chat path %q must not contain the /openapi/ MaaS segment", reqs[1].path)
	}
}

// TestVertexAI_OpenAICompatChat_EmptyModelWithEndpoint asserts that a
// provider configured with an endpoint but no model chats successfully
// (the dashboard hides the Model field for endpoints) and forwards an
// empty model verbatim — deployed endpoints identify their own model.
func TestVertexAI_OpenAICompatChat_EmptyModelWithEndpoint(t *testing.T) {
	const endpointID = "mg-endpoint-nomodel"
	const dedicatedDNS = "mg-endpoint-nomodel.us-central1-1.prediction.vertexai.goog"

	var capturedModel string
	var modelKeyPresent bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(endpointLookupJSON(true, dedicatedDNS)))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(body, &raw)
		_, modelKeyPresent = raw["model"]
		var decoded struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &decoded)
		capturedModel = decoded.Model
		_, _ = w.Write([]byte(`{"model":"served-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	p, _ := providerWithCapture(server.URL, "", endpointID) // no model configured
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("chat with endpoint and no model should succeed, got: %v", err)
	}
	if !modelKeyPresent {
		t.Error("request body should still carry a model field (empty), per the OpenAI wire")
	}
	if capturedModel != "" {
		t.Errorf("model = %q, want empty (endpoint identifies its own model)", capturedModel)
	}

	// Validate must also succeed with an endpoint and no model.
	if err := p.Validate(context.Background()); err != nil {
		t.Errorf("Validate with endpoint and no model should succeed, got: %v", err)
	}
}

// TestVertexAI_OpenAICompatChat_NonDedicatedUsesSharedHost asserts a
// non-dedicated user endpoint serves predictions on the shared
// aiplatform host (the issue's original endpoint shape).
func TestVertexAI_OpenAICompatChat_NonDedicatedUsesSharedHost(t *testing.T) {
	const endpointID = "1234567890123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(endpointLookupJSON(false, "")))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	p, ct := providerWithCapture(server.URL, "m", endpointID)
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "m",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reqs := ct.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("expected lookup + chat requests, got %+v", reqs)
	}
	if reqs[1].host != "us-central1-aiplatform.googleapis.com" {
		t.Errorf("non-dedicated chat host = %q, want shared aiplatform host", reqs[1].host)
	}
}

// TestVertexAI_Dispatch_EndpointIDBeatsCatalog asserts the endpoint_id
// short-circuit wins over catalog wire resolution: a model that would
// otherwise dispatch to the Google-native wire (gemini-2.5-pro) is
// instead sent to the user-deployed OpenAI-compat endpoint.
func TestVertexAI_Dispatch_EndpointIDBeatsCatalog(t *testing.T) {
	const endpointID = "mg-endpoint-cat"
	const dedicatedDNS = "mg-endpoint-cat.us-central1-1.prediction.vertexai.goog"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(endpointLookupJSON(true, dedicatedDNS)))
			return
		}
		_, _ = w.Write([]byte(`{"model":"gemini-2.5-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	p, ct := providerWithCapture(server.URL, "gemini-2.5-pro", endpointID)
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-pro",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reqs := ct.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected lookup + chat, got %+v", reqs)
	}
	wantPath := "/v1beta1/projects/test-project/locations/us-central1/endpoints/" + endpointID + "/chat/completions"
	if reqs[1].path != wantPath || reqs[1].host != dedicatedDNS {
		t.Errorf("chat routed to %+v, want the user-deployed dedicated endpoint (endpoint_id must beat the catalog's google-native wire)", reqs[1])
	}
}

// --- Catalog bypass for endpoint-backed models ---

// TestVertexAIProvider_TokenCounter_EndpointBypassesCatalog pins that a
// provider with endpoint_id set never picks the exact Gemini counter,
// even when the deployed model's name collides with a Gemini catalog ID
// — the endpoint serves the OpenAI-compat wire, which has no exact
// counter.
func TestVertexAIProvider_TokenCounter_EndpointBypassesCatalog(t *testing.T) {
	p := &VertexAIProvider{
		projectID:  "test-project",
		location:   "us-central1",
		model:      "gemini-2.5-pro",
		endpointID: "mg-endpoint-306f661d",
		auth:       &gcpAuth{tokenSource: &mockTokenSource{token: "test-token"}},
		httpClient: &http.Client{Timeout: time.Second},
	}
	c, err := p.TokenCounter(context.Background(), "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for an endpoint-backed model", c)
	}
}

// TestVertexEffectiveInputWindow_EndpointFallsBackToDefault pins that
// endpoint_id makes the budget window resolve to the conservative
// default instead of a colliding catalog model's (large) window, while
// the no-endpoint case still returns the catalog value.
func TestVertexEffectiveInputWindow_EndpointFallsBackToDefault(t *testing.T) {
	catalogWindow := gollm.GetEffectiveInputWindow(providerName, "gemini-2.5-pro", nil)
	if catalogWindow <= gollm.DefaultMaxInputTokens {
		t.Fatalf("test premise broken: gemini-2.5-pro catalog window %d should exceed default %d",
			catalogWindow, gollm.DefaultMaxInputTokens)
	}

	withEndpoint := gollm.GetEffectiveInputWindow(providerName, "gemini-2.5-pro", gollm.ProviderConfig{
		"endpoint_id": "mg-endpoint-306f661d",
	})
	if withEndpoint != gollm.DefaultMaxInputTokens {
		t.Errorf("endpoint-backed window = %d, want conservative default %d (not the colliding catalog window %d)",
			withEndpoint, gollm.DefaultMaxInputTokens, catalogWindow)
	}

	// No endpoint_id → catalog value preserved (no regression).
	noEndpoint := gollm.GetEffectiveInputWindow(providerName, "gemini-2.5-pro", gollm.ProviderConfig{})
	if noEndpoint != catalogWindow {
		t.Errorf("non-endpoint window = %d, want catalog value %d", noEndpoint, catalogWindow)
	}
}

// --- Factory parsing / validation (no network) ---

// TestVertexAIProvider_Factory_EndpointID pins that the factory parses
// the endpoint_id cfg field cleanly for both the empty (MaaS) and set
// (user-deployed) cases. Auth is stubbed so the assertion does not
// depend on ADC being available; no network call happens at
// construction time.
func TestVertexAIProvider_Factory_EndpointID(t *testing.T) {
	tests := []struct {
		name string
		cfg  gollm.ProviderConfig
		want string
	}{
		{
			name: "empty_defaults_to_maas",
			cfg:  gollm.ProviderConfig{"project_id": "proj", "location": "us-central1", "model": "meta/llama-3.3-70b-instruct-maas"},
			want: "",
		},
		{
			name: "set_to_user_endpoint",
			cfg:  gollm.ProviderConfig{"project_id": "proj", "location": "us-central1", "model": "my-model", "endpoint_id": "mg-endpoint-306f661d"},
			want: "mg-endpoint-306f661d",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubAuth(&gcpAuth{tokenSource: &mockTokenSource{token: "test"}}, nil)
			defer restore()

			p, err := factory(tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			vp, ok := p.(*VertexAIProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *VertexAIProvider", p)
			}
			if vp.endpointID != tc.want {
				t.Errorf("endpointID = %q, want %q", vp.endpointID, tc.want)
			}
		})
	}
}

// TestVertexAIProvider_Factory_EndpointIDWireConflict pins the
// validation that rejects an endpoint_id paired with a wire_override
// that is not openai-compat (a user-deployed endpoint only serves the
// OpenAI chat-completions wire). An openai-compat override, or no
// override, is accepted.
func TestVertexAIProvider_Factory_EndpointIDWireConflict(t *testing.T) {
	tests := []struct {
		name      string
		wire      string
		wantError bool
	}{
		{name: "anthropic_conflicts", wire: string(gollm.WireAnthropic), wantError: true},
		{name: "google_native_conflicts", wire: string(gollm.WireGoogleNative), wantError: true},
		{name: "openai_compat_ok", wire: string(gollm.WireOpenAICompat), wantError: false},
		{name: "no_override_ok", wire: "", wantError: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubAuth(&gcpAuth{tokenSource: &mockTokenSource{token: "test"}}, nil)
			defer restore()

			cfg := gollm.ProviderConfig{
				"project_id":  "proj",
				"location":    "us-central1",
				"model":       "my-model",
				"endpoint_id": "mg-endpoint-306f661d",
			}
			if tc.wire != "" {
				cfg["wire_override"] = tc.wire
			}

			_, err := factory(cfg)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected conflict error for wire_override=%q with endpoint_id", tc.wire)
				}
				for _, want := range []string{"endpoint_id", "wire_override"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q missing %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for wire_override=%q: %v", tc.wire, err)
			}
		})
	}
}

// TestVertexAIProvider_Factory_QuotaProjectHeader pins which auth
// methods send X-Goog-User-Project: ADC (user-account tokens need a
// quota project) yes, service-account keys no (they bill to their own
// project and the header can trigger a serviceusage 403).
func TestVertexAIProvider_Factory_QuotaProjectHeader(t *testing.T) {
	tests := []struct {
		name string
		auth string
		want bool
	}{
		{name: "adc", auth: "adc", want: true},
		{name: "default_empty_is_adc", auth: "", want: true},
		{name: "sa_key", auth: "sa_key", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubAuth(&gcpAuth{tokenSource: &mockTokenSource{token: "test"}}, nil)
			defer restore()

			cfg := gollm.ProviderConfig{"project_id": "proj", "location": "us-central1", "model": "gemini-2.5-pro"}
			if tc.auth != "" {
				cfg["auth_method"] = tc.auth
			}
			p, err := factory(cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			if got := p.(*VertexAIProvider).useQuotaProjectHeader; got != tc.want {
				t.Errorf("useQuotaProjectHeader = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestVertexAI_OpenAICompatChat_SAKeySkipsQuotaHeader asserts a
// service-account-key provider does not send X-Goog-User-Project on the
// chat request.
func TestVertexAI_OpenAICompatChat_SAKeySkipsQuotaHeader(t *testing.T) {
	const endpointID = "mg-endpoint-sa"
	const dedicatedDNS = "mg-endpoint-sa.us-central1-1.prediction.vertexai.goog"
	var sawQuotaHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(endpointLookupJSON(true, dedicatedDNS)))
			return
		}
		if r.Header.Get("X-Goog-User-Project") != "" {
			sawQuotaHeader = true
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	p, _ := providerWithCapture(server.URL, "m", endpointID)
	p.useQuotaProjectHeader = false // service-account key
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawQuotaHeader {
		t.Error("service-account key must not send X-Goog-User-Project")
	}
}

// TestVertexAIProvider_Factory_EndpointID_TimeoutWired guards against a
// regression where the endpoint_id validation short-circuited before
// the HTTP client was constructed: a user-endpoint provider must still
// receive the resolved timeout.
func TestVertexAIProvider_Factory_EndpointID_TimeoutWired(t *testing.T) {
	restore := stubAuth(&gcpAuth{tokenSource: &mockTokenSource{token: "test"}}, nil)
	defer restore()

	p, err := factory(gollm.ProviderConfig{
		"project_id":      "proj",
		"location":        "us-central1",
		"model":           "my-model",
		"endpoint_id":     "mg-endpoint-306f661d",
		"timeout_seconds": "123",
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	vp := p.(*VertexAIProvider)
	if vp.httpClient.Timeout != 123*time.Second {
		t.Errorf("timeout = %v, want 123s", vp.httpClient.Timeout)
	}
}
