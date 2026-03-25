package config

import (
	"fmt"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
)

type Config struct {
	Service ServiceConfig
	MongoDB MongoDBConfig
	Server  ServerConfig
	Auth    AuthConfig
}

type AuthConfig struct {
	Enabled      bool
	IssuerURL    string
	Audience     string
	ClaimSub     string
	ClaimEmail   string
	ClaimOrgID   string
	ClaimRoles   string
	DefaultOrgID string
	DefaultRole  string
}

type ServiceConfig struct {
	Name        string
	Environment string
	LogLevel    string
}

type MongoDBConfig struct {
	URI      string
	Database string
}

type ServerConfig struct {
	Port string
}

func Load() (*Config, error) {
	cfg := &Config{
		Service: ServiceConfig{
			Name:        goconfig.GetEnvOrDefault("SERVICE_NAME", "decisionbox-api"),
			Environment: goconfig.GetEnvOrDefault("ENV", "dev"),
			LogLevel:    goconfig.GetEnvOrDefault("LOG_LEVEL", "info"),
		},
		MongoDB: MongoDBConfig{
			URI:      goconfig.GetEnv("MONGODB_URI"),
			Database: goconfig.GetEnvOrDefault("MONGODB_DB", "decisionbox"),
		},
		Server: ServerConfig{
			Port: goconfig.GetEnvOrDefault("PORT", "8080"),
		},
		Auth: AuthConfig{
			Enabled:      goconfig.GetEnvAsBool("AUTH_ENABLED", false),
			IssuerURL:    goconfig.GetEnv("AUTH_ISSUER_URL"),
			Audience:     goconfig.GetEnv("AUTH_AUDIENCE"),
			ClaimSub:     goconfig.GetEnvOrDefault("AUTH_CLAIM_SUB", "sub"),
			ClaimEmail:   goconfig.GetEnvOrDefault("AUTH_CLAIM_EMAIL", "email"),
			ClaimOrgID:   goconfig.GetEnvOrDefault("AUTH_CLAIM_ORG_ID", "org_id"),
			ClaimRoles:   goconfig.GetEnvOrDefault("AUTH_CLAIM_ROLES", "roles"),
			DefaultOrgID: goconfig.GetEnvOrDefault("AUTH_DEFAULT_ORG_ID", "default"),
			DefaultRole:  goconfig.GetEnvOrDefault("AUTH_DEFAULT_ROLE", "member"),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.MongoDB.URI == "" {
		return fmt.Errorf("MONGODB_URI is required")
	}
	if c.Auth.Enabled {
		if c.Auth.IssuerURL == "" {
			return fmt.Errorf("AUTH_ISSUER_URL is required when AUTH_ENABLED=true")
		}
		if c.Auth.Audience == "" {
			return fmt.Errorf("AUTH_AUDIENCE is required when AUTH_ENABLED=true")
		}
	}
	return nil
}
