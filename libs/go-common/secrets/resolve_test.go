package secrets

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeProvider is a minimal Provider whose Get returns a fixed value/error.
type fakeProvider struct {
	value string
	err   error
}

func (f fakeProvider) Get(_ context.Context, _, _ string) (string, error) {
	return f.value, f.err
}
func (fakeProvider) Set(_ context.Context, _, _, _ string) error { return nil }
func (fakeProvider) List(_ context.Context, _ string) ([]SecretEntry, error) {
	return nil, nil
}

func TestResolveCredential(t *testing.T) {
	const (
		envVar    = "TEST_RESOLVE_API_KEY"
		secretVal = "from-secret"
		envVal    = "from-env"
	)
	backendErr := errors.New("boom: connection refused")

	tests := []struct {
		name       string
		provider   Provider // nil means no provider wired
		env        string   // "" means env var unset
		wantValue  string
		wantSource string
		wantErr    error
	}{
		{
			name:       "secret wins over env",
			provider:   fakeProvider{value: secretVal},
			env:        envVal,
			wantValue:  secretVal,
			wantSource: SourceDashboard,
		},
		{
			name:       "empty secret falls back to env",
			provider:   fakeProvider{value: ""},
			env:        envVal,
			wantValue:  envVal,
			wantSource: SourceEnv,
		},
		{
			name:       "ErrNotFound falls back to env",
			provider:   fakeProvider{err: ErrNotFound},
			env:        envVal,
			wantValue:  envVal,
			wantSource: SourceEnv,
		},
		{
			name:       "wrapped ErrNotFound falls back to env",
			provider:   fakeProvider{err: fmt.Errorf("gcp: %w", ErrNotFound)},
			env:        envVal,
			wantValue:  envVal,
			wantSource: SourceEnv,
		},
		{
			name:       "neither secret nor env",
			provider:   fakeProvider{value: ""},
			env:        "",
			wantValue:  "",
			wantSource: SourceNone,
		},
		{
			name:       "backend error still falls back to env, surfacing err",
			provider:   fakeProvider{err: backendErr},
			env:        envVal,
			wantValue:  envVal,
			wantSource: SourceEnv,
			wantErr:    backendErr,
		},
		{
			name:       "backend error, no env, surfaces err",
			provider:   fakeProvider{err: backendErr},
			env:        "",
			wantValue:  "",
			wantSource: SourceNone,
			wantErr:    backendErr,
		},
		{
			name:       "nil provider uses env",
			provider:   nil,
			env:        envVal,
			wantValue:  envVal,
			wantSource: SourceEnv,
		},
		{
			name:       "nil provider, no env",
			provider:   nil,
			env:        "",
			wantValue:  "",
			wantSource: SourceNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv(envVar, tt.env)
			} else {
				// Ensure a leaked value from the environment can't taint the case.
				t.Setenv(envVar, "")
			}
			value, source, err := ResolveCredential(context.Background(), tt.provider, "proj", "some-credentials", envVar)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
			if value != tt.wantValue {
				t.Errorf("value = %q, want %q", value, tt.wantValue)
			}
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
		})
	}
}
