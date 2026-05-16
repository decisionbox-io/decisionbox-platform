package vertexai

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/gcpcreds"
	"golang.org/x/oauth2"
)

// gcpAuth manages GCP access tokens from a resolved oauth2 source.
// Tokens are cached and auto-refreshed; the underlying source is provided
// by libs/gcpcreds based on the project's selected auth method (adc or
// sa_key).
type gcpAuth struct {
	mu          sync.Mutex
	tokenSource oauth2.TokenSource
}

// newGCPAuth resolves credentials via libs/gcpcreds and wraps the
// returned oauth2.TokenSource for use by the dispatcher.
func newGCPAuth(ctx context.Context, c gcpcreds.Config) (*gcpAuth, error) {
	src, err := gcpcreds.TokenSource(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai: %w", err)
	}
	return &gcpAuth{tokenSource: src}, nil
}

// token returns a valid access token, refreshing if needed.
func (a *gcpAuth) token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tok, err := a.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("vertex-ai: failed to get access token: %w", err)
	}

	if tok.Expiry.Before(time.Now().Add(30 * time.Second)) {
		// Token is about to expire, force refresh
		tok, err = a.tokenSource.Token()
		if err != nil {
			return "", fmt.Errorf("vertex-ai: failed to refresh access token: %w", err)
		}
	}

	return tok.AccessToken, nil
}
