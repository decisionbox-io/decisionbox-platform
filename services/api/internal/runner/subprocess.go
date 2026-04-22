package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
)

// SubprocessRunner spawns the agent as a local subprocess.
// Default mode for local development — agent binary must be in PATH.
type SubprocessRunner struct {
	mu        sync.Mutex
	processes map[string]*os.Process // runID → process
}

func NewSubprocessRunner() *SubprocessRunner {
	apilog.Info("Runner mode: subprocess (local dev)")
	return &SubprocessRunner{
		processes: make(map[string]*os.Process),
	}
}

func (r *SubprocessRunner) Run(ctx context.Context, opts RunOptions) error {
	args := []string{
		"--project-id", opts.ProjectID,
		"--run-id", opts.RunID,
	}
	if len(opts.Areas) > 0 {
		args = append(args, "--areas", strings.Join(opts.Areas, ","))
	}
	if opts.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(opts.MaxSteps))
	}
	// MinSteps forwards as-is: zero means "no floor, disabled" (either the
	// caller explicitly set it to 0 or the handler defaulted an old client
	// request with max_steps<=0). The agent CLI also clamps defensively.
	if opts.MinSteps > 0 {
		args = append(args, "--min-steps", strconv.Itoa(opts.MinSteps))
	}

	cmd := exec.Command("decisionbox-agent", args...) //nolint:gosec // controlled binary name
	cmd.Env = append(os.Environ(),
		"MONGODB_URI="+getEnv("MONGODB_URI", "mongodb://localhost:27017"),
		"MONGODB_DB="+getEnv("MONGODB_DB", "decisionbox"),
	)

	// Capture stderr to get error messages from the agent
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		apilog.WithFields(apilog.Fields{
			"run_id": opts.RunID, "error": err.Error(),
		}).Error("Failed to start agent subprocess")
		return err
	}

	apilog.WithFields(apilog.Fields{
		"run_id":     opts.RunID,
		"project_id": opts.ProjectID,
		"pid":        cmd.Process.Pid,
		"areas":      opts.Areas,
		"max_steps":  opts.MaxSteps,
	}).Info("Agent subprocess started")

	r.mu.Lock()
	r.processes[opts.RunID] = cmd.Process
	r.mu.Unlock()

	// Wait in background, handle failure
	go func() {
		err := cmd.Wait()
		r.mu.Lock()
		delete(r.processes, opts.RunID)
		r.mu.Unlock()

		if err != nil {
			errMsg := extractErrorMessage(stderr.String(), err)
			apilog.WithFields(apilog.Fields{
				"run_id": opts.RunID, "error": errMsg,
			}).Warn("Agent subprocess exited with error")

			if opts.OnFailure != nil {
				opts.OnFailure(opts.RunID, errMsg)
			}
		} else {
			apilog.WithField("run_id", opts.RunID).Info("Agent subprocess completed")
		}
	}()

	return nil
}

func (r *SubprocessRunner) RunSync(ctx context.Context, opts RunSyncOptions) (*RunSyncResult, error) {
	args := append([]string{"--project-id", opts.ProjectID}, opts.Args...)

	cmd := exec.CommandContext(ctx, "decisionbox-agent", args...) //nolint:gosec // controlled binary name
	cmd.Env = append(os.Environ(),
		"MONGODB_URI="+getEnv("MONGODB_URI", "mongodb://localhost:27017"),
		"MONGODB_DB="+getEnv("MONGODB_DB", "decisionbox"),
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		return &RunSyncResult{
			Output: output,
			Error:  extractErrorMessage(stderr.String(), err),
		}, err
	}

	return &RunSyncResult{Output: output}, nil
}

func (r *SubprocessRunner) Cancel(ctx context.Context, runID string) error {
	r.mu.Lock()
	proc, ok := r.processes[runID]
	r.mu.Unlock()

	if !ok {
		return nil // not running (already finished or never started)
	}

	apilog.WithField("run_id", runID).Info("Killing agent subprocess")
	return proc.Kill()
}

// extractErrorMessage gets a user-friendly error message from agent stderr output.
// The agent logs structured JSON to stderr — we look for the last FATAL or ERROR line.
func extractErrorMessage(stderr string, exitErr error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")

	// Walk backwards to find the most relevant error
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		// Look for FATAL log lines (agent uses zap which outputs "FATAL" in the level field)
		if strings.Contains(line, "FATAL") || strings.Contains(line, "\"level\":\"fatal\"") {
			// Try to extract the message field
			if msg := extractJSONField(line, "error"); msg != "" {
				return msg
			}
			if msg := extractJSONField(line, "msg"); msg != "" {
				return msg
			}
		}
		// Also check ERROR lines
		if strings.Contains(line, "ERROR") || strings.Contains(line, "\"level\":\"error\"") {
			if msg := extractJSONField(line, "error"); msg != "" {
				return msg
			}
		}
	}

	// Fallback: last non-empty line of stderr
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			line := lines[i]
			// Truncate if too long
			if len(line) > 200 {
				line = line[:200] + "..."
			}
			return line
		}
	}

	return exitErr.Error()
}

// extractJSONField tries to extract a field value from a JSON-ish log line.
func extractJSONField(line, field string) string {
	// Look for "field":"value" or "field": "value"
	key := `"` + field + `"`
	idx := strings.Index(line, key)
	if idx < 0 {
		return ""
	}

	rest := line[idx+len(key):]
	// Skip ": or ":"
	rest = strings.TrimLeft(rest, ": ")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:] // skip opening quote

	// Find closing quote (handle escaped quotes)
	var result strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\\' && i+1 < len(rest) {
			result.WriteByte(rest[i+1])
			i++
			continue
		}
		if rest[i] == '"' {
			break
		}
		result.WriteByte(rest[i])
	}
	return result.String()
}
