package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	defer os.Unsetenv("MONGODB_URI")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Service.Name != "decisionbox-agent" {
		t.Errorf("Name = %q", cfg.Service.Name)
	}
	if cfg.MongoDB.Database != "decisionbox" {
		t.Errorf("Database = %q", cfg.MongoDB.Database)
	}
	if cfg.LLM.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d", cfg.LLM.MaxRetries)
	}
}

func TestValidateMissingMongoDB(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Error("should fail without MONGODB_URI")
	}
}

func TestValidateSuccess(t *testing.T) {
	cfg := &Config{}
	cfg.MongoDB.URI = "mongodb://localhost"

	err := cfg.Validate()
	if err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}

func TestIsProduction(t *testing.T) {
	cfg := &Config{}

	cfg.Service.Environment = "dev"
	if cfg.IsProduction() {
		t.Error("dev should not be production")
	}

	cfg.Service.Environment = "prod"
	if !cfg.IsProduction() {
		t.Error("prod should be production")
	}
}

func TestLLMConfigDefaults(t *testing.T) {
	// LLM config has no API key — secrets come from secret provider
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	defer os.Unsetenv("MONGODB_URI")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.LLM.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.LLM.MaxRetries)
	}
	if cfg.LLM.Timeout.Seconds() != 300 {
		t.Errorf("Timeout = %v, want 300s", cfg.LLM.Timeout)
	}
}

func TestLoad_MissingMongoDBURI(t *testing.T) {
	os.Unsetenv("MONGODB_URI")

	_, err := Load()
	if err == nil {
		t.Error("Load() should fail without MONGODB_URI")
	}
}

func TestLoad_CustomEnvVars(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://custom:27017")
	os.Setenv("MONGODB_DB", "custom-db")
	os.Setenv("SERVICE_NAME", "custom-agent")
	os.Setenv("ENV", "staging")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("LLM_MAX_RETRIES", "5")
	os.Setenv("LLM_TIMEOUT", "600s")
	os.Setenv("LLM_REQUEST_DELAY_MS", "2000")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("MONGODB_DB")
		os.Unsetenv("SERVICE_NAME")
		os.Unsetenv("ENV")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("LLM_MAX_RETRIES")
		os.Unsetenv("LLM_TIMEOUT")
		os.Unsetenv("LLM_REQUEST_DELAY_MS")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.MongoDB.URI != "mongodb://custom:27017" {
		t.Errorf("MongoDB.URI = %q, want %q", cfg.MongoDB.URI, "mongodb://custom:27017")
	}
	if cfg.MongoDB.Database != "custom-db" {
		t.Errorf("MongoDB.Database = %q, want %q", cfg.MongoDB.Database, "custom-db")
	}
	if cfg.Service.Name != "custom-agent" {
		t.Errorf("Service.Name = %q, want %q", cfg.Service.Name, "custom-agent")
	}
	if cfg.Service.Environment != "staging" {
		t.Errorf("Service.Environment = %q, want %q", cfg.Service.Environment, "staging")
	}
	if cfg.Service.LogLevel != "debug" {
		t.Errorf("Service.LogLevel = %q, want %q", cfg.Service.LogLevel, "debug")
	}
	if cfg.LLM.MaxRetries != 5 {
		t.Errorf("LLM.MaxRetries = %d, want 5", cfg.LLM.MaxRetries)
	}
	if cfg.LLM.Timeout.Seconds() != 600 {
		t.Errorf("LLM.Timeout = %v, want 600s", cfg.LLM.Timeout)
	}
	if cfg.LLM.RequestDelayMs != 2000 {
		t.Errorf("LLM.RequestDelayMs = %d, want 2000", cfg.LLM.RequestDelayMs)
	}
}

func TestIsProduction_AllVariants(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"prod", true},
		{"production", true},
		{"dev", false},
		{"staging", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			cfg := &Config{}
			cfg.Service.Environment = tt.env
			if got := cfg.IsProduction(); got != tt.want {
				t.Errorf("IsProduction() with env=%q = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

func TestLoad_InvalidLLMTimeout(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("LLM_TIMEOUT", "not-a-duration")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("LLM_TIMEOUT")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Should fall back to default 300s
	if cfg.LLM.Timeout.Seconds() != 300 {
		t.Errorf("LLM.Timeout = %v, want 300s (default fallback)", cfg.LLM.Timeout)
	}
}
