package embedding

import (
	"errors"
	"testing"
)

func TestResolveConfig_EnvWinsWhenBYOKDisabled(t *testing.T) {
	t.Setenv("EMBEDDING_PROVIDER_API_KEY", "cloud-managed-key")
	t.Setenv("EMBEDDING_PROVIDER", "openai")
	t.Setenv("EMBEDDING_MODEL", "text-embedding-3-small")

	r, err := ResolveConfig(ProjectConfig{
		Provider:    "voyage",
		Model:       "voyage-3",
		Credentials: "project-key",
	}, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if r.Source != "env" {
		t.Errorf("Source = %q, want env", r.Source)
	}
	if r.APIKey != "cloud-managed-key" {
		t.Errorf("APIKey = %q, want cloud-managed-key", r.APIKey)
	}
	if r.Provider != "openai" {
		t.Errorf("Provider = %q, want openai (env override)", r.Provider)
	}
	if r.Model != "text-embedding-3-small" {
		t.Errorf("Model = %q", r.Model)
	}
}

func TestResolveConfig_EnvKeyOnly_KeepsProjectProviderAndModel(t *testing.T) {
	t.Setenv("EMBEDDING_PROVIDER_API_KEY", "cloud-key")
	// no EMBEDDING_PROVIDER, no EMBEDDING_MODEL

	r, err := ResolveConfig(ProjectConfig{
		Provider: "voyage",
		Model:    "voyage-3",
	}, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r.Provider != "voyage" || r.Model != "voyage-3" {
		t.Errorf("provider/model not preserved: %+v", r)
	}
	if r.APIKey != "cloud-key" {
		t.Errorf("APIKey = %q", r.APIKey)
	}
}

func TestResolveConfig_BYOKEnabled_UsesProjectCreds(t *testing.T) {
	t.Setenv("EMBEDDING_PROVIDER_API_KEY", "cloud-key")

	r, err := ResolveConfig(ProjectConfig{
		Provider:    "voyage",
		Model:       "voyage-3",
		Credentials: "project-key",
	}, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r.APIKey != "project-key" {
		t.Errorf("APIKey = %q, want project-key (BYOK)", r.APIKey)
	}
	if r.Provider != "voyage" {
		t.Errorf("Provider = %q", r.Provider)
	}
	if r.Source != "project-byok" {
		t.Errorf("Source = %q, want project-byok (env ignored because BYOK was on)", r.Source)
	}
}

func TestResolveConfig_NoEnv_NoProject_Errors(t *testing.T) {
	t.Setenv("EMBEDDING_PROVIDER_API_KEY", "")
	_, err := ResolveConfig(ProjectConfig{}, false)
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}

func TestResolveConfig_NoEnv_ProjectOnly(t *testing.T) {
	t.Setenv("EMBEDDING_PROVIDER_API_KEY", "")

	r, err := ResolveConfig(ProjectConfig{
		Provider:    "voyage",
		Model:       "voyage-3",
		Credentials: "project-key",
	}, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r.Source != "project" {
		t.Errorf("Source = %q", r.Source)
	}
	if r.APIKey != "project-key" {
		t.Errorf("APIKey = %q", r.APIKey)
	}
}
