package claude

import (
	"context"
	"errors"
	"net/http"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// A refusal is final: it surfaces as gollm.ErrModelRefused after one call,
// without the transport retries (the same request is refused again).
func TestChat_RefusalIsAnErrorAndNotRetried(t *testing.T) {
	var raw []byte
	calls := 0
	srv := captureServer(t, `{"content":[],"stop_reason":"refusal","usage":{"input_tokens":120,"output_tokens":0}}`, &raw)
	defer srv.Close()
	p := providerWithCache(t, srv, false)
	p.maxRetries = 3
	inner := p.httpClient.Transport
	p.httpClient.Transport = countingTransport{inner: inner, n: &calls}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, gollm.ErrModelRefused) {
		t.Fatalf("err = %v, want ErrModelRefused", err)
	}
	if calls != 1 {
		t.Fatalf("refusal sent %d requests, want 1", calls)
	}
}

// countingTransport counts round trips through an inner transport.
type countingTransport struct {
	inner http.RoundTripper
	n     *int
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*c.n++
	return c.inner.RoundTrip(r)
}
