package secrets

import (
	"fmt"
	"os"
	"sync"
)

// ProviderFactory creates a Provider from configuration.
type ProviderFactory func(cfg Config) (Provider, error)

// ProviderMeta describes a secret provider for documentation.
type ProviderMeta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var (
	mu         sync.RWMutex
	factories  = make(map[string]ProviderFactory)
	metas      = make(map[string]ProviderMeta)
)

// Register registers a secret provider factory.
func Register(name string, factory ProviderFactory, meta ProviderMeta) {
	mu.Lock()
	defer mu.Unlock()
	meta.ID = name
	factories[name] = factory
	metas[name] = meta
}

// NewProvider creates a Provider by name from configuration.
func NewProvider(cfg Config) (Provider, error) {
	mu.RLock()
	factory, ok := factories[cfg.Provider]
	mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown secret provider: %q (registered: %v)", cfg.Provider, RegisteredProviders())
	}

	if cfg.Namespace == "" {
		cfg.Namespace = "decisionbox"
	}

	return factory(cfg)
}

// RegisteredProviders returns all registered provider names.
func RegisteredProviders() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for k := range factories {
		names = append(names, k)
	}
	return names
}

// RegisteredProvidersMeta returns metadata for all registered providers.
func RegisteredProvidersMeta() []ProviderMeta {
	mu.RLock()
	defer mu.RUnlock()
	result := make([]ProviderMeta, 0, len(metas))
	for _, m := range metas {
		result = append(result, m)
	}
	return result
}

// LoadConfig loads secret provider config from environment variables.
func LoadConfig() Config {
	return Config{
		Provider:      getEnv("SECRET_PROVIDER", "mongodb"),
		Namespace:     getEnv("SECRET_NAMESPACE", "decisionbox"),
		EncryptionKey: os.Getenv("SECRET_ENCRYPTION_KEY"),
		GCPProjectID:  os.Getenv("SECRET_GCP_PROJECT_ID"),
		AWSRegion:     getEnv("SECRET_AWS_REGION", "us-east-1"),
		AzureVaultURL: os.Getenv("SECRET_AZURE_VAULT_URL"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ErrNotFound is returned when a secret doesn't exist.
var ErrNotFound = fmt.Errorf("secret not found")
