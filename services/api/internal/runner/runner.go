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
	"strconv"
)

// Runner spawns and manages agent processes for discovery runs.
type Runner interface {
	Run(ctx context.Context, opts RunOptions) error
	RunSync(ctx context.Context, opts RunSyncOptions) (*RunSyncResult, error)
	Cancel(ctx context.Context, runID string) error
}

// RunSyncOptions configures a synchronous agent invocation (e.g., test-connection).
type RunSyncOptions struct {
	ProjectID string
	Args      []string // additional CLI args (e.g., "--test-connection", "warehouse")
}

// RunSyncResult holds the output of a synchronous agent invocation.
type RunSyncResult struct {
	Output []byte // stdout
	Error  string // stderr summary
}

// RunOptions configures a discovery agent run.
type RunOptions struct {
	ProjectID string
	RunID     string
	Areas     []string // optional: selective discovery
	MaxSteps  int      // optional: override default
	// MinSteps is a floor on exploration steps — premature "done" signals
	// below this value are rejected and exploration continues. The handler
	// layer defaults this to 60% of MaxSteps when the request omits it; the
	// runner layer forwards whatever it receives unchanged so zero means
	// "no floor" (explicitly disabled by the caller).
	MinSteps int

	// OnFailure is called when the agent process exits with an error.
	// The runner passes the error message so the caller can update the run status.
	OnFailure func(runID string, errMsg string)
}

// Config holds runner configuration from environment variables.
type Config struct {
	Mode string // "subprocess" or "kubernetes"

	// Kubernetes mode settings
	AgentImage         string
	Namespace          string
	ServiceAccountName string
	CPURequest         string
	CPULimit           string
	MemoryRequest      string
	MemoryLimit        string

	// Job timeout in hours — how long to watch a Job before giving up.
	// Applies to both K8s Job watching and subprocess waiting.
	// Default: 6 hours.
	JobTimeoutHours int
}

// LoadConfig loads runner configuration from environment variables.
func LoadConfig() Config {
	timeoutHours, _ := strconv.Atoi(getEnv("AGENT_JOB_TIMEOUT_HOURS", "6"))
	if timeoutHours <= 0 {
		timeoutHours = 6
	}

	return Config{
		Mode:            getEnv("RUNNER_MODE", "subprocess"),
		AgentImage:         getEnv("AGENT_IMAGE", "ghcr.io/decisionbox-io/decisionbox-agent:latest"),
		Namespace:          getEnv("AGENT_NAMESPACE", "default"),
		ServiceAccountName: getEnv("AGENT_SERVICE_ACCOUNT", ""),
		CPURequest:      getEnv("AGENT_CPU_REQUEST", "250m"),
		CPULimit:        getEnv("AGENT_CPU_LIMIT", "2"),
		MemoryRequest:   getEnv("AGENT_MEMORY_REQUEST", "256Mi"),
		MemoryLimit:     getEnv("AGENT_MEMORY_LIMIT", "1Gi"),
		JobTimeoutHours: timeoutHours,
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
