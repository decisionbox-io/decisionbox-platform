package apiserver

import "net/http"

// Middleware is a function that wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

var globalMiddlewares []Middleware

// RegisterGlobalMiddleware registers an HTTP middleware that wraps all API requests.
// This is typically called from an init() function in a plugin (e.g. custom auth or governance).
func RegisterGlobalMiddleware(mw Middleware) {
	globalMiddlewares = append(globalMiddlewares, mw)
}

// ApplyGlobalMiddlewares wraps the given handler with all registered global middlewares.
func ApplyGlobalMiddlewares(h http.Handler) http.Handler {
	for i := len(globalMiddlewares) - 1; i >= 0; i-- {
		h = globalMiddlewares[i](h)
	}
	return h
}
