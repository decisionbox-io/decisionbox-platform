package config

import (
	"os"
	"testing"
)

func TestLoad_Defaults_RequiresMongoDBURI(t *testing.T) {
	// Ensure MONGODB_URI is not set
	os.Unsetenv("MONGODB_URI")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() should return error when MONGODB_URI is not set")
	}
	if err.Error() != "MONGODB_URI is required" {
		t.Errorf("error = %q, want %q", err.Error(), "MONGODB_URI is required")
	}
}

func TestLoad_WithMongoDBURI(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	defer os.Unsetenv("MONGODB_URI")

	// Clear overrides to get defaults
	for _, key := range []string{"SERVICE_NAME", "ENV", "LOG_LEVEL", "MONGODB_DB", "PORT"} {
		os.Unsetenv(key)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.Name != "decisionbox-api" {
		t.Errorf("Service.Name = %q, want %q", cfg.Service.Name, "decisionbox-api")
	}
	if cfg.Service.Environment != "dev" {
		t.Errorf("Service.Environment = %q, want %q", cfg.Service.Environment, "dev")
	}
	if cfg.Service.LogLevel != "info" {
		t.Errorf("Service.LogLevel = %q, want %q", cfg.Service.LogLevel, "info")
	}
	if cfg.MongoDB.URI != "mongodb://localhost:27017" {
		t.Errorf("MongoDB.URI = %q", cfg.MongoDB.URI)
	}
	if cfg.MongoDB.Database != "decisionbox" {
		t.Errorf("MongoDB.Database = %q, want %q", cfg.MongoDB.Database, "decisionbox")
	}
	if cfg.Server.Port != "8080" {
		t.Errorf("Server.Port = %q, want %q", cfg.Server.Port, "8080")
	}
}

func TestLoad_CustomPort(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("PORT", "9090")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("PORT")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != "9090" {
		t.Errorf("Server.Port = %q, want %q", cfg.Server.Port, "9090")
	}
}

func TestLoad_CustomServiceName(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("SERVICE_NAME", "my-api")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("SERVICE_NAME")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.Name != "my-api" {
		t.Errorf("Service.Name = %q, want %q", cfg.Service.Name, "my-api")
	}
}

func TestLoad_CustomEnvironment(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("ENV", "production")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("ENV")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.Environment != "production" {
		t.Errorf("Service.Environment = %q, want %q", cfg.Service.Environment, "production")
	}
}

func TestLoad_CustomLogLevel(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("LOG_LEVEL", "debug")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("LOG_LEVEL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.LogLevel != "debug" {
		t.Errorf("Service.LogLevel = %q, want %q", cfg.Service.LogLevel, "debug")
	}
}

func TestLoad_CustomDatabase(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	os.Setenv("MONGODB_DB", "mydb")
	defer func() {
		os.Unsetenv("MONGODB_URI")
		os.Unsetenv("MONGODB_DB")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.MongoDB.Database != "mydb" {
		t.Errorf("MongoDB.Database = %q, want %q", cfg.MongoDB.Database, "mydb")
	}
}

func TestLoad_AllCustomValues(t *testing.T) {
	os.Setenv("MONGODB_URI", "mongodb://prod-host:27017/prod")
	os.Setenv("MONGODB_DB", "proddb")
	os.Setenv("PORT", "3000")
	os.Setenv("SERVICE_NAME", "prod-api")
	os.Setenv("ENV", "production")
	os.Setenv("LOG_LEVEL", "warn")
	defer func() {
		for _, key := range []string{"MONGODB_URI", "MONGODB_DB", "PORT", "SERVICE_NAME", "ENV", "LOG_LEVEL"} {
			os.Unsetenv(key)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.MongoDB.URI != "mongodb://prod-host:27017/prod" {
		t.Errorf("MongoDB.URI = %q", cfg.MongoDB.URI)
	}
	if cfg.MongoDB.Database != "proddb" {
		t.Errorf("MongoDB.Database = %q", cfg.MongoDB.Database)
	}
	if cfg.Server.Port != "3000" {
		t.Errorf("Server.Port = %q", cfg.Server.Port)
	}
	if cfg.Service.Name != "prod-api" {
		t.Errorf("Service.Name = %q", cfg.Service.Name)
	}
	if cfg.Service.Environment != "production" {
		t.Errorf("Service.Environment = %q", cfg.Service.Environment)
	}
	if cfg.Service.LogLevel != "warn" {
		t.Errorf("Service.LogLevel = %q", cfg.Service.LogLevel)
	}
}

func TestValidate_EmptyURI(t *testing.T) {
	cfg := &Config{
		MongoDB: MongoDBConfig{URI: ""},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() should return error for empty URI")
	}
	if err.Error() != "MONGODB_URI is required" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestValidate_ValidURI(t *testing.T) {
	cfg := &Config{
		MongoDB: MongoDBConfig{URI: "mongodb://localhost:27017"},
	}

	err := cfg.Validate()
	if err != nil {
		t.Errorf("Validate() should not error for valid URI: %v", err)
	}
}

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		Service: ServiceConfig{
			Name:        "test",
			Environment: "dev",
			LogLevel:    "debug",
		},
		MongoDB: MongoDBConfig{
			URI:      "mongodb://localhost:27017",
			Database: "testdb",
		},
		Server: ServerConfig{
			Port: "8080",
		},
	}

	if cfg.Service.Name != "test" {
		t.Errorf("Service.Name = %q", cfg.Service.Name)
	}
	if cfg.MongoDB.URI != "mongodb://localhost:27017" {
		t.Errorf("MongoDB.URI = %q", cfg.MongoDB.URI)
	}
	if cfg.Server.Port != "8080" {
		t.Errorf("Server.Port = %q", cfg.Server.Port)
	}
}
