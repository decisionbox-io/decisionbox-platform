// Package askoverride owns the process-global override slot for the
// community `POST /api/v1/projects/{id}/ask` route. Plugins register a
// replacement handler via apiserver.RegisterAskOverride (a thin wrapper
// around this package). The community route handler reads the slot on
// every request and defers to the override when set.
//
// This lives in its own leaf package so both apiserver (which exports the
// registration function) and the community handler / server (which read
// the registered handler) can depend on it without an import cycle.
package askoverride

import (
	"net/http"
	"sync"
)

var (
	mu       sync.RWMutex
	override http.Handler
)

// Register installs h as the override handler. nil panics; double
// registration panics. Both are programmer errors that would silently
// shadow earlier configuration.
func Register(h http.Handler) {
	if h == nil {
		panic("askoverride: Register called with nil handler")
	}
	mu.Lock()
	defer mu.Unlock()
	if override != nil {
		panic("askoverride: Register called twice")
	}
	override = h
}

// Get returns the currently registered override and whether one is set.
// Safe to call from any goroutine.
func Get() (http.Handler, bool) {
	mu.RLock()
	defer mu.RUnlock()
	return override, override != nil
}

// resetForTest clears the slot. Test-only.
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	override = nil
}

// ResetForTest is the exported test helper. Production code MUST NOT call it.
func ResetForTest() {
	resetForTest()
}
