package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BaseConfig contains common configuration fields shared across all services.
// Services embed this struct and add their own fields.
type BaseConfig struct {
	Env      string `envconfig:"ENV" default:"dev"`
	Port     int    `envconfig:"PORT" default:"8080"`
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
	MongoURI string `envconfig:"MONGODB_URI" required:"true"`
	MongoDB  string `envconfig:"MONGODB_DB" required:"true"`
}

// GetEnv reads an environment variable, supporting file:// prefix for K8s secret mounts.
// If the value starts with "file://", reads the file contents instead.
func GetEnv(key string) string {
	val := os.Getenv(key)
	if strings.HasPrefix(val, "file://") {
		path := strings.TrimPrefix(val, "file://")
		data, err := os.ReadFile(path) //nolint:gosec // path comes from trusted env var
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	return val
}

// GetEnvOrDefault reads an environment variable with a default fallback.
func GetEnvOrDefault(key, defaultValue string) string {
	val := GetEnv(key)
	if val == "" {
		return defaultValue
	}
	return val
}

// GetEnvAsInt reads an environment variable as an integer.
func GetEnvAsInt(key string, defaultValue int) int {
	val := GetEnv(key)
	if val == "" {
		return defaultValue
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return defaultValue
	}
	return i
}

// GetEnvAsBool reads an environment variable as a boolean.
func GetEnvAsBool(key string, defaultValue bool) bool {
	val := GetEnv(key)
	if val == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return defaultValue
	}
	return b
}

// GetEnvAsDuration reads an environment variable as a time.Duration.
func GetEnvAsDuration(key string, defaultValue time.Duration) time.Duration {
	val := GetEnv(key)
	if val == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return defaultValue
	}
	return d
}

// GetEnvAsSlice reads an environment variable as a string slice, split by separator.
func GetEnvAsSlice(key, separator string) []string {
	val := GetEnv(key)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, separator)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// MustGetEnv reads an environment variable and panics if not set.
func MustGetEnv(key string) string {
	val := GetEnv(key)
	if val == "" {
		panic(fmt.Sprintf("required environment variable %s is not set", key))
	}
	return val
}
