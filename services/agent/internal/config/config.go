package config

import (
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
)

// Config holds all service configuration.
type Config struct {
	Service    ServiceConfig
	Warehouse  WarehouseConfig
	MongoDB    MongoDBConfig
	LLM        LLMConfig
	Discovery  DiscoveryConfig
	Validation ValidationConfig
}

// ServiceConfig contains service-level configuration.
type ServiceConfig struct {
	Name        string
	Environment string
	LogLevel    string
}

// WarehouseConfig contains data warehouse configuration.
type WarehouseConfig struct {
	Provider  string // "bigquery" (future: "clickhouse", "redshift")
	ProjectID string
	Dataset   string
	Location  string
	Timeout   time.Duration
}

// MongoDBConfig contains MongoDB configuration.
type MongoDBConfig struct {
	URI      string
	Database string
}

// LLMConfig contains LLM provider configuration.
type LLMConfig struct {
	Provider       string // "claude" (future: "openai", "gemini")
	APIKey         string
	Model          string
	MaxRetries     int
	Timeout        time.Duration
	RequestDelayMs int
}

// DiscoveryConfig contains discovery-specific configuration.
type DiscoveryConfig struct {
	MaxSteps              int
	MaxSQLRetries         int
	TimeoutPerStep        time.Duration
	EnableContextLearning bool
	MinPatternUsers       int
	PromptsDir            string
}

// ValidationConfig contains ruleset validation API configuration.
type ValidationConfig struct {
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	env := config.GetEnvOrDefault("ENV", "dev")

	cfg := &Config{
		Service: ServiceConfig{
			Name:        config.GetEnvOrDefault("SERVICE_NAME", "decisionbox-agent"),
			Environment: env,
			LogLevel:    config.GetEnvOrDefault("LOG_LEVEL", "info"),
		},
		Warehouse: WarehouseConfig{
			Provider:  config.GetEnvOrDefault("WAREHOUSE_PROVIDER", "bigquery"),
			ProjectID: config.GetEnv("WAREHOUSE_PROJECT_ID"),
			Dataset:   config.GetEnv("WAREHOUSE_DATASET"),
			Location:  config.GetEnvOrDefault("WAREHOUSE_LOCATION", "US"),
			Timeout:   parseDuration("WAREHOUSE_TIMEOUT", "5m"),
		},
		MongoDB: MongoDBConfig{
			URI:      config.GetEnv("MONGODB_URI"),
			Database: config.GetEnvOrDefault("MONGODB_DB", "decisionbox"),
		},
		LLM: LLMConfig{
			Provider:       config.GetEnvOrDefault("LLM_PROVIDER", "claude"),
			APIKey:         config.GetEnv("LLM_API_KEY"),
			Model:          config.GetEnvOrDefault("LLM_MODEL", "claude-sonnet-4-20250514"),
			MaxRetries:     config.GetEnvAsInt("LLM_MAX_RETRIES", 3),
			Timeout:        parseDuration("LLM_TIMEOUT", "60s"),
			RequestDelayMs: config.GetEnvAsInt("LLM_REQUEST_DELAY_MS", 2000),
		},
		Discovery: DiscoveryConfig{
			MaxSteps:              config.GetEnvAsInt("DISCOVERY_MAX_STEPS", 100),
			MaxSQLRetries:         config.GetEnvAsInt("DISCOVERY_MAX_SQL_RETRIES", 5),
			TimeoutPerStep:        parseDuration("DISCOVERY_TIMEOUT_PER_STEP", "5m"),
			EnableContextLearning: config.GetEnvOrDefault("DISCOVERY_ENABLE_CONTEXT_LEARNING", "true") == "true",
			MinPatternUsers:       config.GetEnvAsInt("DISCOVERY_MIN_PATTERN_USERS", 50),
			PromptsDir:            config.GetEnvOrDefault("PROMPTS_DIR", "./prompts"),
		},
		Validation: ValidationConfig{
			BaseURL:    config.GetEnv("VALIDATION_API_URL"),
			Timeout:    parseDuration("VALIDATION_TIMEOUT", "30s"),
			MaxRetries: config.GetEnvAsInt("VALIDATION_MAX_RETRIES", 3),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks required configuration.
func (c *Config) Validate() error {
	if c.MongoDB.URI == "" {
		return fmt.Errorf("MONGODB_URI is required")
	}
	// API key required only for direct API providers (claude, openai)
	// Vertex AI uses GCP credentials, Bedrock uses AWS credentials, Ollama needs no key
	if c.LLM.APIKey == "" && (c.LLM.Provider == "claude" || c.LLM.Provider == "openai") {
		return fmt.Errorf("LLM_API_KEY is required for %s provider", c.LLM.Provider)
	}
	if c.Warehouse.Provider == "bigquery" && c.Warehouse.ProjectID == "" {
		return fmt.Errorf("WAREHOUSE_PROJECT_ID is required for BigQuery provider")
	}
	if c.Warehouse.Dataset == "" {
		return fmt.Errorf("WAREHOUSE_DATASET is required")
	}
	return nil
}

// IsProduction returns true if running in production.
func (c *Config) IsProduction() bool {
	return c.Service.Environment == "prod" || c.Service.Environment == "production"
}

func parseDuration(key, defaultVal string) time.Duration {
	val := config.GetEnvOrDefault(key, defaultVal)
	d, err := time.ParseDuration(val)
	if err != nil {
		d, _ = time.ParseDuration(defaultVal)
	}
	return d
}
