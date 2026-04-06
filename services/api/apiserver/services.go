package apiserver

import (
	"sync"

	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
)

// Services provides access to shared infrastructure for enterprise plugins.
// Populated during Run() after all connections are established.
// Enterprise plugins access these at request time (not init time) via GetServices().
type Services struct {
	DB             *database.DB
	SecretProvider gosecrets.Provider
	VectorStore    vectorstore.Provider // nil if Qdrant not configured
}

var (
	servicesMu     sync.RWMutex
	sharedServices *Services
)

// RegisterServices makes shared infrastructure available to enterprise plugins.
// Called during Run() after all connections are established.
func RegisterServices(s *Services) {
	servicesMu.Lock()
	defer servicesMu.Unlock()
	sharedServices = s
}

// GetServices returns the shared infrastructure, or nil if not yet initialized.
// Enterprise plugins should call this at request time, not init time.
func GetServices() *Services {
	servicesMu.RLock()
	defer servicesMu.RUnlock()
	return sharedServices
}
