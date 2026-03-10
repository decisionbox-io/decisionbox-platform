package server

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/handler"
)

// New creates an HTTP server with all routes registered.
func New(db *database.DB) http.Handler {
	mux := http.NewServeMux()

	// Repos
	projectRepo := database.NewProjectRepository(db)
	discoveryRepo := database.NewDiscoveryRepository(db)

	// Handlers
	providers := handler.NewProvidersHandler()
	domains := handler.NewDomainsHandler()
	projects := handler.NewProjectsHandler(projectRepo)
	discoveries := handler.NewDiscoveriesHandler(discoveryRepo, projectRepo)

	// Health
	mux.HandleFunc("GET /api/v1/health", handler.HealthCheck)

	// Providers
	mux.HandleFunc("GET /api/v1/providers/llm", providers.ListLLMProviders)
	mux.HandleFunc("GET /api/v1/providers/warehouse", providers.ListWarehouseProviders)

	// Domains
	mux.HandleFunc("GET /api/v1/domains", domains.ListDomains)
	mux.HandleFunc("GET /api/v1/domains/{domain}/categories", domains.ListCategories)
	mux.HandleFunc("GET /api/v1/domains/{domain}/categories/{category}/schema", domains.GetProfileSchema)
	mux.HandleFunc("GET /api/v1/domains/{domain}/categories/{category}/areas", domains.GetAnalysisAreas)

	// Projects
	mux.HandleFunc("POST /api/v1/projects", projects.Create)
	mux.HandleFunc("GET /api/v1/projects", projects.List)
	mux.HandleFunc("GET /api/v1/projects/{id}", projects.Get)
	mux.HandleFunc("PUT /api/v1/projects/{id}", projects.Update)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", projects.Delete)

	// Discoveries
	mux.HandleFunc("POST /api/v1/projects/{id}/discover", discoveries.TriggerDiscovery)
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries", discoveries.List)
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries/latest", discoveries.GetLatest)
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries/{date}", discoveries.GetByDate)
	mux.HandleFunc("GET /api/v1/projects/{id}/status", discoveries.GetStatus)

	// CORS middleware for dashboard
	return corsMiddleware(mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
