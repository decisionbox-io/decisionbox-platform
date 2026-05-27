package apiserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBackgroundJobsShutdown is returned by SpawnBackgroundJob after
// the apiserver has entered shutdown. Callers MUST handle this:
// typically they refuse the inbound request with 503 and let the
// client retry once a new pod is up. Silently swallowing the error
// would race the Mongo disconnect — any work the goroutine writes
// after shutdownBackgroundJobs returns can land in a half-closed
// process.
var ErrBackgroundJobsShutdown = errors.New("apiserver: background jobs registry is shutting down")

// SpawnBackgroundJob runs fn on a new goroutine whose lifetime is
// tied to the API process — NOT to any inbound HTTP request. The
// goroutine is registered with the apiserver's shutdown drain so
// Mongo and other shared resources stay available while fn
// completes its cleanup writes. fn should observe ctx.Done() and
// return promptly when the apiserver enters shutdown.
//
// Returns ErrBackgroundJobsShutdown if shutdown has already begun
// (the drain is past srv.Shutdown and into the WaitGroup.Wait phase
// or beyond). When this happens the goroutine is NOT spawned;
// callers should propagate the error to the HTTP layer.
//
// Typical pattern from a plugin HTTP handler:
//
//	if err := apiserver.SpawnBackgroundJob(func(ctx context.Context) {
//	    doLongWork(ctx, …)
//	}); err != nil {
//	    writeError(w, http.StatusServiceUnavailable, err.Error())
//	    return
//	}
//	// Only on success do we commit the durable launch marker /
//	// return 202 to the caller.
//
// The shutdown drain has a bounded budget (15 s by default).
// Goroutines that don't return within the budget are abandoned;
// any in-flight Mongo write past that point may race the Mongo
// disconnect. fn should size its work and revert paths so its
// cleanup fits comfortably within the budget.
func SpawnBackgroundJob(fn func(ctx context.Context)) error {
	bg := backgroundJobs()

	bg.mu.RLock()
	if bg.shuttingDown.Load() {
		bg.mu.RUnlock()
		return ErrBackgroundJobsShutdown
	}
	bg.wg.Add(1)
	bg.mu.RUnlock()

	go func() {
		defer bg.wg.Done()
		fn(bg.ctx)
	}()
	return nil
}

// shutdownBackgroundJobs is called by Run during shutdown. Cancels
// the background context, then bounded-waits for all spawned
// goroutines to return. Idempotent — calling twice is a no-op.
//
// Order in Run():
//  1. srv.Shutdown — HTTP drain (refuses new requests, waits for
//     handlers).
//  2. shutdownBackgroundJobs — cancel + bounded wait.
//  3. (deferred) Mongo disconnect.
//
// HTTP drain MUST run first so no new inbound request can call
// SpawnBackgroundJob between the shuttingDown.Store(true) below and
// the WaitGroup.Wait. After srv.Shutdown returns, only goroutines
// spawned during the brief race window (handler past the spawn call
// but pre-WaitGroup.Done) remain — they finish under the drain
// budget.
func shutdownBackgroundJobs(timeout time.Duration) {
	bg := backgroundJobs()

	bg.mu.Lock()
	if bg.shuttingDown.Swap(true) {
		bg.mu.Unlock()
		return
	}
	if bg.cancel != nil {
		bg.cancel()
	}
	bg.mu.Unlock()

	done := make(chan struct{})
	go func() {
		bg.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// resetBackgroundJobsForTest tears down the singleton so each test
// gets a fresh registry. Tests MUST call this in t.Cleanup; not
// intended for production code.
func resetBackgroundJobsForTest() {
	bgOnce = sync.Once{}
	bgSingleton.Store((*backgroundJobsState)(nil))
}

type backgroundJobsState struct {
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.RWMutex
	shuttingDown atomic.Bool
}

var (
	bgOnce      sync.Once
	bgSingleton atomic.Pointer[backgroundJobsState]
)

// backgroundJobs returns the process-wide registry, constructing it
// on first call. Safe to call from init() — the ctx is rooted in
// context.Background() and isn't tied to apiserver.Run's lifetime.
//
// The cancel func is stashed on the singleton and invoked exactly
// once from shutdownBackgroundJobs (guarded by the same atomic flag
// that gates spawn). gosec's G118 can't follow the indirection;
// nolint with this rationale is intentional.
func backgroundJobs() *backgroundJobsState {
	bgOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel invoked from shutdownBackgroundJobs
		state := &backgroundJobsState{
			ctx:    ctx,
			cancel: cancel,
		}
		bgSingleton.Store(state)
	})
	return bgSingleton.Load()
}
