package apiserver

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/decisionbox-io/decisionbox/services/api/internal/server"
)

// RegisterRouteGroup registers a (prefix, handler) pair that the API
// server will mount on its authenticated mux during Run(). Intended for
// binaries that compose the community API server with their own
// init()-registered routes — the registered handler is reachable at the
// full prefix without modifying the core router.
//
// Constraints (panic on violation — these are programmer errors caught
// at init() rather than at first request):
//
//   - prefix must start with "/" and must not end with one. The mux
//     appends the trailing slash internally.
//   - prefix must not be empty.
//   - h must not be nil.
//   - prefix must be unique across all registered groups.
//
// The handler receives the full request URL including the prefix; it
// is responsible for any internal sub-routing and for its own auth /
// RBAC enforcement beyond what the API server's global chain provides.
func RegisterRouteGroup(prefix string, h http.Handler) {
	if prefix == "" {
		panic("apiserver: RegisterRouteGroup with empty prefix")
	}
	if !strings.HasPrefix(prefix, "/") {
		panic(fmt.Sprintf("apiserver: RegisterRouteGroup prefix %q must start with '/'", prefix))
	}
	if strings.HasSuffix(prefix, "/") {
		panic(fmt.Sprintf("apiserver: RegisterRouteGroup prefix %q must not end with '/'", prefix))
	}
	if h == nil {
		panic(fmt.Sprintf("apiserver: RegisterRouteGroup handler is nil for prefix %q", prefix))
	}
	routeGroupsMu.Lock()
	defer routeGroupsMu.Unlock()
	for _, g := range routeGroups {
		if g.Prefix == prefix {
			panic(fmt.Sprintf("apiserver: RegisterRouteGroup called twice for prefix %q", prefix))
		}
	}
	routeGroups = append(routeGroups, server.RouteGroup{Prefix: prefix, Handler: h})
}

// RegisteredRouteGroups returns the route groups registered via
// RegisterRouteGroup. The returned slice is a copy — callers cannot
// mutate the registry by aliasing.
//
// Read at Run() time when building the HTTP mux. Tests that need a
// clean registry use ResetRouteGroupsForTest.
func RegisteredRouteGroups() []server.RouteGroup {
	routeGroupsMu.RLock()
	defer routeGroupsMu.RUnlock()
	out := make([]server.RouteGroup, len(routeGroups))
	copy(out, routeGroups)
	return out
}

// ResetRouteGroupsForTest clears the registry. Exported so tests in
// other packages (e.g. enterprise plugin tests that register groups
// and want to start clean) can wipe state between cases. Not meant
// for production code.
func ResetRouteGroupsForTest() {
	routeGroupsMu.Lock()
	defer routeGroupsMu.Unlock()
	routeGroups = nil
}

var (
	routeGroupsMu sync.RWMutex
	routeGroups   []server.RouteGroup
)
