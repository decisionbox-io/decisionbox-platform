package apiserver

import (
	"net/http"
	"sync"
)

// Middleware is a function that wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

type namedHTTPMiddleware struct {
	name string
	mw   Middleware
}

var (
	globalMu          sync.RWMutex
	globalMiddlewares []namedHTTPMiddleware
	globalNames       = make(map[string]bool)
)

// RegisterGlobalMiddleware registers a named HTTP middleware that wraps all API requests.
// Middlewares are applied in registration order (first registered = outermost).
// This is typically called from an init() function in a plugin.
func RegisterGlobalMiddleware(name string, mw Middleware) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if mw == nil {
		panic("apiserver: RegisterGlobalMiddleware middleware is nil for " + name)
	}
	if globalNames[name] {
		panic("apiserver: RegisterGlobalMiddleware called twice for " + name)
	}
	globalNames[name] = true
	globalMiddlewares = append(globalMiddlewares, namedHTTPMiddleware{name: name, mw: mw})
}

// ApplyGlobalMiddlewares wraps the given handler with all registered global middlewares.
// First registered middleware is outermost (executes first on request, last on response).
func ApplyGlobalMiddlewares(h http.Handler) http.Handler {
	globalMu.RLock()
	defer globalMu.RUnlock()
	for i := len(globalMiddlewares) - 1; i >= 0; i-- {
		h = globalMiddlewares[i].mw(h)
	}
	return h
}
