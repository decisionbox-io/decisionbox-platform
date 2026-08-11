package askserve

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingBuilder returns a runtime whose Close bumps a shared counter, and
// records how many times the builder itself ran.
func countingBuilder(builds, closes *int32) ProjectBuilder {
	return func(ctx context.Context, projectID string) (*ProjectRuntime, error) {
		atomic.AddInt32(builds, 1)
		return NewProjectRuntime(ProjectRuntimeOptions{
			Model:         "m",
			SharedClosers: []func() error{func() error { atomic.AddInt32(closes, 1); return nil }},
		}), nil
	}
}

func TestPool_ReusesEntry(t *testing.T) {
	var builds, closes int32
	p := newPool(countingBuilder(&builds, &closes), Config{PoolMaxProjects: 10, ConnectTimeout: time.Second})
	defer p.closeAll()

	for i := 0; i < 3; i++ {
		rt, release, err := p.acquire(context.Background(), "proj")
		if err != nil || rt == nil {
			t.Fatalf("acquire: %v", err)
		}
		release()
	}
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("builds = %d, want 1 (entry reused)", got)
	}
}

func TestPool_ConcurrentSameProjectSharesBuild(t *testing.T) {
	var builds, closes int32
	p := newPool(countingBuilder(&builds, &closes), Config{PoolMaxProjects: 10, ConnectTimeout: time.Second})
	defer p.closeAll()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt, release, err := p.acquire(context.Background(), "proj")
			if err != nil || rt == nil {
				t.Errorf("acquire: %v", err)
				return
			}
			release()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("builds = %d, want 1 (concurrent callers share one build)", got)
	}
}

func TestPool_LRUEvictsIdleAtCapacity(t *testing.T) {
	var builds, closes int32
	p := newPool(countingBuilder(&builds, &closes), Config{PoolMaxProjects: 1, ConnectTimeout: time.Second})
	defer p.closeAll()

	_, relA, err := p.acquire(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	relA() // A now idle

	_, relB, err := p.acquire(context.Background(), "B") // over capacity → evict A
	if err != nil {
		t.Fatal(err)
	}
	relB()

	// Give the async closeRuntime goroutine a moment.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&closes) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&closes) == 0 {
		t.Fatal("expected LRU eviction to close project A")
	}
}

func TestPool_DoesNotEvictInUse(t *testing.T) {
	var builds, closes int32
	p := newPool(countingBuilder(&builds, &closes), Config{PoolMaxProjects: 1, ConnectTimeout: time.Second})
	defer p.closeAll()

	_, relA, err := p.acquire(context.Background(), "A") // A held (in use)
	if err != nil {
		t.Fatal(err)
	}
	defer relA()

	_, relB, err := p.acquire(context.Background(), "B") // capacity hit, but A is in use
	if err != nil {
		t.Fatal(err)
	}
	defer relB()

	if atomic.LoadInt32(&closes) != 0 {
		t.Fatal("must not evict an in-use entry")
	}
}

func TestPool_IdleEviction(t *testing.T) {
	var builds, closes int32
	p := newPool(countingBuilder(&builds, &closes), Config{PoolMaxProjects: 10, PoolIdleTTL: 10 * time.Millisecond, ConnectTimeout: time.Second})
	defer p.closeAll()

	_, release, err := p.acquire(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	release()

	time.Sleep(20 * time.Millisecond)
	p.evictIdle()
	if atomic.LoadInt32(&closes) == 0 {
		t.Fatal("expected idle eviction to close the entry")
	}
}

func TestPool_BuildErrorRetries(t *testing.T) {
	var attempts int32
	builder := func(ctx context.Context, projectID string) (*ProjectRuntime, error) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			return nil, errors.New("transient connect failure")
		}
		return NewProjectRuntime(ProjectRuntimeOptions{Model: "m"}), nil
	}
	p := newPool(builder, Config{PoolMaxProjects: 10, ConnectTimeout: time.Second})
	defer p.closeAll()

	if _, _, err := p.acquire(context.Background(), "proj"); err == nil {
		t.Fatal("expected first acquire to fail")
	}
	rt, release, err := p.acquire(context.Background(), "proj")
	if err != nil || rt == nil {
		t.Fatalf("retry after failure should succeed: %v", err)
	}
	release()
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2 (failed entry was dropped and rebuilt)", got)
	}
}
