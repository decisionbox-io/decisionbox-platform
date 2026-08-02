package askserve

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// defaultMaxWarmDatasources bounds how many warehouse connections one project
// keeps warm at once (Risk R3 — a project with many datasources must not pin N
// live connections indefinitely). A turn spans at most a handful of datasources
// and per-project turn concurrency is small, so this is generous; the LRU only
// bites for pathological projects. Evicted connections are rebuilt on next use.
const defaultMaxWarmDatasources = 8

// ProjectRuntime is the per-project hand-off the dependency builder returns. It
// holds the datasource-agnostic pieces (the reasoning LLM, the insights search,
// the cross-datasource schema knowledge, and the datasource descriptors) plus a
// lazy, LRU-bounded cache of per-datasource warehouse connections. One runtime
// is shared across concurrent turns for the project; the connection cache is
// mutex-guarded and single-flights builds, exactly like the project pool one
// level up.
type ProjectRuntime struct {
	// AIClient drives the reasoning LLM (multi-turn CreateMessage), shared
	// across every datasource in the project.
	AIClient *ai.Client
	// Model is the project's configured model id (for the transcript record).
	Model string
	// InsightsProvider serves search_insights (semantic search over the
	// project's discovered insights). Project-level and datasource-agnostic.
	// May be nil when the project has no embedder / vector store.
	InsightsProvider ai.InsightsProvider

	// Schema serves lookup_schema (per datasource) and search_tables (across
	// all datasources). Built eagerly from every datasource's cached schema —
	// it needs no warehouse connection. May be nil when nothing is indexed.
	Schema *SchemaRouter

	// Datasources describes each of the project's warehouses for the prompt +
	// routing, primary first. Never empty for a runnable project.
	Datasources []DatasourceInfo
	// PrimaryID is the datasource a query targets when it names none.
	PrimaryID string

	// build lazily constructs a datasource's execution context (open + validate
	// read-only + wire the executor).
	build   WarehouseBuilder
	maxWarm int

	mu   sync.Mutex
	warm map[string]*connEntry

	// sharedClosers release project-level resources (schema retriever handle,
	// etc.). Per-datasource connections are closed via their own entries.
	sharedClosers []func() error
}

// connEntry is one warm datasource connection in the runtime's cache. It mirrors
// the project pool's poolEntry: a ready channel single-flights the build, a
// reference count keeps an in-flight query from being evicted, and lastUsed
// drives LRU eviction.
type connEntry struct {
	id       string
	ready    chan struct{} // closed when the build completes
	conn     *WarehouseConn
	err      error
	inUse    int
	lastUsed time.Time
}

// ProjectRuntimeOptions assembles a ProjectRuntime. The agentserver package
// owns provider/secret wiring and builds this; askserve consumes it.
type ProjectRuntimeOptions struct {
	AIClient         *ai.Client
	Model            string
	InsightsProvider ai.InsightsProvider
	Schema           *SchemaRouter
	Datasources      []DatasourceInfo
	PrimaryID        string
	Build            WarehouseBuilder
	// MaxWarmDatasources bounds warm connections per project (0 → default).
	MaxWarmDatasources int
	SharedClosers      []func() error
}

// NewProjectRuntime builds a ProjectRuntime from its options.
func NewProjectRuntime(opts ProjectRuntimeOptions) *ProjectRuntime {
	maxWarm := opts.MaxWarmDatasources
	if maxWarm <= 0 {
		maxWarm = defaultMaxWarmDatasources
	}
	return &ProjectRuntime{
		AIClient:         opts.AIClient,
		Model:            opts.Model,
		InsightsProvider: opts.InsightsProvider,
		Schema:           opts.Schema,
		Datasources:      opts.Datasources,
		PrimaryID:        opts.PrimaryID,
		build:            opts.Build,
		maxWarm:          maxWarm,
		warm:             make(map[string]*connEntry),
		sharedClosers:    opts.SharedClosers,
	}
}

// datasource returns the descriptor for id, or false when id is not a
// configured datasource of this project.
func (r *ProjectRuntime) datasource(id string) (DatasourceInfo, bool) {
	for _, d := range r.Datasources {
		if d.ID == id {
			return d, true
		}
	}
	return DatasourceInfo{}, false
}

// acquireConn returns the datasource's execution context, building it lazily on
// first use (and rebuilding it if it was evicted), plus a release the caller
// MUST invoke when it is done using the connection for this hop. The connection
// is reference-counted for the release window so an LRU eviction never closes a
// connection with a query in flight. Concurrent callers for the same datasource
// share one build and one connection.
func (r *ProjectRuntime) acquireConn(ctx context.Context, id string) (*WarehouseConn, func(), error) {
	if r.build == nil {
		return nil, nil, fmt.Errorf("datasource %q: no warehouse builder configured", id)
	}

	r.mu.Lock()
	e, ok := r.warm[id]
	if !ok {
		// Reserve the entry (inUse=1) before releasing the lock so a concurrent
		// acquire's capacity eviction can't drop and close this connection during
		// the build window — evictForCapacityLocked skips in-use entries. Without
		// this, a first touch of another datasource that trips the warm cap could
		// evict this just-built entry between its ready-close and the refcount
		// bump, handing this caller a closed connection.
		e = &connEntry{id: id, ready: make(chan struct{}), lastUsed: time.Now(), inUse: 1}
		r.warm[id] = e
		r.evictForCapacityLocked(id)
		r.mu.Unlock()
		r.buildConn(ctx, e)
	} else {
		// Existing entry: reserve under the same lock we read it on so it can't be
		// evicted between the read and the refcount bump.
		e.inUse++
		e.lastUsed = time.Now()
		r.mu.Unlock()
	}

	select {
	case <-e.ready:
	case <-ctx.Done():
		r.releaseConn(id)
		return nil, nil, ctx.Err()
	}

	if e.err != nil {
		r.mu.Lock()
		e.inUse--
		// Drop a failed entry so the next hop retries the build instead of
		// inheriting the cached error for the rest of the turn.
		if cur, ok := r.warm[id]; ok && cur == e && e.inUse == 0 {
			delete(r.warm, id)
		}
		r.mu.Unlock()
		return nil, nil, e.err
	}

	return e.conn, func() { r.releaseConn(id) }, nil
}

// buildConn runs the datasource builder under a detached context so one hop's
// cancellation can't poison a connection shared across concurrent turns.
func (r *ProjectRuntime) buildConn(ctx context.Context, e *connEntry) {
	conn, err := r.build(context.WithoutCancel(ctx), e.id)
	e.conn, e.err = conn, err
	close(e.ready)
	if err != nil {
		applog.WithError(err).WithField("datasource_id", e.id).Warn("ask-serve: datasource connection build failed")
	}
}

func (r *ProjectRuntime) releaseConn(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.warm[id]; ok {
		if e.inUse > 0 {
			e.inUse--
		}
		e.lastUsed = time.Now()
	}
}

// evictForCapacityLocked drops the least-recently-used idle connection when the
// cache is over capacity. Caller holds r.mu. The just-created entry is exempt.
func (r *ProjectRuntime) evictForCapacityLocked(keep string) {
	if r.maxWarm <= 0 || len(r.warm) <= r.maxWarm {
		return
	}
	var lru *connEntry
	for id, e := range r.warm {
		if id == keep || e.inUse > 0 {
			continue
		}
		select {
		case <-e.ready: // only evict fully-built entries
		default:
			continue
		}
		if lru == nil || e.lastUsed.Before(lru.lastUsed) {
			lru = e
		}
	}
	if lru != nil {
		delete(r.warm, lru.id)
		go closeConn(lru.conn)
	}
}

// Close releases every warm datasource connection and the project-level
// shared resources. Called by the pool on eviction / shutdown.
func (r *ProjectRuntime) Close() {
	r.mu.Lock()
	entries := r.warm
	r.warm = make(map[string]*connEntry)
	r.mu.Unlock()
	for _, e := range entries {
		select {
		case <-e.ready:
			closeConn(e.conn)
		default:
			// Build still in flight. buildConn runs under context.WithoutCancel,
			// so it will finish and produce a live connection after we've cleared
			// r.warm — which nothing else would then close. Wait for it on a
			// detached goroutine and close it so the connection can't leak past
			// shutdown. (Only reachable from closeAll's shutdown sweep; capacity/
			// idle eviction never closes a runtime with an in-flight build, since
			// its turn still holds a pool reference.)
			go func(e *connEntry) {
				<-e.ready
				closeConn(e.conn)
			}(e)
		}
	}
	for _, c := range r.sharedClosers {
		if c != nil {
			_ = c()
		}
	}
}

func closeConn(conn *WarehouseConn) {
	if conn == nil {
		return
	}
	for _, c := range conn.Closers {
		if c != nil {
			_ = c()
		}
	}
}
