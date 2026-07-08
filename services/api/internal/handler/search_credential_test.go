package handler

import "testing"

// TestCredentialWithEnvFallback pins the resolution order the API-side
// search/ask providers use: a per-project secret wins, and when it's empty
// (managed-inference gateway mode stores no per-project AI secret) the
// named env var is the fallback. Without the fallback, managed-mode Ask and
// semantic search build credential-less providers and fail.
func TestCredentialWithEnvFallback(t *testing.T) {
	t.Run("secret wins over env", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "from-env")
		if got := credentialWithEnvFallback("from-secret", "LLM_API_KEY"); got != "from-secret" {
			t.Errorf("got %q, want from-secret", got)
		}
	})

	t.Run("env fallback when no secret", func(t *testing.T) {
		t.Setenv("EMBEDDING_API_KEY", "dbx-live-embed")
		if got := credentialWithEnvFallback("", "EMBEDDING_API_KEY"); got != "dbx-live-embed" {
			t.Errorf("got %q, want dbx-live-embed", got)
		}
	})

	t.Run("empty when neither", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "")
		if got := credentialWithEnvFallback("", "LLM_API_KEY"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}
