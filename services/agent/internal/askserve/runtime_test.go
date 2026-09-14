package askserve

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// --- per-datasource lazy connection cache (Risk R3) ---------------------------

// countingConnBuilder returns a WarehouseBuilder that counts builds per
// datasource and closes bump a per-datasource counter, so tests can assert the
// lazy-build + LRU behaviour.
func countingConnBuilder(builds, closes map[string]*int32) WarehouseBuilder {
	return func(_ context.Context, id string) (*WarehouseConn, error) {
		if c := builds[id]; c != nil {
			atomic.AddInt32(c, 1)
		}
		closed := closes[id]
		return &WarehouseConn{Closers: []func() error{func() error {
			if closed != nil {
				atomic.AddInt32(closed, 1)
			}
			return nil
		}}}, nil
	}
}

func runtimeWith(build WarehouseBuilder, maxWarm int, ids ...string) *ProjectRuntime {
	ds := make([]DatasourceInfo, 0, len(ids))
	for _, id := range ids {
		ds = append(ds, DatasourceInfo{ID: id})
	}
	return NewProjectRuntime(ProjectRuntimeOptions{
		Datasources:        ds,
		PrimaryID:          ids[0],
		Build:              build,
		MaxWarmDatasources: maxWarm,
	})
}

func TestRuntime_LazyBuildAndReuse(t *testing.T) {
	builds := map[string]*int32{"a": new(int32), "b": new(int32)}
	rt := runtimeWith(countingConnBuilder(builds, nil), 8, "a", "b")

	// Touch only "a" three times: built once, reused, and "b" never built.
	for i := 0; i < 3; i++ {
		conn, release, err := rt.acquireConn(context.Background(), "a")
		if err != nil || conn == nil {
			t.Fatalf("acquireConn(a): %v", err)
		}
		release()
	}
	if got := atomic.LoadInt32(builds["a"]); got != 1 {
		t.Fatalf("builds[a] = %d, want 1 (lazy + reused)", got)
	}
	if got := atomic.LoadInt32(builds["b"]); got != 0 {
		t.Fatalf("builds[b] = %d, want 0 (never touched — the whole point of lazy build)", got)
	}
}

func TestRuntime_ConcurrentSameDatasourceSharesBuild(t *testing.T) {
	builds := map[string]*int32{"a": new(int32)}
	rt := runtimeWith(countingConnBuilder(builds, nil), 8, "a")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, release, err := rt.acquireConn(context.Background(), "a")
			if err != nil || conn == nil {
				t.Errorf("acquireConn: %v", err)
				return
			}
			release()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(builds["a"]); got != 1 {
		t.Fatalf("builds[a] = %d, want 1 (concurrent callers share one build)", got)
	}
}

func TestRuntime_LRUEvictsIdleAndRebuilds(t *testing.T) {
	builds := map[string]*int32{"a": new(int32), "b": new(int32)}
	closes := map[string]*int32{"a": new(int32), "b": new(int32)}
	rt := runtimeWith(countingConnBuilder(builds, closes), 1, "a", "b")

	_, relA, _ := rt.acquireConn(context.Background(), "a")
	relA() // a idle

	_, relB, _ := rt.acquireConn(context.Background(), "b") // over cap → evict a
	relB()

	// Eviction closes the connection on a background goroutine (as the project
	// pool does), so poll briefly for it.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(closes["a"]) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(closes["a"]) == 0 {
		t.Fatal("expected LRU to evict + close idle datasource a")
	}

	// a is gone → touching it again rebuilds it.
	_, relA2, _ := rt.acquireConn(context.Background(), "a")
	relA2()
	if got := atomic.LoadInt32(builds["a"]); got != 2 {
		t.Fatalf("builds[a] = %d, want 2 (rebuilt after eviction)", got)
	}
}

func TestRuntime_DoesNotEvictInUse(t *testing.T) {
	builds := map[string]*int32{"a": new(int32), "b": new(int32)}
	closes := map[string]*int32{"a": new(int32), "b": new(int32)}
	rt := runtimeWith(countingConnBuilder(builds, closes), 1, "a", "b")

	_, relA, _ := rt.acquireConn(context.Background(), "a") // held (in use)
	defer relA()

	_, relB, _ := rt.acquireConn(context.Background(), "b") // cap hit, but a is in use
	defer relB()

	if atomic.LoadInt32(closes["a"]) != 0 {
		t.Fatal("must not evict an in-use datasource connection (would break its in-flight query)")
	}
}

// Concurrent first touches of many distinct datasources over a small warm cap
// must never hand a caller a connection that gets closed while it is held. The
// bug this guards: a freshly-built entry sat at inUse=0 during the window
// between its build and the caller's refcount bump, so another datasource's
// first touch could evict + close it. Reserving inUse=1 at creation closes that
// window; an in-use entry is never evicted, so a held conn is never closed.
// Run under -race for maximum signal.
func TestRuntime_ConcurrentFirstTouchNeverClosesHeldConn(t *testing.T) {
	const n = 12
	ids := make([]string, n)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}
	// Each build stamps a fresh "closed" flag for that datasource and delays a
	// touch to widen the build→reserve window the bug lived in.
	var mu sync.Mutex
	closedFlags := map[string]*int32{}
	build := func(_ context.Context, id string) (*WarehouseConn, error) {
		closed := new(int32)
		mu.Lock()
		closedFlags[id] = closed
		mu.Unlock()
		time.Sleep(time.Millisecond)
		return &WarehouseConn{Closers: []func() error{func() error { atomic.StoreInt32(closed, 1); return nil }}}, nil
	}
	rt := runtimeWith(build, 3, ids...) // cap 3 « 12 ⇒ frequent evictions

	var wg sync.WaitGroup
	var handedClosed int32
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			conn, release, err := rt.acquireConn(context.Background(), id)
			if err != nil || conn == nil {
				return
			}
			// While we hold this connection it must not be closed under us.
			mu.Lock()
			flag := closedFlags[id]
			mu.Unlock()
			if flag != nil && atomic.LoadInt32(flag) == 1 {
				atomic.AddInt32(&handedClosed, 1)
			}
			release()
		}(id)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&handedClosed); got != 0 {
		t.Fatalf("a held connection was closed by eviction %d times — inUse reservation is not protecting new entries", got)
	}
}

// Acquiring more datasources than the warm cap while all are in use grows the
// map past the cap (eviction skips in-use entries). Once released, the runtime
// must trim idle LRU entries back down to the cap — otherwise the connection
// bound is defeated for concurrent multi-datasource turns.
func TestRuntime_ReleaseTrimsWarmBackToCap(t *testing.T) {
	const maxWarm = 2
	ids := []string{"a", "b", "c", "d", "e"}
	rt := runtimeWith(func(context.Context, string) (*WarehouseConn, error) {
		return &WarehouseConn{Closers: []func() error{func() error { return nil }}}, nil
	}, maxWarm, ids...)

	// Acquire (and HOLD) all of them, forcing the warm map past the cap.
	releases := make([]func(), 0, len(ids))
	for _, id := range ids {
		_, release, err := rt.acquireConn(context.Background(), id)
		if err != nil {
			t.Fatalf("acquireConn(%s): %v", id, err)
		}
		releases = append(releases, release)
	}
	rt.mu.Lock()
	overCap := len(rt.warm)
	rt.mu.Unlock()
	if overCap != len(ids) {
		t.Fatalf("warm size while all in use = %d, want %d (eviction must not drop in-use entries)", overCap, len(ids))
	}

	// Release everything; each release should reclaim idle LRU entries.
	for _, release := range releases {
		release()
	}
	rt.mu.Lock()
	got := len(rt.warm)
	rt.mu.Unlock()
	if got > maxWarm {
		t.Fatalf("warm size after release = %d, want <= %d (release must trim back to the cap)", got, maxWarm)
	}
}

func TestRuntime_CloseClosesWarmAndShared(t *testing.T) {
	var connClosed, sharedClosed int32
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		Datasources: []DatasourceInfo{{ID: "a"}},
		PrimaryID:   "a",
		Build: func(context.Context, string) (*WarehouseConn, error) {
			return &WarehouseConn{Closers: []func() error{func() error { atomic.AddInt32(&connClosed, 1); return nil }}}, nil
		},
		SharedClosers: []func() error{func() error { atomic.AddInt32(&sharedClosed, 1); return nil }},
	})
	_, release, _ := rt.acquireConn(context.Background(), "a")
	release()

	rt.Close()
	if atomic.LoadInt32(&connClosed) != 1 {
		t.Fatalf("warm connection not closed on Close(): %d", connClosed)
	}
	if atomic.LoadInt32(&sharedClosed) != 1 {
		t.Fatalf("shared closer not run on Close(): %d", sharedClosed)
	}
}

// acquireConn must honor cancellation while a cold datasource is still
// connecting: buildConn detaches cancellation (WithoutCancel) so a shared conn
// survives one hop, so a synchronous build would block the caller past its
// turn/query timeout on first touch. The build must run async and the select
// race it against ctx.Done().
func TestRuntime_AcquireHonorsCancelDuringColdConnect(t *testing.T) {
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		Datasources: []DatasourceInfo{{ID: "a"}},
		PrimaryID:   "a",
		Build: func(context.Context, string) (*WarehouseConn, error) {
			close(buildStarted)
			<-releaseBuild // simulate a slow cold connect
			return &WarehouseConn{Closers: []func() error{func() error { return nil }}}, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, release, err := rt.acquireConn(ctx, "a")
		if release != nil {
			release()
		}
		done <- err
	}()

	<-buildStarted // the cold connect is in flight
	cancel()       // cancel the turn while the connect is still running

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquireConn err = %v, want context.Canceled (must not block on the cold connect)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquireConn blocked on the cold connect instead of honoring cancellation")
	}
	close(releaseBuild) // let the detached build finish and clean up
}

// A datasource whose build is still in flight when Close() runs (the shutdown
// sweep) must still be closed once the build completes — buildConn runs under a
// detached context, so without this it would leak a live connection nothing owns.
func TestRuntime_CloseClosesInFlightBuild(t *testing.T) {
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	var closed int32
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		Datasources: []DatasourceInfo{{ID: "a"}},
		PrimaryID:   "a",
		Build: func(context.Context, string) (*WarehouseConn, error) {
			close(buildStarted)
			<-releaseBuild // hold the build open so Close() runs mid-build
			return &WarehouseConn{Closers: []func() error{func() error { atomic.AddInt32(&closed, 1); return nil }}}, nil
		},
	})

	go func() {
		if _, release, err := rt.acquireConn(context.Background(), "a"); err == nil && release != nil {
			release()
		}
	}()

	<-buildStarted // build is in flight (entry present, ready not yet closed)
	rt.Close()     // must schedule a close-on-completion for the in-flight build
	close(releaseBuild)

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&closed) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if atomic.LoadInt32(&closed) == 0 {
		t.Fatal("Close() must close a datasource whose build completes after shutdown")
	}
}

func TestRuntime_BuildErrorDropsEntryAndRetries(t *testing.T) {
	var attempts int32
	rt := NewProjectRuntime(ProjectRuntimeOptions{
		Datasources: []DatasourceInfo{{ID: "a"}},
		PrimaryID:   "a",
		Build: func(context.Context, string) (*WarehouseConn, error) {
			if atomic.AddInt32(&attempts, 1) == 1 {
				return nil, errors.New("transient connect failure")
			}
			return &WarehouseConn{}, nil
		},
	})
	if _, _, err := rt.acquireConn(context.Background(), "a"); err == nil {
		t.Fatal("expected first acquire to surface the build error")
	}
	conn, release, err := rt.acquireConn(context.Background(), "a")
	if err != nil || conn == nil {
		t.Fatalf("retry after a failed build should rebuild: %v", err)
	}
	release()
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2 (failed entry dropped and rebuilt, not cached)", got)
	}
}

// --- per-turn datasource routing plan ----------------------------------------

func routingRuntime(ids ...string) *ProjectRuntime {
	return runtimeWith(func(context.Context, string) (*WarehouseConn, error) { return &WarehouseConn{}, nil }, 8, ids...)
}

func TestResolveTurnRouting_SingleDatasourcePins(t *testing.T) {
	rt := routingRuntime("default")
	r, err := rt.resolveTurnRouting("")
	if err != nil {
		t.Fatal(err)
	}
	if r.multi {
		t.Error("single-datasource project must not be multi (behaviour-neutral vs single-warehouse)")
	}
	if r.pinned != "default" || r.primary != "default" {
		t.Errorf("pinned=%q primary=%q, want default/default", r.pinned, r.primary)
	}
}

func TestResolveTurnRouting_MultiDatasourceDefersToModel(t *testing.T) {
	rt := routingRuntime("wh_a", "wh_b")
	r, err := rt.resolveTurnRouting("")
	if err != nil {
		t.Fatal(err)
	}
	if !r.multi {
		t.Error("multi-datasource project with no explicit pin must be multi")
	}
	if r.pinned != "" {
		t.Errorf("pinned=%q, want empty (model chooses)", r.pinned)
	}
	if r.primary != "wh_a" {
		t.Errorf("primary=%q, want wh_a", r.primary)
	}
}

func TestResolveTurnRouting_ExplicitOverridePins(t *testing.T) {
	rt := routingRuntime("wh_a", "wh_b")
	r, err := rt.resolveTurnRouting("wh_b")
	if err != nil {
		t.Fatal(err)
	}
	if r.multi {
		t.Error("an explicit datasource override must pin (not multi)")
	}
	if r.pinned != "wh_b" || r.primary != "wh_b" {
		t.Errorf("pinned=%q primary=%q, want wh_b/wh_b", r.pinned, r.primary)
	}
}

// TestResolveTurnRouting_CarriesTheWholeProject pins the wiring the reachable
// set depends on. Every routing must carry the project's datasources
// alongside its visible ones, including the explicit-pin case whose visible
// list is a single datasource — without it a turn describes what it shows
// rather than what it can reach, and the two differ whenever the router
// narrows.
func TestResolveTurnRouting_CarriesTheWholeProject(t *testing.T) {
	rt := routingRuntime("wh_a", "wh_b")

	for _, explicit := range []string{"", "wh_b"} {
		r, err := rt.resolveTurnRouting(explicit)
		if err != nil {
			t.Fatalf("explicit=%q: %v", explicit, err)
		}
		var got []string
		for _, d := range r.all {
			got = append(got, d.ID)
		}
		if len(got) != 2 || got[0] != "wh_a" || got[1] != "wh_b" {
			t.Errorf("explicit=%q: routing carries %v, want the whole project [wh_a wh_b]", explicit, got)
		}
	}
}

func TestResolveTurnRouting_ExplicitUnknownErrors(t *testing.T) {
	rt := routingRuntime("wh_a", "wh_b")
	if _, err := rt.resolveTurnRouting("nope"); err == nil {
		t.Fatal("an unknown explicit datasource must fail the turn, not silently pick another")
	}
}

// --- SchemaRouter -------------------------------------------------------------

func TestSchemaRouter_LookupUnknownDatasourceErrors(t *testing.T) {
	sr := NewSchemaRouter(SchemaRouterOptions{
		Lookups: map[string]ai.SchemaProvider{"wh_a": &fakeSchema{}},
		Primary: "wh_a",
	})
	if _, err := sr.Lookup(context.Background(), "wh_zzz", []string{"t"}); err == nil {
		t.Fatal("lookup against an unknown datasource must error, not fall back to another datasource's schema")
	}
}

func TestSchemaRouter_LookupEmptyUsesPrimary(t *testing.T) {
	primaryHit := ai.LookupResult{Tables: []ai.LookupTable{{Table: "sales.orders"}}}
	sr := NewSchemaRouter(SchemaRouterOptions{
		Lookups: map[string]ai.SchemaProvider{
			"wh_a": &fakeSchema{lookup: primaryHit},
			"wh_b": &fakeSchema{lookup: ai.LookupResult{Tables: []ai.LookupTable{{Table: "crm.users"}}}},
		},
		Primary: "wh_a",
	})
	res, err := sr.Lookup(context.Background(), "", []string{"orders"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tables) != 1 || res.Tables[0].Table != "sales.orders" {
		t.Fatalf("empty datasource id should resolve to the primary, got %+v", res.Tables)
	}
}

func TestSchemaRouter_SearchAllNilSpanUnavailable(t *testing.T) {
	sr := NewSchemaRouter(SchemaRouterOptions{
		Lookups: map[string]ai.SchemaProvider{"wh_a": &fakeSchema{}},
		Primary: "wh_a",
	})
	if _, err := sr.SearchAll(context.Background(), "x", 5); err == nil {
		t.Fatal("SearchAll must report unavailable when no cross-datasource searcher is wired")
	}
}

func TestSchemaRouter_SearchOneTagsDatasource(t *testing.T) {
	sr := NewSchemaRouter(SchemaRouterOptions{
		Lookups: map[string]ai.SchemaProvider{
			"wh_a": &fakeSchema{hits: []ai.SearchHit{{Table: "sales.orders", Blurb: "orders", RowCount: 5, Score: 0.9}}},
		},
		Labels:  map[string]string{"wh_a": "Sales"},
		Primary: "wh_a",
	})
	hits, err := sr.SearchOne(context.Background(), "wh_a", "orders", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DatasourceID != "wh_a" || hits[0].DatasourceLabel != "Sales" || hits[0].Table != "sales.orders" {
		t.Fatalf("SearchOne must tag the hit with its datasource: %+v", hits)
	}
}

func TestNewSchemaRouter_NilWhenNothingIndexed(t *testing.T) {
	if sr := NewSchemaRouter(SchemaRouterOptions{Lookups: map[string]ai.SchemaProvider{}}); sr != nil {
		t.Fatal("router with no lookup providers must be nil so the loop degrades to schema-less")
	}
}
