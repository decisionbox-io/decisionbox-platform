// Package catalog hosts an extension hook for the LLM model catalog.
// The community llm.ProviderMeta.Models slice is the authoritative
// catalog for built-in providers; plugins built on top of the
// community libraries can register an Extender to surface additional
// project-scoped model entries (for example, a per-project external
// model registry whose entries are not known at provider-init time).
//
// Two callers consume the registry:
//
//   - The API server exposes per-project extended entries through a
//     project-scoped endpoint so the dashboard can merge them into the
//     model picker alongside the built-in catalog.
//   - Resolvers that need to look up an opaque model ID (one not in any
//     built-in catalog) call Resolve to ask the registry whether any
//     plugin owns it.
//
// With zero extenders registered (the default community build), Extend
// returns nil and Resolve returns ErrModelNotFound. No allocation, no
// I/O — the registry is a no-op until a plugin attaches.
package catalog

import (
	"context"
	"errors"
	"sync"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Extender supplies additional ModelEntry rows from outside the built-in
// provider catalogs. Two responsibilities, both project-scoped:
//
//   - Extend lists every entry the extender wants to surface for the
//     given project. The caller MUST pass a non-empty project ID;
//     extenders may return an empty slice but must not return nil with
//     a non-nil error. Use the error channel only for transport / I/O
//     failures the caller should log; configuration-style misses are
//     reported as an empty slice.
//
//   - Resolve answers "do you own this opaque model ID?". Returns
//     (nil, ErrModelNotFound) when the extender does not own the ID —
//     this is the only way other extenders get a chance to match.
//     Any other error is surfaced to the caller as-is.
//
// Extenders must be safe for concurrent use; the registry calls them
// without locking.
type Extender interface {
	Extend(ctx context.Context, projectID string) ([]gollm.ModelEntry, error)
	Resolve(ctx context.Context, modelID string) (*gollm.ModelEntry, error)
}

// ErrModelNotFound is returned by Resolve when no registered extender
// owns the given model ID. Callers wanting to distinguish "no plugin
// owns this" from a real lookup failure can errors.Is against it.
var ErrModelNotFound = errors.New("catalog: model not found in any registered extender")

// RegisterExtender attaches an Extender to the registry. Intended to
// be called from a plugin's init(). Order of registration is preserved
// across Extend (results are concatenated in registration order) and
// across Resolve (the first extender that returns a non-not-found
// answer wins). nil panics so a typo in a plugin fails noisily at
// startup.
func RegisterExtender(e Extender) {
	if e == nil {
		panic("catalog: RegisterExtender with nil extender")
	}
	mu.Lock()
	defer mu.Unlock()
	extenders = append(extenders, e)
}

// RegisteredExtenders returns a snapshot of the registry. The slice is
// a copy — callers cannot mutate the registry by aliasing.
func RegisteredExtenders() []Extender {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Extender, len(extenders))
	copy(out, extenders)
	return out
}

// Extend asks every registered extender for project-scoped entries and
// returns the concatenated result in registration order. Empty project
// IDs are rejected: project scope is part of the contract, and silently
// returning a global list would expose plugin entries the caller has
// not authorised.
//
// The first transport error short-circuits — callers see whichever
// extender failed and must decide whether to retry or fall through to
// the built-in catalog alone.
func Extend(ctx context.Context, projectID string) ([]gollm.ModelEntry, error) {
	if projectID == "" {
		return nil, errors.New("catalog: Extend requires a non-empty project ID")
	}
	exts := RegisteredExtenders()
	if len(exts) == 0 {
		return nil, nil
	}
	out := make([]gollm.ModelEntry, 0)
	for _, e := range exts {
		entries, err := e.Extend(ctx, projectID)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

// Resolve walks the registry in order and returns the first non
// not-found answer. Returns (nil, ErrModelNotFound) when every
// extender disclaimed ownership, and (nil, err) on the first transport
// error.
func Resolve(ctx context.Context, modelID string) (*gollm.ModelEntry, error) {
	if modelID == "" {
		return nil, errors.New("catalog: Resolve requires a non-empty model ID")
	}
	for _, e := range RegisteredExtenders() {
		entry, err := e.Resolve(ctx, modelID)
		if err == nil {
			return entry, nil
		}
		if errors.Is(err, ErrModelNotFound) {
			continue
		}
		return nil, err
	}
	return nil, ErrModelNotFound
}

// ResetForTest clears the registry. Tests in other packages that
// register a stub extender and want to start from a clean state call
// this in t.Cleanup. Not meant for production code.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	extenders = nil
}

var (
	mu        sync.RWMutex
	extenders []Extender
)
