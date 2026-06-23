package askserve

import (
	"context"
	"sync"
	"time"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// pool is the warm per-project runtime cache. Each project's warehouse + LLM +
// schema bundle is built lazily on first use and reused across turns. Entries
// are reference-counted so an idle/LRU eviction never closes a runtime with an
// in-flight turn; a background janitor evicts entries idle past the TTL.
type pool struct {
	build          ProjectBuilder
	idleTTL        time.Duration
	maxProjects    int
	connectTimeout time.Duration

	mu      sync.Mutex
	entries map[string]*poolEntry
	stop    chan struct{}
	stopped bool
}

type poolEntry struct {
	projectID string
	ready     chan struct{} // closed when the build completes
	rt        *ProjectRuntime
	err       error
	inUse     int
	lastUsed  time.Time
}

func newPool(build ProjectBuilder, cfg Config) *pool {
	return &pool{
		build:          build,
		idleTTL:        cfg.PoolIdleTTL,
		maxProjects:    cfg.PoolMaxProjects,
		connectTimeout: cfg.ConnectTimeout,
		entries:        make(map[string]*poolEntry),
		stop:           make(chan struct{}),
	}
}

// acquire returns the project's runtime (building it on first use) plus a
// release function the caller MUST invoke when the turn finishes. Concurrent
// callers for the same project share one build and one runtime.
func (p *pool) acquire(ctx context.Context, projectID string) (*ProjectRuntime, func(), error) {
	p.mu.Lock()
	e, ok := p.entries[projectID]
	if !ok {
		e = &poolEntry{projectID: projectID, ready: make(chan struct{}), lastUsed: time.Now()}
		p.entries[projectID] = e
		p.evictForCapacityLocked(projectID)
		p.mu.Unlock()
		p.buildEntry(ctx, e)
	} else {
		p.mu.Unlock()
	}

	// Reserve before waiting on the build so a janitor can't evict between
	// ready and our refcount bump.
	p.mu.Lock()
	e.inUse++
	e.lastUsed = time.Now()
	p.mu.Unlock()

	select {
	case <-e.ready:
	case <-ctx.Done():
		p.release(projectID)
		return nil, nil, ctx.Err()
	}

	if e.err != nil {
		p.mu.Lock()
		e.inUse--
		// Drop a failed entry so the next turn retries the build instead of
		// inheriting the cached error forever.
		if cur, ok := p.entries[projectID]; ok && cur == e && e.inUse == 0 {
			delete(p.entries, projectID)
		}
		p.mu.Unlock()
		return nil, nil, e.err
	}

	return e.rt, func() { p.release(projectID) }, nil
}

// buildEntry runs the injected builder under a detached, bounded context so a
// single turn's cancellation can't poison a runtime shared by other turns.
func (p *pool) buildEntry(ctx context.Context, e *poolEntry) {
	bctx := context.WithoutCancel(ctx)
	if p.connectTimeout > 0 {
		var cancel context.CancelFunc
		bctx, cancel = context.WithTimeout(bctx, p.connectTimeout)
		defer cancel()
	}
	rt, err := p.build(bctx, e.projectID)
	e.rt, e.err = rt, err
	close(e.ready)
	if err != nil {
		applog.WithError(err).WithField("project_id", e.projectID).Warn("ask-serve: project runtime build failed")
	}
}

func (p *pool) release(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[projectID]; ok {
		if e.inUse > 0 {
			e.inUse--
		}
		e.lastUsed = time.Now()
	}
}

// evictForCapacityLocked drops the least-recently-used idle entry when the pool
// is at capacity. Caller holds p.mu. The just-created entry (keep) is exempt.
func (p *pool) evictForCapacityLocked(keep string) {
	if p.maxProjects <= 0 || len(p.entries) <= p.maxProjects {
		return
	}
	var lru *poolEntry
	for id, e := range p.entries {
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
		delete(p.entries, lru.projectID)
		go closeRuntime(lru)
	}
}

// janitor periodically evicts entries idle longer than the TTL.
func (p *pool) janitor() {
	if p.idleTTL <= 0 {
		return
	}
	ticker := time.NewTicker(p.idleTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.evictIdle()
		}
	}
}

func (p *pool) evictIdle() {
	now := time.Now()
	var evicted []*poolEntry
	p.mu.Lock()
	for id, e := range p.entries {
		if e.inUse > 0 || now.Sub(e.lastUsed) < p.idleTTL {
			continue
		}
		select {
		case <-e.ready:
		default:
			continue
		}
		delete(p.entries, id)
		evicted = append(evicted, e)
	}
	p.mu.Unlock()
	for _, e := range evicted {
		closeRuntime(e)
	}
}

// closeAll evicts and closes every entry — called on shutdown.
func (p *pool) closeAll() {
	p.mu.Lock()
	if !p.stopped {
		close(p.stop)
		p.stopped = true
	}
	entries := p.entries
	p.entries = make(map[string]*poolEntry)
	p.mu.Unlock()
	for _, e := range entries {
		closeRuntime(e)
	}
}

func closeRuntime(e *poolEntry) {
	select {
	case <-e.ready:
		if e.rt != nil {
			e.rt.Close()
		}
	default:
		// never finished building; nothing to close
	}
}
