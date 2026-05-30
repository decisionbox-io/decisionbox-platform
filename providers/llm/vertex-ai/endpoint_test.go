package vertexai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestVertexAIProvider_OpenAICompatURL pins the two endpoint shapes the
// OpenAI-compat path builds: the shared Model Garden MaaS endpoint when
// endpoint_id is blank, and the user-deployed endpoint (endpoint ID
// embedded, no /openapi/ segment) when it is set. The region-scoped host
// must collapse to the bare host only for the "global" location.
func TestVertexAIProvider_OpenAICompatURL(t *testing.T) {
	tests := []struct {
		name       string
		location   string
		endpointID string
		want       string
	}{
		{
			name:     "maas_regional",
			location: "us-central1",
			want:     "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/proj/locations/us-central1/endpoints/openapi/chat/completions",
		},
		{
			name:     "maas_global",
			location: "global",
			want:     "https://aiplatform.googleapis.com/v1beta1/projects/proj/locations/global/endpoints/openapi/chat/completions",
		},
		{
			name:       "user_endpoint_regional",
			location:   "us-central1",
			endpointID: "1234567890123456789",
			want:       "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/proj/locations/us-central1/endpoints/1234567890123456789/chat/completions",
		},
		{
			name:       "user_endpoint_global",
			location:   "global",
			endpointID: "987654321",
			want:       "https://aiplatform.googleapis.com/v1beta1/projects/proj/locations/global/endpoints/987654321/chat/completions",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &VertexAIProvider{projectID: "proj", location: tc.location, endpointID: tc.endpointID}
			if got := p.openAICompatURL(); got != tc.want {
				t.Errorf("openAICompatURL() = %q, want %q", got, tc.want)
			}
			// The user-deployed shape must never carry the /openapi/
			// segment; the MaaS shape must always carry it.
			gotOpenapi := strings.Contains(p.openAICompatURL(), "/endpoints/openapi/")
			wantOpenapi := tc.endpointID == ""
			if gotOpenapi != wantOpenapi {
				t.Errorf("/openapi/ present = %v, want %v (url=%q)", gotOpenapi, wantOpenapi, p.openAICompatURL())
			}
		})
	}
}

// TestVertexAI_OpenAICompatChat_RoutesToUserEndpoint asserts that with
// endpoint_id set, a chat request targets
// .../endpoints/{endpoint_id}/chat/completions (no /openapi/ segment)
// and forwards an arbitrary, un-prefixed model name through verbatim —
// custom endpoints don't require a publisher prefix and the model would
// not resolve through the publisher catalog or family inferrer.
func TestVertexAI_OpenAICompatChat_RoutesToUserEndpoint(t *testing.T) {
	const customModel = "my-finetuned-qwen-27b"
	const endpointID = "1234567890123456789"

	var capturedPath string
	var capturedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var decoded struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &decoded)
		capturedModel = decoded.Model

		resp := `{"model":"my-finetuned-qwen-27b",
			"choices":[{"index":0,"message":{"role":"assistant","content":"custom says hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, customModel)
	p.endpointID = endpointID

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

	wantPath := "/v1beta1/projects/test-project/locations/us-central1/endpoints/" + endpointID + "/chat/completions"
	if capturedPath != wantPath {
		t.Errorf("path = %q, want %q", capturedPath, wantPath)
	}
	if strings.Contains(capturedPath, "/openapi/") {
		t.Errorf("path %q must not contain the /openapi/ MaaS segment", capturedPath)
	}
	if capturedModel != customModel {
		t.Errorf("request model = %q, want verbatim %q (no publisher mangling)", capturedModel, customModel)
	}
}

// TestVertexAI_Dispatch_EndpointIDBeatsCatalog asserts the endpoint_id
// short-circuit wins over catalog wire resolution: a model that would
// otherwise dispatch to the Google-native wire (gemini-2.5-pro) is
// instead sent to the user-deployed OpenAI-compat endpoint when
// endpoint_id is set.
func TestVertexAI_Dispatch_EndpointIDBeatsCatalog(t *testing.T) {
	const endpointID = "55555"

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		resp := `{"model":"gemini-2.5-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	p := newTestProviderWithURL(server.URL, "gemini-2.5-pro")
	p.endpointID = endpointID

	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "gemini-2.5-pro",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPath := "/v1beta1/projects/test-project/locations/us-central1/endpoints/" + endpointID + "/chat/completions"
	if capturedPath != wantPath {
		t.Errorf("path = %q, want the user-deployed endpoint %q (endpoint_id must beat the catalog's google-native wire)", capturedPath, wantPath)
	}
}

// TestVertexAIProvider_Factory_EndpointID pins that the factory parses
// the endpoint_id cfg field cleanly for both the empty (MaaS) and set
// (user-deployed) cases. Auth is stubbed so the assertion does not
// depend on ADC being available.
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
			cfg:  gollm.ProviderConfig{"project_id": "proj", "location": "us-central1", "model": "my-model", "endpoint_id": "1234567890123456789"},
			want: "1234567890123456789",
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
// that is not openai-compat (the user-deployed endpoint only serves the
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
				"endpoint_id": "1234567890123456789",
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

// TestVertexAIProvider_Factory_EndpointID_TimeoutWired guards against a
// regression where adding the endpoint_id validation short-circuited
// before the HTTP client was constructed: a user-endpoint provider must
// still receive the resolved timeout.
func TestVertexAIProvider_Factory_EndpointID_TimeoutWired(t *testing.T) {
	restore := stubAuth(&gcpAuth{tokenSource: &mockTokenSource{token: "test"}}, nil)
	defer restore()

	p, err := factory(gollm.ProviderConfig{
		"project_id":      "proj",
		"location":        "us-central1",
		"model":           "my-model",
		"endpoint_id":     "1234567890123456789",
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
