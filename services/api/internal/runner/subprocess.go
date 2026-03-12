package runner

import (
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

	cmd := exec.Command("decisionbox-agent", args...)
	cmd.Env = append(os.Environ(),
		"MONGODB_URI="+getEnv("MONGODB_URI", "mongodb://localhost:27017"),
		"MONGODB_DB="+getEnv("MONGODB_DB", "decisionbox"),
	)

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

	// Wait in background, clean up when done
	go func() {
		err := cmd.Wait()
		r.mu.Lock()
		delete(r.processes, opts.RunID)
		r.mu.Unlock()

		if err != nil {
			apilog.WithFields(apilog.Fields{
				"run_id": opts.RunID, "error": err.Error(),
			}).Warn("Agent subprocess exited with error")
		} else {
			apilog.WithField("run_id", opts.RunID).Info("Agent subprocess completed")
		}
	}()

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
