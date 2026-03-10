package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Set required env vars
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("LLM_API_KEY", "test-key")
	os.Setenv("WAREHOUSE_DATASET", "test_dataset")
	os.Setenv("WAREHOUSE_PROJECT_ID", "test-project")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("LLM_API_KEY")
		os.Unsetenv("WAREHOUSE_DATASET")
		os.Unsetenv("WAREHOUSE_PROJECT_ID")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Service.Name != "decisionbox-agent" {
		t.Errorf("Name = %q, want %q", cfg.Service.Name, "decisionbox-agent")
	}
	if cfg.LLM.Provider != "claude" {
		t.Errorf("LLM.Provider = %q, want %q", cfg.LLM.Provider, "claude")
	}
	if cfg.Warehouse.Provider != "bigquery" {
		t.Errorf("Warehouse.Provider = %q, want %q", cfg.Warehouse.Provider, "bigquery")
	}
	if cfg.Discovery.MaxSteps != 100 {
		t.Errorf("MaxSteps = %d, want 100", cfg.Discovery.MaxSteps)
	}
}

func TestValidateMissingMongoDB(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.APIKey = "test"
	cfg.Warehouse.Dataset = "test"

	err := cfg.Validate()
	if err == nil {
		t.Error("should fail without MONGODB_URI")
	}
}

func TestValidateMissingLLMKey(t *testing.T) {
	cfg := &Config{}
	cfg.MongoDB.URI = "mongodb://localhost"
	cfg.Warehouse.Dataset = "test"

	err := cfg.Validate()
	if err == nil {
		t.Error("should fail without LLM_API_KEY")
	}
}

func TestValidateMissingDataset(t *testing.T) {
	cfg := &Config{}
	cfg.MongoDB.URI = "mongodb://localhost"
	cfg.LLM.APIKey = "test"

	err := cfg.Validate()
	if err == nil {
		t.Error("should fail without WAREHOUSE_DATASET")
	}
}

func TestValidateBigQueryRequiresProjectID(t *testing.T) {
	cfg := &Config{}
	cfg.MongoDB.URI = "mongodb://localhost"
	cfg.LLM.APIKey = "test"
	cfg.Warehouse.Dataset = "test"
	cfg.Warehouse.Provider = "bigquery"
	cfg.Warehouse.ProjectID = "" // missing

	err := cfg.Validate()
	if err == nil {
		t.Error("should fail for BigQuery without project_id")
	}
}

func TestValidateSuccess(t *testing.T) {
	cfg := &Config{}
	cfg.MongoDB.URI = "mongodb://localhost"
	cfg.LLM.APIKey = "test"
	cfg.Warehouse.Dataset = "test"
	cfg.Warehouse.Provider = "bigquery"
	cfg.Warehouse.ProjectID = "my-project"

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

	cfg.Service.Environment = "production"
	if !cfg.IsProduction() {
		t.Error("production should be production")
	}
}
