package server

import (
	"context"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/handler"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// New creates an HTTP server with all routes registered.
// Cleans up stale discovery runs from previous container lifecycle.
func New(db *database.DB, healthHandler *health.Handler, secretProvider secrets.Provider, authProvider auth.Provider) http.Handler {
	mux := http.NewServeMux()

	// Repos
	projectRepo := database.NewProjectRepository(db)
	discoveryRepo := database.NewDiscoveryRepository(db)
	runRepo := database.NewRunRepository(db)
	feedbackRepo := database.NewFeedbackRepository(db)
	pricingRepo := database.NewPricingRepository(db)
	domainPackRepo := database.NewDomainPackRepository(db)

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

	// Seed built-in domain packs (if not yet in MongoDB)
	handler.SeedBuiltInPacks(context.Background(), domainPackRepo)

	// Handlers
	providers := handler.NewProvidersHandler()
	domains := handler.NewDomainsHandler(domainPackRepo)
	domainPacks := handler.NewDomainPacksHandler(domainPackRepo)
	projects := handler.NewProjectsHandler(projectRepo, domainPackRepo)
	discoveries := handler.NewDiscoveriesHandler(discoveryRepo, projectRepo, runRepo, agentRunner)
	feedback := handler.NewFeedbackHandler(feedbackRepo)
	pricing := handler.NewPricingHandler(pricingRepo)
	estimate := handler.NewEstimateHandler(projectRepo)
	secretsHandler := handler.NewSecretsHandler(secretProvider, projectRepo)
	testConn := handler.NewTestConnectionHandler(projectRepo, agentRunner)

	// RBAC helpers — wrap a handler with role-based access control.
	// With NoAuth (default), all requests get "admin" role — all routes pass.
	viewer := auth.RequireRole("viewer")
	member := auth.RequireRole("member")
	admin := auth.RequireRole("admin")

	withRole := func(mw func(http.Handler) http.Handler, fn http.HandlerFunc) http.HandlerFunc {
		wrapped := mw(fn)
		return wrapped.ServeHTTP
	}

	// Health endpoints — no auth required (separate mux for K8s probes)
	healthMux := http.NewServeMux()
	if healthHandler != nil {
		healthMux.HandleFunc("GET /health", healthHandler.LivenessHandler())
		healthMux.HandleFunc("GET /health/ready", healthHandler.ReadinessHandler())
		healthMux.HandleFunc("GET /api/v1/health", healthHandler.ReadinessHandler())
	} else {
		healthMux.HandleFunc("GET /api/v1/health", handler.HealthCheck)
	}

	// Providers — viewer
	mux.HandleFunc("GET /api/v1/providers/llm", withRole(viewer, providers.ListLLMProviders))
	mux.HandleFunc("GET /api/v1/providers/warehouse", withRole(viewer, providers.ListWarehouseProviders))

	// Domain packs — viewer for read, admin for write
	mux.HandleFunc("GET /api/v1/domain-packs", withRole(viewer, domainPacks.List))
	mux.HandleFunc("GET /api/v1/domain-packs/{slug}/export", withRole(viewer, domainPacks.Export))
	mux.HandleFunc("GET /api/v1/domain-packs/{slug}", withRole(viewer, domainPacks.Get))
	mux.HandleFunc("POST /api/v1/domain-packs/import", withRole(admin, domainPacks.Import))
	mux.HandleFunc("POST /api/v1/domain-packs", withRole(admin, domainPacks.Create))
	mux.HandleFunc("PUT /api/v1/domain-packs/{slug}", withRole(admin, domainPacks.Update))
	mux.HandleFunc("DELETE /api/v1/domain-packs/{slug}", withRole(admin, domainPacks.Delete))

	// Domains — viewer (backward-compatible endpoints for project creation flow)
	mux.HandleFunc("GET /api/v1/domains", withRole(viewer, domains.ListDomains))
	mux.HandleFunc("GET /api/v1/domains/{domain}/categories", withRole(viewer, domains.ListCategories))
	mux.HandleFunc("GET /api/v1/domains/{domain}/categories/{category}/schema", withRole(viewer, domains.GetProfileSchema))
	mux.HandleFunc("GET /api/v1/domains/{domain}/categories/{category}/areas", withRole(viewer, domains.GetAnalysisAreas))

	// Projects — viewer for read, member for write, admin for delete
	mux.HandleFunc("POST /api/v1/projects", withRole(member, projects.Create))
	mux.HandleFunc("GET /api/v1/projects", withRole(viewer, projects.List))
	mux.HandleFunc("GET /api/v1/projects/{id}", withRole(viewer, projects.Get))
	mux.HandleFunc("PUT /api/v1/projects/{id}", withRole(member, projects.Update))
	mux.HandleFunc("DELETE /api/v1/projects/{id}", withRole(admin, projects.Delete))

	// Prompts — viewer for read, member for write
	mux.HandleFunc("GET /api/v1/projects/{id}/prompts", withRole(viewer, handler.GetPrompts(projectRepo, domainPackRepo)))
	mux.HandleFunc("PUT /api/v1/projects/{id}/prompts", withRole(member, handler.UpdatePrompts(projectRepo)))

	// Discoveries — member for trigger, viewer for read
	mux.HandleFunc("POST /api/v1/projects/{id}/discover", withRole(member, discoveries.TriggerDiscovery))
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries", withRole(viewer, discoveries.List))
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries/latest", withRole(viewer, discoveries.GetLatest))
	mux.HandleFunc("GET /api/v1/projects/{id}/discoveries/{date}", withRole(viewer, discoveries.GetByDate))
	mux.HandleFunc("GET /api/v1/projects/{id}/status", withRole(viewer, discoveries.GetStatus))

	// Single discovery by ID
	mux.HandleFunc("GET /api/v1/discoveries/{id}", withRole(viewer, discoveries.GetDiscoveryByID))

	// Runs — viewer for read, admin for cancel
	mux.HandleFunc("GET /api/v1/runs/{runId}", withRole(viewer, discoveries.GetRun))
	mux.HandleFunc("DELETE /api/v1/runs/{runId}", withRole(admin, discoveries.CancelRun))

	// Feedback — member for submit, viewer for read, admin for delete
	mux.HandleFunc("POST /api/v1/discoveries/{runId}/feedback", withRole(member, feedback.Submit))
	mux.HandleFunc("GET /api/v1/discoveries/{runId}/feedback", withRole(viewer, feedback.List))
	mux.HandleFunc("DELETE /api/v1/feedback/{id}", withRole(admin, feedback.Delete))

	// Pricing — viewer for read, admin for update
	mux.HandleFunc("GET /api/v1/pricing", withRole(viewer, pricing.Get))
	mux.HandleFunc("PUT /api/v1/pricing", withRole(admin, pricing.Update))

	// Cost estimation — member
	mux.HandleFunc("POST /api/v1/projects/{id}/discover/estimate", withRole(member, estimate.Estimate))

	// Connection testing — admin
	mux.HandleFunc("POST /api/v1/projects/{id}/test/warehouse", withRole(admin, testConn.TestWarehouse))
	mux.HandleFunc("POST /api/v1/projects/{id}/test/llm", withRole(admin, testConn.TestLLM))

	// Secrets — admin
	if secretProvider != nil {
		mux.HandleFunc("PUT /api/v1/projects/{id}/secrets/{key}", withRole(admin, secretsHandler.Set))
		mux.HandleFunc("GET /api/v1/projects/{id}/secrets", withRole(admin, secretsHandler.List))
	}

	// Combine: health (no auth) + app (with auth + RBAC)
	root := http.NewServeMux()
	root.Handle("/health", healthMux)
	root.Handle("/health/", healthMux)
	root.Handle("/api/v1/health", healthMux)
	root.Handle("/", authProvider.Middleware()(mux))

	// Middleware chain: CORS → Logging → Auth → RBAC → Router
	return corsMiddleware(handler.LoggingMiddleware(root))
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
