package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shimAgent puts a fake `decisionbox-agent` on PATH for this test.
//
// The subprocess runner resolves the agent by name, so a shim makes these
// tests spawn REAL processes the test controls — which is the only way to
// check that Cancel actually kills them. The shim branches on --resume so one
// script can play both halves of the race: a resumed attempt that keeps
// running, and a previous attempt that is on its way out.
func shimAgent(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--resume" ]; then
    # The live resumed attempt: stay up until something kills us.
    sleep 30
    exit 0
  fi
done
# The previous attempt, already exiting cleanly.
sleep 0.2
exit 0
`
	path := filepath.Join(dir, "decisionbox-agent")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test shim must be executable
		t.Fatalf("write agent shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// trackedAttempts reports which attempts of a run the runner is holding.
func trackedAttempts(r *SubprocessRunner, runID string) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int
	for attempt := range r.processes[runID] {
		out = append(out, attempt)
	}
	return out
}

// waitFor polls until cond holds, so the tests do not race the wait
// goroutines that do the deregistering.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSubprocessRunner_CancelKillsEveryLiveAttempt is the guarantee Cancel
// documents: a terminal hard-kill of the RUN, not of one of its attempts.
//
// A resume re-enters the same run id, and the previous attempt's process can
// still be alive — the agent writes its own `failed` status before it exits,
// so an operator resuming inside that window leaves two agents up. Keyed by
// run id alone, the runner held only the newer one and Cancel left the other
// running, writing checkpoints for a run the operator had just stopped.
func TestSubprocessRunner_CancelKillsEveryLiveAttempt(t *testing.T) {
	shimAgent(t)
	r := NewSubprocessRunner()
	ctx := context.Background()

	// Attempt 1 (a fresh run sends no Attempt at all, which means 1) and
	// attempt 2, both kept alive by --resume so neither exits on its own.
	for _, attempt := range []int{0, 2} {
		if err := r.Run(ctx, RunOptions{
			ProjectID: "p1",
			RunID:     "run-1",
			MaxSteps:  10,
			Resume:    true,
			Attempt:   attempt,
			OnFailure: func(string, string) {},
		}); err != nil {
			t.Fatalf("Run(attempt %d): %v", attempt, err)
		}
	}

	waitFor(t, "both attempts to be tracked", func() bool {
		return len(trackedAttempts(r, "run-1")) == 2
	})
	r.mu.Lock()
	first := r.processes["run-1"][1]
	second := r.processes["run-1"][2]
	r.mu.Unlock()
	if first == nil || second == nil {
		t.Fatalf("attempts tracked = %v, want both 1 and 2", trackedAttempts(r, "run-1"))
	}
	if first.Pid == second.Pid {
		t.Fatal("both attempts recorded the same process; the newer one overwrote the older")
	}

	if err := r.Cancel(ctx, "run-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// Both processes are gone, and the run is no longer tracked — which can
	// only happen once both wait goroutines have reaped their own entry.
	waitFor(t, "every attempt to be killed and deregistered", func() bool {
		return len(trackedAttempts(r, "run-1")) == 0
	})
}

// TestSubprocessRunner_AnExitingAttemptKeepsALaterOneTracked is the
// bookkeeping half of the same bug. The wait goroutine deleted by run id, so
// whichever attempt exited first deregistered the survivor — and Cancel then
// reported success, having found nothing, with an agent still running.
func TestSubprocessRunner_AnExitingAttemptKeepsALaterOneTracked(t *testing.T) {
	shimAgent(t)
	r := NewSubprocessRunner()
	ctx := context.Background()

	// Attempt 2 is the live resumed one; it stays up.
	if err := r.Run(ctx, RunOptions{
		ProjectID: "p1", RunID: "run-1", MaxSteps: 10,
		Resume: true, Attempt: 2, OnFailure: func(string, string) {},
	}); err != nil {
		t.Fatalf("Run(attempt 2): %v", err)
	}
	// Attempt 1 is the previous one, already exiting.
	if err := r.Run(ctx, RunOptions{
		ProjectID: "p1", RunID: "run-1", MaxSteps: 10,
		Attempt: 1, OnFailure: func(string, string) {},
	}); err != nil {
		t.Fatalf("Run(attempt 1): %v", err)
	}

	// Once attempt 1's wait goroutine has run, attempt 2 must still be there.
	waitFor(t, "the exiting attempt to deregister itself", func() bool {
		got := trackedAttempts(r, "run-1")
		return len(got) == 1 && got[0] == 2
	})

	// And Cancel still reaches it.
	if err := r.Cancel(ctx, "run-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitFor(t, "the live attempt to be killed", func() bool {
		return len(trackedAttempts(r, "run-1")) == 0
	})
}

// TestSubprocessRunner_CancelOnAnUnknownRunIsNotAnError keeps the behaviour
// callers rely on to tell a cancelled-while-running run from one that had
// already exited.
func TestSubprocessRunner_CancelOnAnUnknownRunIsNotAnError(t *testing.T) {
	r := NewSubprocessRunner()
	if err := r.Cancel(context.Background(), "run-nobody-started"); err != nil {
		t.Errorf("Cancel on an unknown run = %v, want nil", err)
	}
}
