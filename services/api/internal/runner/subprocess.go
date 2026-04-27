package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
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

// RunIndexSchema executes the agent in schema-indexing mode and blocks
// until it exits. Unlike Run (which backgrounds the agent and reports
// failure via a callback), indexing runs are owned by the single-node
// worker loop in the API — it needs the exit code synchronously to
// decide whether to flip the project's schema_index_status to ready
// or failed.
func (r *SubprocessRunner) RunIndexSchema(ctx context.Context, opts IndexSchemaOptions) error {
	args := []string{
		"--mode", "index-schema",
		"--project-id", opts.ProjectID,
	}
	if opts.RunID != "" {
		args = append(args, "--run-id", opts.RunID)
	}

	cmd := exec.CommandContext(ctx, "decisionbox-agent", args...) //nolint:gosec // controlled binary name
	cmd.Env = append(os.Environ(),
		"MONGODB_URI="+getEnv("MONGODB_URI", "mongodb://localhost:27017"),
		"MONGODB_DB="+getEnv("MONGODB_DB", "decisionbox"),
	)

	// Agent writes structured logs to stderr. We want two things
	// simultaneously:
	//   1. Live-tail the output to the API stderr so operators watching
	//      /tmp/dbx-schema-retrieval/api.log see progress in real time
	//      (buffering until exit means a hung run shows nothing).
	//   2. Keep the full stderr in a byte buffer so `extractErrorMessage`
	//      can still surface a human-readable failure when the agent
	//      exits non-zero.
	// The tee writer below does both — each line is forked to the API's
	// os.Stderr (prefixed so it's obvious which subprocess it came from)
	// and to the tail-capture buffer.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("agent stderr pipe: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"project_id": opts.ProjectID,
		"run_id":     opts.RunID,
	}).Info("Agent index-schema subprocess starting")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agent start: %w", err)
	}

	// Rolling tail buffer. Cap at 64 KiB so a runaway log output doesn't
	// blow API memory while still holding enough context for the
	// extractErrorMessage call post-exit.
	const tailCap = 64 * 1024
	var tail bytes.Buffer
	tail.Grow(tailCap)

	// Single goroutine consumes the pipe, forwards each line to API
	// stderr (visible in /tmp/.../api.log) and pushes to the tail buffer
	// with ring-style trimming.
	prefix := fmt.Sprintf("[agent %s] ", opts.RunID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stderrPipe)
		// Bump buffer for long zap JSON log lines (default is 64KiB).
		scanner.Buffer(make([]byte, 0, 128*1024), 512*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			_, _ = io.WriteString(os.Stderr, prefix)
			_, _ = os.Stderr.Write(line)
			_, _ = os.Stderr.Write([]byte{'\n'})
			// Ring-style append with soft cap.
			if tail.Len()+len(line)+1 > tailCap {
				tail.Reset()
			}
			tail.Write(line)
			tail.WriteByte('\n')
			if opts.OnLogLine != nil {
				// Copy out of scanner's internal buffer — scanner
				// reuses the byte slice across iterations, so handing
				// the raw []byte to an async consumer would tear.
				opts.OnLogLine(string(line))
			}
		}
	}()

	waitErr := cmd.Wait()
	<-done

	if waitErr != nil {
		msg := extractErrorMessage(tail.String(), waitErr)
		apilog.WithFields(apilog.Fields{
			"project_id": opts.ProjectID,
			"run_id":     opts.RunID,
			"error":      msg,
		}).Warn("Agent index-schema subprocess failed")
		return fmt.Errorf("agent --mode index-schema: %s", msg)
	}
	apilog.WithFields(apilog.Fields{
		"project_id": opts.ProjectID,
		"run_id":     opts.RunID,
	}).Info("Agent index-schema subprocess completed")
	return nil
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
