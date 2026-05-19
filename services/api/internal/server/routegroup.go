package server

import (
	"fmt"
	"net/http"
	"strings"
)

// mountRouteGroups mounts each (prefix, handler) onto the given mux at
// "{prefix}/". Enforces every constraint the RouteGroup doc-comment
// documents — empty/missing-leading-slash/trailing-slash prefixes, nil
// handlers, and duplicate prefixes all panic. Validation lives here
// (the closest layer to the actual mount) so both entry points —
// apiserver.RegisterRouteGroup at init time AND
// NewWithRouteGroups when callers pass a literal RouteGroup — share
// one enforcement point. Kept package-private so tests can exercise
// the panic paths without standing up a full server.
func mountRouteGroups(mux *http.ServeMux, groups []RouteGroup) {
	seen := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		ValidateRouteGroup(g)
		if _, dup := seen[g.Prefix]; dup {
			panic(fmt.Sprintf("server: duplicate route-group prefix %q", g.Prefix))
		}
		seen[g.Prefix] = struct{}{}
		mux.Handle(g.Prefix+"/", g.Handler)
	}
}

// ValidateRouteGroup panics if g violates any RouteGroup contract.
// Exported so cross-package callers (apiserver.RegisterRouteGroup) can
// share the same enforcement at registration time without rules
// drifting between entry points.
func ValidateRouteGroup(g RouteGroup) {
	if g.Prefix == "" {
		panic("server: route-group prefix is empty")
	}
	if !strings.HasPrefix(g.Prefix, "/") {
		panic(fmt.Sprintf("server: route-group prefix %q must start with '/'", g.Prefix))
	}
	if strings.HasSuffix(g.Prefix, "/") {
		panic(fmt.Sprintf("server: route-group prefix %q must not end with '/'", g.Prefix))
	}
	if g.Handler == nil {
		panic(fmt.Sprintf("server: route-group handler is nil for prefix %q", g.Prefix))
	}
}

// RouteGroup is a (prefix, handler) pair mounted on the API server's
// authenticated mux. Plugins built on top of the community API server
// (e.g. binaries that compose apiserver.Run() with their own
// init()-registered routes) declare additional API surface this way
// instead of editing the core router.
//
// One group, one prefix. The handler is mounted at "{prefix}/" on the
// authenticated mux, which means:
//
//   - The handler sees the FULL request URL, including the prefix.
//     Pattern matching inside the handler must account for it (use a
//     sub-mux with the same prefix or strip explicitly).
//   - All routes under the prefix go through the API server's auth +
//     RBAC chain. The handler is responsible for its own role checks.
//   - Prefixes are matched longest-first by Go's net/http mux; a group
//     under "/api/foo/" will not shadow a built-in route at "/api/foo"
//     (note the trailing slash difference).
//
// Prefixes must start with "/" and not end with one — the mux appends
// the trailing slash. Duplicate prefixes panic at mount time so a
// typo in a plugin fails noisily during boot rather than silently
// shadowing a built-in route.
type RouteGroup struct {
	Prefix  string
	Handler http.Handler
}
