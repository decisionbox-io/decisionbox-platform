package server

import (
	"fmt"
	"net/http"
)

// mountRouteGroups mounts each (prefix, handler) onto the given mux at
// "{prefix}/". Duplicate prefixes panic so a boot-time collision is
// visible immediately. Exported through the package only via
// NewWithRouteGroups; kept package-private here so tests can exercise
// the panic path without standing up a full server.
func mountRouteGroups(mux *http.ServeMux, groups []RouteGroup) {
	seen := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if _, dup := seen[g.Prefix]; dup {
			panic(fmt.Sprintf("server: duplicate route-group prefix %q", g.Prefix))
		}
		seen[g.Prefix] = struct{}{}
		mux.Handle(g.Prefix+"/", g.Handler)
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
