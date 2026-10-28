package azurefoundry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// A safety refusal (200, no content, stop_reason "refusal") reaches the caller
// as gollm.ErrModelRefused rather than as an empty reply.
func TestClaudeRefusalIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"refusal","usage":{"input_tokens":120,"output_tokens":0}}`))
	}))
	defer server.Close()
	p := newTestProviderWithURL(server.URL, "claude-opus-4-8")
	_, err := p.Chat(context.Background(), gollm.ChatRequest{Model: "claude-opus-4-8",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, gollm.ErrModelRefused) {
		t.Fatalf("err = %v, want ErrModelRefused", err)
	}
}
