// Package systeminfo is the component registry behind GET /api/v1/system.
//
// It answers one question for an operator: "what is deployed here, and at
// what version?" Each platform component — a service like the API, or a
// background worker that rides inside one — contributes a Descriptor. The
// system endpoint serves whatever the registry reports; nothing about the
// component list is hardcoded in the endpoint or the dashboard.
//
// Two extension points, both safe to call from init():
//
//   - Register(Descriptor) — for a component whose version is known in the
//     running process (the API stamps its own ldflags version; its
//     in-process workers share it). This is the common case.
//
//   - RegisterSource(Source) — for components the process cannot introspect
//     directly: out-of-process workers or services that run as their own
//     image with their own version and self-report a version + last-seen
//     heartbeat into a store. A Source reads that store at request time and
//     returns their Descriptors. The core platform ships only in-process
//     components, but this is the documented seam for the rest.
//
// The registry mirrors the codebase's other init()-time registries (LLM /
// warehouse / secret providers, apiserver.RegisterRouteGroup).
package systeminfo

import (
	"context"
	"sort"
	"sync"
)

// Kind classifies a component as a standalone service or a worker that
// runs inside a parent service's image.
type Kind string

const (
	// KindService is a deployable image (API, Dashboard, Agent).
	KindService Kind = "service"
	// KindWorker is a background loop that runs in-process inside a
	// parent service and is not a separately-versioned artifact.
	KindWorker Kind = "worker"
)

// Descriptor identifies one platform component for the system inventory.
//
// A worker that rides a parent image MUST set RunsIn and an explanatory
// Note so a reader is not misled into thinking it is independently
// versioned — its Version is the parent image's version.
type Descriptor struct {
	// Name is the human-facing component name (e.g. "API",
	// "Schema indexing"). Unique within the registry; a later
	// registration with the same Name replaces the earlier one.
	Name string `json:"name"`

	// Kind is "service" or "worker".
	Kind Kind `json:"kind"`

	// Version is the component's reported version. For a worker this is
	// its parent image's version; Note explains that.
	Version string `json:"version,omitempty"`

	// Commit is the git commit the component was built from, when known.
	Commit string `json:"commit,omitempty"`

	// BuildDate is the component's build timestamp (RFC3339), when known.
	BuildDate string `json:"build_date,omitempty"`

	// RunsIn names the parent service a worker runs inside (e.g. "API").
	// Empty for standalone services.
	RunsIn string `json:"runs_in,omitempty"`

	// Note is a free-text clarification — e.g. that a worker shares its
	// parent image's version, or that the agent version reflects the
	// configured image rather than a live process.
	Note string `json:"note,omitempty"`
}

// Source dynamically contributes Descriptors at collection time. Use it
// for components whose version is not known in this process — typically
// out-of-process workers that self-report version + heartbeat into a
// store the Source reads. If a Source returns an error, Collect ignores
// the error (a failing Source must not take the whole inventory down) but
// still merges any descriptors the Source returned alongside it — so a
// Source may return partial results on a transient read failure.
type Source func(ctx context.Context) ([]Descriptor, error)

var (
	mu      sync.RWMutex
	statics = make(map[string]Descriptor)
	sources []Source
)

// Register adds (or replaces, by Name) a static component descriptor.
// Intended for init()-time registration of in-process components.
// Panics on an empty Name — a programmer error caught at startup.
func Register(d Descriptor) {
	if d.Name == "" {
		panic("systeminfo: Register called with empty Name")
	}
	mu.Lock()
	defer mu.Unlock()
	statics[d.Name] = d
}

// RegisterSource adds a dynamic descriptor source, evaluated on every
// Collect. Panics on a nil source — a programmer error caught at startup.
func RegisterSource(s Source) {
	if s == nil {
		panic("systeminfo: RegisterSource called with nil Source")
	}
	mu.Lock()
	defer mu.Unlock()
	sources = append(sources, s)
}

// Collect returns the full component inventory: every static descriptor,
// merged with the descriptors returned by each registered source. A
// source's error is ignored — it cannot take the inventory down — and any
// descriptors that source returned alongside the error are still merged.
// When a source reports a Name that is also statically registered, the
// source's descriptor wins (it is the live reading). The result is sorted
// deterministically — services before workers, then by name — and is
// always non-nil.
func Collect(ctx context.Context) []Descriptor {
	mu.RLock()
	merged := make(map[string]Descriptor, len(statics))
	for name, d := range statics {
		merged[name] = d
	}
	srcs := make([]Source, len(sources))
	copy(srcs, sources)
	mu.RUnlock()

	for _, src := range srcs {
		// Error intentionally ignored: a failing source must not take the
		// inventory down, and it may still return partial descriptors.
		ds, _ := src(ctx)
		for _, d := range ds {
			if d.Name == "" {
				continue
			}
			merged[d.Name] = d
		}
	}

	out := make([]Descriptor, 0, len(merged))
	for _, d := range merged {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if ki, kj := kindOrder(out[i].Kind), kindOrder(out[j].Kind); ki != kj {
			return ki < kj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// kindOrder ranks services ahead of workers, with any unrecognised kind
// last, so Collect's output is stable and reads top-down.
func kindOrder(k Kind) int {
	switch k {
	case KindService:
		return 0
	case KindWorker:
		return 1
	default:
		return 2
	}
}

// ResetForTest clears the registry. For tests that need to start from a
// known-empty state; not for production code.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	statics = make(map[string]Descriptor)
	sources = nil
}
