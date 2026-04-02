package warehouse

import (
	"sync"
)

// Middleware allows wrapping a warehouse Provider with additional functionality
// (e.g., logging, metrics, governance, or redaction).
type Middleware func(Provider) Provider

type namedMiddleware struct {
	name string
	mw   Middleware
}

var (
	middlewareMu sync.RWMutex
	middlewares  []namedMiddleware
	middlewareNames = make(map[string]bool)
)

// RegisterMiddleware registers a warehouse provider middleware by name.
// Middlewares are applied in registration order.
// This is typically called from an init() function in a plugin.
func RegisterMiddleware(name string, mw Middleware) {
	middlewareMu.Lock()
	defer middlewareMu.Unlock()
	if mw == nil {
		panic("warehouse: RegisterMiddleware middleware is nil for " + name)
	}
	if middlewareNames[name] {
		panic("warehouse: RegisterMiddleware called twice for " + name)
	}
	middlewareNames[name] = true
	middlewares = append(middlewares, namedMiddleware{name: name, mw: mw})
}

// ApplyMiddleware applies all registered middlewares to a provider
// in registration order.
func ApplyMiddleware(p Provider) Provider {
	middlewareMu.RLock()
	defer middlewareMu.RUnlock()
	for _, nm := range middlewares {
		p = nm.mw(p)
	}
	return p
}
