package warehouse

import (
	"sync"
)

// Middleware allows wrapping a warehouse Provider with additional functionality
// (e.g., logging, metrics, governance, or redaction).
type Middleware func(Provider) Provider

var (
	middlewareMu sync.RWMutex
	middlewares  = make(map[string]Middleware)
)

// RegisterMiddleware registers a warehouse provider middleware by name.
// This is typically called from an init() function in a plugin (e.g. custom auth or governance).
func RegisterMiddleware(name string, mw Middleware) {
	middlewareMu.Lock()
	defer middlewareMu.Unlock()
	if mw == nil {
		panic("warehouse: RegisterMiddleware is nil for " + name)
	}
	if _, exists := middlewares[name]; exists {
		panic("warehouse: RegisterMiddleware called twice for " + name)
	}
	middlewares[name] = mw
}

// ApplyMiddleware applies all registered middlewares to a provider.
func ApplyMiddleware(p Provider) Provider {
	middlewareMu.RLock()
	defer middlewareMu.RUnlock()
	for _, mw := range middlewares {
		p = mw(p)
	}
	return p
}
