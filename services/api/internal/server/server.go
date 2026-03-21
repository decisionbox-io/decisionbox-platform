package server

import (
	"context"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/handler"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// New creates an HTTP server with all routes registered.
// Cleans up stale discovery runs from previous container lifecycle.
func New(db *database.DB, healthHandler *health.Handler, secretProvider secrets.Provider) http.Handler {
	mux := http.NewServeMux()

	// Repos
	projectRepo := database.NewProjectRepository(db)
	discoveryRepo := database.NewDiscoveryRepository(db)
	runRepo := database.NewRunRepository(db)
	feedbackRepo := database.NewFeedbackRepository(db)
	pricingRepo := database.NewPricingRepository(db)

	// Clean up stale runs from previous container lifecycle
	cleaned, err := runRepo.CleanupStaleRuns(context.Background())
	if err != nil {
		apilog.WithError(err).Warn("Failed to cleanup stale runs")
	} else if cleaned > 0 {
		apilog.WithField("count", cleaned).Info("Cleaned up stale discovery runs")
	}

	// Create agent runner (subprocess or K8s based on RUNNER_MODE env)
	runnerCfg := runner.LoadConfig()
	agentRunner, err := runner.New(runnerCfg)
	if err != nil {
		apilog.WithError(err).Error("Failed to create agent runner")
		// Fall back to subprocess mode
		agentRunner = runner.NewSubprocessRunner()
	}

	// Seed pricing from registered providers (if not yet in MongoDB)
	handler.SeedPricingFromProviders(context.Background(), pricingRepo)

	// Handlers
	providers := handler.NewProvidersHandler()
	domains := handler.NewDomainsHandler()
	projects := handler.NewProjectsHandler(projectRepo)
	discoveries := handler.NewDiscoveriesHandler(discoveryRepo, projectRepo, runRepo, agentRunner)
	feedback := handler.NewFeedbackHandler(feedbackRepo)
	pricing := handler.NewPricingHandler(pricingRepo)
	estimate := handler.NewEstimateHandler(projectRepo)
	secretsHandler := handler.NewSecretsHandler(secretProvider, projectRepo)
	testConn := handler.NewTestConnectionHandler(projectRepo, agentRunner)

	// Health endpoints
	// /health and /health/ready — for K8s liveness/readiness probes (from go-common)
	// /api/v1/health — for dashboard and API consumers
	if healthHandler != nil {
		mux.HandleFunc("GET /health", healthHandler.LivenessHandler())
		mux.HandleFunc("GET /health/ready", healthHandler.ReadinessHandler())
		mux.HandleFunc("GET /api/v1/health", healthHandler.ReadinessHandler())
	} else {
		mux.HandleFunc("GET /api/v1/health", handler.HealthCheck)
	}

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

	// Prompts
	mux.HandleFunc("GET /api/v1/projects/{id}/prompts", handler.GetPrompts(projectRepo))
	mux.HandleFunc("PUT /api/v1/projects/{id}/prompts", handler.UpdatePrompts(projectRepo))

	// Discoveries
	mux.HandleFunc("POST /api/v1/projects/{id}/discover", discoveries.TriggerDiscovery)
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries", discoveries.List)
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries/latest", discoveries.GetLatest)
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries/{date}", discoveries.GetByDate)
	mux.HandleFunc("GET /api/v1/projects/{id}/status", discoveries.GetStatus)

	// Single discovery by ID
	mux.HandleFunc("GET /api/v1/discoveries/{id}", discoveries.GetDiscoveryByID)

	// Runs (live status + cancel)
	mux.HandleFunc("GET /api/v1/runs/{runId}", discoveries.GetRun)
	mux.HandleFunc("DELETE /api/v1/runs/{runId}", discoveries.CancelRun)

	// Feedback
	mux.HandleFunc("POST /api/v1/discoveries/{runId}/feedback", feedback.Submit)
	mux.HandleFunc("GET /api/v1/discoveries/{runId}/feedback", feedback.List)
	mux.HandleFunc("DELETE /api/v1/feedback/{id}", feedback.Delete)

	// Pricing
	mux.HandleFunc("GET /api/v1/pricing", pricing.Get)
	mux.HandleFunc("PUT /api/v1/pricing", pricing.Update)

	// Cost estimation
	mux.HandleFunc("POST /api/v1/projects/{id}/discover/estimate", estimate.Estimate)

	// Connection testing (runs agent subprocess with --test-connection)
	mux.HandleFunc("POST /api/v1/projects/{id}/test/warehouse", testConn.TestWarehouse)
	mux.HandleFunc("POST /api/v1/projects/{id}/test/llm", testConn.TestLLM)

	// Secrets (per-project, no delete via API)
	if secretProvider != nil {
		mux.HandleFunc("PUT /api/v1/projects/{id}/secrets/{key}", secretsHandler.Set)
		mux.HandleFunc("GET /api/v1/projects/{id}/secrets", secretsHandler.List)
	}

	// Middleware chain: CORS → Logging → Router
	return corsMiddleware(handler.LoggingMiddleware(mux))
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
