package apiserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpawnBackgroundJob_RunsToCompletion(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	var ran atomic.Bool
	done := make(chan struct{})
	if err := SpawnBackgroundJob(func(ctx context.Context) {
		ran.Store(true)
		close(done)
	}); err != nil {
		t.Fatalf("SpawnBackgroundJob returned %v, want nil", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran")
	}
	if !ran.Load() {
		t.Fatal("goroutine did not set the flag")
	}
}

func TestSpawnBackgroundJob_AfterShutdown_ReturnsError(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	shutdownBackgroundJobs(50 * time.Millisecond)

	err := SpawnBackgroundJob(func(ctx context.Context) {
		t.Fatal("goroutine should not have been spawned after shutdown")
	})
	if !errors.Is(err, ErrBackgroundJobsShutdown) {
		t.Fatalf("err = %v, want ErrBackgroundJobsShutdown", err)
	}
}

func TestShutdownBackgroundJobs_WaitsForRunningGoroutine(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	started := make(chan struct{})
	exited := atomic.Bool{}
	if err := SpawnBackgroundJob(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		// Simulate a brief terminal write before exit.
		time.Sleep(20 * time.Millisecond)
		exited.Store(true)
	}); err != nil {
		t.Fatalf("SpawnBackgroundJob returned %v, want nil", err)
	}
	<-started

	shutdownBackgroundJobs(2 * time.Second)
	if !exited.Load() {
		t.Fatal("shutdown returned before goroutine finished its cleanup")
	}
}

func TestShutdownBackgroundJobs_BoundedWait_TimesOut(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	if err := SpawnBackgroundJob(func(ctx context.Context) {
		// Ignore ctx — simulate a goroutine that won't finish in
		// time. The bounded wait must still return.
		<-time.After(5 * time.Second)
	}); err != nil {
		t.Fatalf("SpawnBackgroundJob returned %v, want nil", err)
	}

	start := time.Now()
	shutdownBackgroundJobs(100 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown took %v, expected to return within ~100ms budget", elapsed)
	}
}

func TestShutdownBackgroundJobs_IdempotentDoubleCall(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("double-shutdown panicked: %v", r)
		}
	}()
	shutdownBackgroundJobs(50 * time.Millisecond)
	shutdownBackgroundJobs(50 * time.Millisecond)
}

func TestSpawnBackgroundJob_ManyConcurrent(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	const n = 32
	var ran atomic.Int64
	for i := 0; i < n; i++ {
		if err := SpawnBackgroundJob(func(ctx context.Context) {
			ran.Add(1)
		}); err != nil {
			t.Fatalf("SpawnBackgroundJob #%d: %v", i, err)
		}
	}

	shutdownBackgroundJobs(2 * time.Second)
	if got := ran.Load(); got != n {
		t.Fatalf("ran = %d, want %d", got, n)
	}
}

func TestSpawnBackgroundJob_ContextCancelsOnShutdown(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	gotCancel := make(chan struct{})
	if err := SpawnBackgroundJob(func(ctx context.Context) {
		<-ctx.Done()
		close(gotCancel)
	}); err != nil {
		t.Fatalf("SpawnBackgroundJob returned %v, want nil", err)
	}

	go shutdownBackgroundJobs(2 * time.Second)
	select {
	case <-gotCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx never canceled on shutdown")
	}
}

// Verifies the spawn-during-shutdown race: shutdown sees the
// in-flight goroutine in its WaitGroup and waits for it. This
// matches what happens during graceful termination — an HTTP
// handler called SpawnBackgroundJob, returned 202, and exited
// just before srv.Shutdown returned.
func TestSpawnBackgroundJob_RaceWithShutdown_GoroutineCompletes(t *testing.T) {
	t.Cleanup(resetBackgroundJobsForTest)
	resetBackgroundJobsForTest()

	var wg sync.WaitGroup
	wg.Add(1)
	completed := atomic.Bool{}
	go func() {
		defer wg.Done()
		if err := SpawnBackgroundJob(func(ctx context.Context) {
			// Short job — shutdown should wait for this to land.
			time.Sleep(50 * time.Millisecond)
			completed.Store(true)
		}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()
	wg.Wait()

	shutdownBackgroundJobs(2 * time.Second)
	if !completed.Load() {
		t.Fatal("shutdown returned before spawned goroutine completed")
	}
}
