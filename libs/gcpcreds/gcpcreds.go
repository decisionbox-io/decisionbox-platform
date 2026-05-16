// Package gcpcreds resolves a GCP token source / client options slice
// from a generic AuthMethod-based project configuration, shared by every
// GCP-backed DecisionBox provider (warehouse BigQuery, LLM Vertex AI,
// embedding Vertex AI, secrets GCP Secret Manager).
//
// Resolution order, per project:
//   - sa_key with non-empty CredentialsJSON: parse and use the project
//     service-account JSON.
//   - sa_key with empty CredentialsJSON: fall through to ADC, which
//     honours GOOGLE_APPLICATION_CREDENTIALS.
//   - adc (or empty method): ADC chain — GOOGLE_APPLICATION_CREDENTIALS,
//     well-known gcloud file, then metadata server.
package gcpcreds

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// Method identifiers stored in a provider's ProviderConfig under
// "auth_method".
const (
	MethodADC   = "adc"
	MethodSAKey = "sa_key"
)

// Field keys read from a provider's ProviderConfig map. Provider
// AuthMethod.Fields[].Key entries must match these.
const FieldCredentials = "credentials_json"

// DefaultScope is the cloud-platform scope used when c.Scopes is empty.
const DefaultScope = "https://www.googleapis.com/auth/cloud-platform"

// Config carries the inputs the helpers need.
type Config struct {
	Method          string
	CredentialsJSON string
	Scopes          []string
}

// TokenSource returns an oauth2.TokenSource for HTTP-based callers
// (Vertex AI dispatches against Google APIs over plain HTTP).
func TokenSource(ctx context.Context, c Config) (oauth2.TokenSource, error) {
	method, scopes := c.Method, c.Scopes
	if method == "" {
		method = MethodADC
	}
	if len(scopes) == 0 {
		scopes = []string{DefaultScope}
	}

	switch method {
	case MethodSAKey:
		if c.CredentialsJSON != "" {
			creds, err := google.CredentialsFromJSON(ctx, []byte(c.CredentialsJSON), scopes...)
			if err != nil {
				return nil, fmt.Errorf("gcpcreds: invalid service-account JSON: %w", err)
			}
			return creds.TokenSource, nil
		}
		return findDefault(ctx, scopes)

	case MethodADC:
		return findDefault(ctx, scopes)

	default:
		return nil, fmt.Errorf("gcpcreds: unsupported auth method %q", method)
	}
}

// ClientOptions returns the option.ClientOption slice for Google Cloud
// SDK clients (BigQuery and the Vertex SDK both consume this shape).
// An empty slice means "let the SDK do its own ADC".
func ClientOptions(ctx context.Context, c Config) ([]option.ClientOption, error) {
	method := c.Method
	if method == "" {
		method = MethodADC
	}

	switch method {
	case MethodSAKey:
		if c.CredentialsJSON == "" {
			// Empty project JSON: SDK will use ADC, which picks up
			// GOOGLE_APPLICATION_CREDENTIALS.
			return nil, nil
		}
		if _, err := google.CredentialsFromJSON(ctx, []byte(c.CredentialsJSON), DefaultScope); err != nil {
			return nil, fmt.Errorf("gcpcreds: invalid service-account JSON: %w", err)
		}
		// option.WithCredentialsJSON is the canonical way to inject a
		// service-account key JSON into a Google Cloud SDK client. The
		// staticcheck deprecation notice flags it as a "potential
		// security risk" because the JSON sits in process memory — that
		// is true of every other transport (file path, token source,
		// env var pointed at a file) since the SDK ultimately needs the
		// bytes. The platform takes the JSON from the secret provider
		// (already in process memory, never logged) and hands it
		// straight to the SDK; there is no longer-lived exposure to
		// avoid here. The alternative APIs (google.CredentialsFromJSON
		// + option.WithTokenSource) end up holding the same bytes.
		return []option.ClientOption{option.WithCredentialsJSON([]byte(c.CredentialsJSON))}, nil //nolint:staticcheck // SA1019: see comment above

	case MethodADC:
		return nil, nil

	default:
		return nil, fmt.Errorf("gcpcreds: unsupported auth method %q", method)
	}
}

func findDefault(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
	creds, err := google.FindDefaultCredentials(ctx, scopes...)
	if err != nil {
		return nil, fmt.Errorf("gcpcreds: failed to find default GCP credentials (run 'gcloud auth application-default login' or set GOOGLE_APPLICATION_CREDENTIALS): %w", err)
	}
	return creds.TokenSource, nil
}
