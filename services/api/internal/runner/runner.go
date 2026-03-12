// Package runner provides an abstraction for spawning discovery agent processes.
// Supports subprocess mode (local dev) and Kubernetes Jobs (production).
//
// Selection via RUNNER_MODE env var:
//   - "subprocess" (default): exec.Command, agent binary must be in PATH
//   - "kubernetes": creates K8s Job per discovery run
package runner

import (
	"context"
	"fmt"
	"os"
)

// Runner spawns and manages agent processes for discovery runs.
type Runner interface {
	Run(ctx context.Context, opts RunOptions) error
	Cancel(ctx context.Context, runID string) error
}

// RunOptions configures a discovery agent run.
type RunOptions struct {
	ProjectID string
	RunID     string
	Areas     []string // optional: selective discovery
	MaxSteps  int      // optional: override default
}

// Config holds runner configuration from environment variables.
type Config struct {
	Mode string // "subprocess" or "kubernetes"

	// Kubernetes mode settings
	AgentImage     string
	Namespace      string
	CPURequest     string
	CPULimit       string
	MemoryRequest  string
	MemoryLimit    string
}

// LoadConfig loads runner configuration from environment variables.
func LoadConfig() Config {
	return Config{
		Mode:          getEnv("RUNNER_MODE", "subprocess"),
		AgentImage:    getEnv("AGENT_IMAGE", "ghcr.io/decisionbox-io/decisionbox-agent:latest"),
		Namespace:     getEnv("AGENT_NAMESPACE", "default"),
		CPURequest:    getEnv("AGENT_CPU_REQUEST", "250m"),
		CPULimit:      getEnv("AGENT_CPU_LIMIT", "2"),
		MemoryRequest: getEnv("AGENT_MEMORY_REQUEST", "256Mi"),
		MemoryLimit:   getEnv("AGENT_MEMORY_LIMIT", "1Gi"),
	}
}

// New creates a Runner based on the configuration.
func New(cfg Config) (Runner, error) {
	switch cfg.Mode {
	case "subprocess", "":
		return NewSubprocessRunner(), nil
	case "kubernetes":
		return NewKubernetesRunner(cfg)
	default:
		return nil, fmt.Errorf("unknown RUNNER_MODE: %q (use 'subprocess' or 'kubernetes')", cfg.Mode)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
