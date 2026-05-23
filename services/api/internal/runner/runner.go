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
	"strings"
	"time"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
)

// Runner spawns and manages agent processes for discovery runs.
type Runner interface {
	Run(ctx context.Context, opts RunOptions) error
	RunSync(ctx context.Context, opts RunSyncOptions) (*RunSyncResult, error)
	Cancel(ctx context.Context, runID string) error
	// RunIndexSchema synchronously runs the schema-indexing mode of the
	// agent. Returns a non-nil error only when the agent process exits
	// non-zero or fails to start; progress is surfaced out-of-band
	// through the project_schema_index_progress collection. Used by the
	// API's indexing worker loop — the worker owns the project lifecycle
	// status transitions around this call.
	RunIndexSchema(ctx context.Context, opts IndexSchemaOptions) error
}

// IndexSchemaOptions configures a schema-indexing agent invocation.
type IndexSchemaOptions struct {
	ProjectID string
	RunID     string // logical run identifier stamped into the progress doc
	// OnLogLine, when non-nil, is called with each raw stderr line
	// emitted by the agent in real time. Used to fan the live tail out
	// to MongoDB (so the dashboard can render an in-UI log viewer) in
	// addition to the API's own stderr. Must not block — the scanner
	// goroutine calls it synchronously; callbacks that need IO should
	// queue the work and return fast.
	OnLogLine func(line string)
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
	// Default: 25 hours, paired with the agent's 24h
	// DISCOVERY_MAX_DURATION default so the in-agent cap fires first
	// and the agent fails gracefully through its persistence tail
	// rather than being hard-killed by the wall-clock watcher.
	JobTimeoutHours int
}

// defaultJobTimeoutHours pairs with the agent's
// defaultDiscoveryMaxDuration (24h). The runner needs at least 1h
// headroom above the in-agent cap so the agent fails gracefully
// through its persistence tail (10-minute persist budget) instead of
// being hard-killed by ActiveDeadlineSeconds / the subprocess
// watcher giving up. Raised from the historical 6h paired with the
// old 2h hardcoded cap.
const defaultJobTimeoutHours = 25

// LoadConfig loads runner configuration from environment variables.
func LoadConfig() Config {
	timeoutHours, _ := strconv.Atoi(getEnv("AGENT_JOB_TIMEOUT_HOURS", strconv.Itoa(defaultJobTimeoutHours)))
	if timeoutHours <= 0 {
		timeoutHours = defaultJobTimeoutHours
	}

	warnIfDiscoveryCapShadowed(timeoutHours)

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

// agentDefaultDiscoveryMaxDuration mirrors the agent's
// defaultDiscoveryMaxDuration. Duplicated here so the API process
// can warn about a shadowed configuration without taking a build
// dependency on the agent package. If the agent's default changes,
// keep this in sync.
const agentDefaultDiscoveryMaxDuration = 24 * time.Hour

// agentTerminalTailHeadroom mirrors the agent's persistTimeout
// (10m for Save / split logs / embed-index) plus completeTimeout
// (30s for the final Complete/Fail UpdateOne). Both run
// sequentially after compute finishes, so the wall-clock budget
// the API enforces must cover the effective in-agent cap PLUS
// this total — otherwise the kubelet / subprocess watcher fires
// during the persistence tail or during the terminal status
// write and defeats the partial-save design. Keep in sync with
// the agent's persistTimeout and completeTimeout if they change.
const agentTerminalTailHeadroom = 10*time.Minute + 30*time.Second

// warnIfDiscoveryCapShadowed surfaces a startup warning when the
// EFFECTIVE in-agent cap is >= the API's wall-clock budget. In that
// configuration the wall-clock kill (K8s ActiveDeadlineSeconds or
// subprocess watcher) fires first and the in-agent cap never takes
// effect — the agent gets hard-killed mid-write instead of failing
// gracefully through the persistence tail.
//
// Uses the effective cap (env value if valid, else the agent's
// default) rather than only an explicit env-var setting. Without
// that, the most common shadowed config — operator left an old
// AGENT_JOB_TIMEOUT_HOURS=6 override in place but never set
// DISCOVERY_MAX_DURATION — silently slips through this guard.
//
// We don't error out (a running install with a stale setting
// should keep working) but log loudly enough that the next deploy
// notices.
func warnIfDiscoveryCapShadowed(jobTimeoutHours int) {
	raw := strings.TrimSpace(os.Getenv("DISCOVERY_MAX_DURATION"))
	effective := agentDefaultDiscoveryMaxDuration
	source := "default (env unset)"

	if raw != "" {
		parsed, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			source = "default (env invalid)"
		case parsed == 0:
			// Operator explicitly disabled the in-agent cap. The
			// agent will run unbounded; the wall-clock kill at
			// jobTimeoutHours is the only ceiling. That's a
			// different configuration choice from "shadowed" so
			// we don't warn.
			return
		case parsed < 0:
			source = "default (env negative)"
		default:
			effective = parsed
			source = "DISCOVERY_MAX_DURATION"
		}
	}

	jobBudget := time.Duration(jobTimeoutHours) * time.Hour

	// Require the wall-clock budget to STRICTLY exceed the effective
	// cap plus the agent's full terminal-tail headroom (persistTimeout
	// + completeTimeout). `>=` rather than `>` because the exact-equal
	// case still leaves zero margin for clock skew, scheduler jitter,
	// and the K8s Job controller's own deadline-observation latency —
	// any one of those bites the terminal status write.
	required := effective + agentTerminalTailHeadroom
	if required >= jobBudget {
		apilog.WithFields(apilog.Fields{
			"effective_discovery_cap":        effective.String(),
			"effective_discovery_cap_source": source,
			"agent_job_timeout_hours":        jobTimeoutHours,
			"effective_wall_clock_kill":      jobBudget.String(),
			"required_minimum":               required.String(),
			"terminal_tail_headroom":         agentTerminalTailHeadroom.String(),
		}).Warn("AGENT_JOB_TIMEOUT_HOURS does not leave room for the agent's terminal tail (persist + Complete/Fail) above the effective DISCOVERY_MAX_DURATION — the wall-clock kill can fire during partial-save or the final status write, defeating the partial-result-on-cancellation contract. Raise AGENT_JOB_TIMEOUT_HOURS so it strictly exceeds the effective cap PLUS persistTimeout + completeTimeout.")
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
