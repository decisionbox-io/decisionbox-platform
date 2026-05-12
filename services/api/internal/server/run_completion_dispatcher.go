package server

import (
	"context"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runhooks"
)

// runCompletionDispatcherInterval is how often the dispatcher scans for
// terminal runs that still need their completion hooks fired. Mirrors
// the policy confirmer's cadence — a short tick keeps the dashboard
// responsive (the executive-summary banner switches from "generating"
// to "ready" within the same window) without spamming Mongo when no
// runs are pending.
const runCompletionDispatcherInterval = 15 * time.Second

// runCompletionDispatcherBatch caps the number of runs fired per tick.
// Bounded so a sudden backlog (e.g. after an API restart) does not
// monopolise a Mongo connection or starve the policy confirmer.
const runCompletionDispatcherBatch = 50

// startRunCompletionDispatcher spawns a background goroutine that ticks
// every runCompletionDispatcherInterval, fires every registered run
// completion hook (plugin-hooks.md Hook 5) for any terminal run whose
// hooks have not yet been fired, and marks the run dispatched on
// success.
//
// The dispatcher exits when ctx is cancelled. The server passes a
// process-lifetime context today; future shutdown wiring will plumb a
// cancellable one through.
func startRunCompletionDispatcher(ctx context.Context, runRepo database.RunRepo) {
	startRunCompletionDispatcherWithInterval(ctx, runRepo, runCompletionDispatcherInterval)
}

// startRunCompletionDispatcherWithInterval is the parameterised entry
// point. Production code uses the const-based wrapper; tests override
// the interval to drive the ticker branch without sleeping the full
// 15-second cadence.
func startRunCompletionDispatcherWithInterval(ctx context.Context, runRepo database.RunRepo, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		dispatchTerminalRuns(ctx, runRepo)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				dispatchTerminalRuns(ctx, runRepo)
			}
		}
	}()
}

// dispatchTerminalRuns is one tick of the dispatcher. Exported via
// startRunCompletionDispatcher; broken out so tests can drive a single
// tick without spinning the ticker.
func dispatchTerminalRuns(ctx context.Context, runRepo database.RunRepo) {
	runs, err := runRepo.ListTerminalWithoutCompletionHook(ctx, runCompletionDispatcherBatch)
	if err != nil {
		apilog.WithError(err).Warn("run completion dispatcher: list terminal runs failed")
		return
	}
	if len(runs) == 0 {
		return
	}
	for _, run := range runs {
		completion := runhooks.RunCompletion{
			RunID:       run.ID,
			DiscoveryID: run.DiscoveryID,
			ProjectID:   run.ProjectID,
			Status:      run.Status,
			Error:     run.Error,
		}
		if run.CompletedAt != nil {
			completion.CompletedAt = *run.CompletedAt
		}

		results := runhooks.Fire(ctx, completion)
		anyFailed := false
		for _, res := range results {
			if res.Err == nil {
				continue
			}
			anyFailed = true
			apilog.WithFields(apilog.Fields{
				"run_id": run.ID,
				"hook":   res.Name,
				"error":  res.Err.Error(),
			}).Warn("run completion hook failed; will retry on next tick")
		}
		if anyFailed {
			// At least one hook failed — leave completion_hooks_fired_at
			// unset so the next tick re-fires every hook for this run.
			// Hooks are documented as idempotent precisely so a peer
			// failure doesn't cause double-side-effects for the peers
			// that already succeeded.
			continue
		}
		if err := runRepo.MarkCompletionHooksFired(ctx, run.ID); err != nil {
			apilog.WithFields(apilog.Fields{
				"run_id": run.ID,
				"error":  err.Error(),
			}).Warn("run completion dispatcher: mark fired failed; hooks may re-fire on next tick")
		}
	}
}
