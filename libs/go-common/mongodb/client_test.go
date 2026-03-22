package mongodb

import (
	"context"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxPoolSize != 100 {
		t.Errorf("MaxPoolSize = %d, want 100", cfg.MaxPoolSize)
	}
	if cfg.MinPoolSize != 10 {
		t.Errorf("MinPoolSize = %d, want 10", cfg.MinPoolSize)
	}
	if cfg.MaxConnIdleTime != 60*time.Second {
		t.Errorf("MaxConnIdleTime = %v, want 60s", cfg.MaxConnIdleTime)
	}
	if cfg.ConnectTimeout != 10*time.Second {
		t.Errorf("ConnectTimeout = %v, want 10s", cfg.ConnectTimeout)
	}
	if cfg.ServerSelectionTimeout != 10*time.Second {
		t.Errorf("ServerSelectionTimeout = %v, want 10s", cfg.ServerSelectionTimeout)
	}
}

func TestDefaultConfig_FieldDefaults(t *testing.T) {
	cfg := DefaultConfig()

	// URI and Database should be empty by default (user must set them)
	if cfg.URI != "" {
		t.Errorf("URI = %q, want empty", cfg.URI)
	}
	if cfg.Database != "" {
		t.Errorf("Database = %q, want empty", cfg.Database)
	}
}

func TestNewClient_InvalidURI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := DefaultConfig()
	cfg.URI = "invalid://uri"
	cfg.Database = "test"
	cfg.ServerSelectionTimeout = 1 * time.Second
	cfg.ConnectTimeout = 1 * time.Second

	_, err := NewClient(ctx, cfg)
	if err == nil {
		t.Fatal("NewClient with invalid URI should return error")
	}
}

func TestNewClient_EmptyURI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := DefaultConfig()
	cfg.URI = ""
	cfg.Database = "test"
	cfg.ServerSelectionTimeout = 1 * time.Second
	cfg.ConnectTimeout = 1 * time.Second

	_, err := NewClient(ctx, cfg)
	if err == nil {
		t.Fatal("NewClient with empty URI should return error")
	}
}

func TestNewClient_UnreachableHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cfg := DefaultConfig()
	cfg.URI = "mongodb://localhost:27099"
	cfg.Database = "test"
	cfg.ServerSelectionTimeout = 1 * time.Second
	cfg.ConnectTimeout = 1 * time.Second

	_, err := NewClient(ctx, cfg)
	if err == nil {
		t.Fatal("NewClient with unreachable host should return error")
	}
}
