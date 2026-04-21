package server

import (
	"context"
	"net/http"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/handler"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// New creates an HTTP server with all routes registered.
// Cleans up stale discovery runs from previous container lifecycle.
func New(db *database.DB, healthHandler *health.Handler, secretProvider secrets.Provider, authProvider auth.Provider, vectorStore ...vectorstore.Provider) http.Handler {
	var vs vectorstore.Provider
	if len(vectorStore) > 0 {
		vs = vectorStore[0]
	}
	mux := http.NewServeMux()

	// Repos
	projectRepo := database.NewProjectRepository(db)
	discoveryRepo := database.NewDiscoveryRepository(db)
	runRepo := database.NewRunRepository(db)
	feedbackRepo := database.NewFeedbackRepository(db)
	pricingRepo := database.NewPricingRepository(db)
	insightRepo := database.NewInsightRepository(db)
	recommendationRepo := database.NewRecommendationRepository(db)
	domainPackRepo := database.NewDomainPackRepository(db)
	bookmarkListRepo := database.NewBookmarkListRepository(db)
	bookmarkRepo := database.NewBookmarkRepository(db)
	readMarkRepo := database.NewReadMarkRepository(db)

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

	// Policy-plugin reconciliation + post-completion confirmer only
	// run when a non-default Checker is registered. On self-hosted the
	// Noop drops every call, so the background Mongo queries would be
	// pure waste.
	if policy.HasRegisteredChecker() {
		startCounterReconciliation(projectRepo)
		startRunConfirmer(runRepo)
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
	insights := handler.NewInsightsHandler(insightRepo)
	recommendations := handler.NewRecommendationsHandler(recommendationRepo)
	lists := handler.NewListsHandler(bookmarkListRepo, bookmarkRepo, insightRepo, recommendationRepo, discoveryRepo)
	reads := handler.NewReadsHandler(readMarkRepo)
	searchHistoryRepo := database.NewSearchHistoryRepository(db)
	askSessionRepo := database.NewAskSessionRepository(db)
	search := handler.NewSearchHandler(projectRepo, insightRepo, recommendationRepo, searchHistoryRepo, askSessionRepo, secretProvider, vs)

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
	mux.HandleFunc("GET /api/v1/providers/embedding", withRole(viewer, providers.ListEmbeddingProviders))

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

	// Search — viewer
	mux.HandleFunc("POST /api/v1/projects/{id}/search", withRole(viewer, search.Search))
	mux.HandleFunc("POST /api/v1/search", withRole(viewer, search.CrossProjectSearch))
	mux.HandleFunc("POST /api/v1/projects/{id}/ask", withRole(viewer, search.Ask))
	mux.HandleFunc("GET /api/v1/projects/{id}/ask/sessions", withRole(viewer, search.ListAskSessions))
	mux.HandleFunc("GET /api/v1/projects/{id}/ask/sessions/{sessionId}", withRole(viewer, search.GetAskSession))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/ask/sessions/{sessionId}", withRole(admin, search.DeleteAskSession))
	mux.HandleFunc("GET /api/v1/projects/{id}/search/history", withRole(viewer, search.ListHistory))

	// Insights & Recommendations — viewer
	mux.HandleFunc("GET /api/v1/projects/{id}/insights", withRole(viewer, insights.List))
	mux.HandleFunc("GET /api/v1/projects/{id}/insights/{insightId}", withRole(viewer, insights.Get))
	mux.HandleFunc("GET /api/v1/projects/{id}/recommendations", withRole(viewer, recommendations.List))
	mux.HandleFunc("GET /api/v1/projects/{id}/recommendations/{recId}", withRole(viewer, recommendations.Get))

	// Bookmark lists — viewer for read, member for write
	mux.HandleFunc("POST /api/v1/projects/{id}/lists", withRole(member, lists.Create))
	mux.HandleFunc("GET /api/v1/projects/{id}/lists", withRole(viewer, lists.List))
	mux.HandleFunc("GET /api/v1/projects/{id}/lists/{listId}", withRole(viewer, lists.Get))
	mux.HandleFunc("PATCH /api/v1/projects/{id}/lists/{listId}", withRole(member, lists.Update))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/lists/{listId}", withRole(member, lists.Delete))
	mux.HandleFunc("POST /api/v1/projects/{id}/lists/{listId}/items", withRole(member, lists.AddBookmark))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/lists/{listId}/items/{bookmarkId}", withRole(member, lists.RemoveBookmark))
	mux.HandleFunc("GET /api/v1/projects/{id}/bookmarks", withRole(viewer, lists.ListsContaining))

	// Read marks — viewer for read, member for write
	mux.HandleFunc("POST /api/v1/projects/{id}/reads", withRole(member, reads.MarkRead))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/reads", withRole(member, reads.MarkUnread))
	mux.HandleFunc("GET /api/v1/projects/{id}/reads", withRole(viewer, reads.ListReadIDs))

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

// counterReconcileInterval is how often the reconciliation goroutine
// counts projects + data sources and reports them to the policy
// Checker. Five minutes is comfortably longer than any reserve/confirm
// round-trip, so steady-state drift is bounded by one tick.
const counterReconcileInterval = 5 * time.Minute

// startCounterReconciliation launches a goroutine that periodically
// counts the tenant's persistent resources and forwards them to the
// registered policy Checker for reconciliation. Does nothing visible
// on self-hosted because the Noop Checker drops the call.
//
// Shutdown: the goroutine runs on context.Background() for the
// process lifetime. The community API exits by SIGTERM / SIGINT which
// terminates the whole process — no graceful-shutdown path is
// wired because there is no in-flight state worth preserving (the
// next process's startup tick re-reports ground truth). If finer-
// grained shutdown becomes necessary, thread a shared context from
// apiserver.Run() through this function.
func startCounterReconciliation(projectRepo database.ProjectRepo) {
	go func() {
		ctx := context.Background()
		ticker := time.NewTicker(counterReconcileInterval)
		defer ticker.Stop()
		// Fire once on startup so the counter is warm before the first tick.
		reconcileOnce(ctx, projectRepo)
		for range ticker.C {
			reconcileOnce(ctx, projectRepo)
		}
	}()
}

func reconcileOnce(ctx context.Context, projectRepo database.ProjectRepo) {
	projects, err := projectRepo.Count(ctx)
	if err != nil {
		apilog.WithError(err).Warn("counter reconciliation: project count failed")
		return
	}
	dataSources, err := projectRepo.CountWithWarehouse(ctx)
	if err != nil {
		apilog.WithError(err).Warn("counter reconciliation: data-source count failed")
		return
	}
	policy.GetChecker().SyncCounters(ctx, "", policy.CounterSnapshot{
		ProjectsCurrent:    projects,
		DataSourcesCurrent: dataSources,
	})
}

// runConfirmerInterval is how often the confirmer scans for terminal
// runs carrying a reservation. A short tick keeps the concurrent-runs
// counter accurate between a run completing and the cap freeing up
// on the dashboard; the sweeper TTL (minutes) is the slower backstop.
const runConfirmerInterval = 15 * time.Second

func startRunConfirmer(runRepo database.RunRepo) {
	go func() {
		ctx := context.Background()
		ticker := time.NewTicker(runConfirmerInterval)
		defer ticker.Stop()
		confirmTerminalRuns(ctx, runRepo)
		for range ticker.C {
			confirmTerminalRuns(ctx, runRepo)
		}
	}()
}

// policyStatusFromDB maps a DiscoveryRun.Status ("completed", "failed",
// "cancelled") to the policy.RunOutcome vocabulary ("success",
// "failure", "cancelled") used by the handler-side confirm paths, so
// the control plane receives a consistent value regardless of which
// code path closed the reservation.
func policyStatusFromDB(dbStatus string) string {
	switch dbStatus {
	case "completed":
		return "success"
	case "failed":
		return "failure"
	case "cancelled":
		return "cancelled"
	default:
		return dbStatus
	}
}

func confirmTerminalRuns(ctx context.Context, runRepo database.RunRepo) {
	runs, err := runRepo.ListTerminalWithReservation(ctx, 50)
	if err != nil {
		apilog.WithError(err).Warn("run confirmer: list terminal runs failed")
		return
	}
	if len(runs) == 0 {
		return
	}
	checker := policy.GetChecker()
	for _, run := range runs {
		outcome := policy.RunOutcome{Status: policyStatusFromDB(run.Status)}
		if run.CompletedAt != nil {
			outcome.EndedAt = *run.CompletedAt
		}
		if run.Error != "" {
			outcome.Error = run.Error
		}
		if err := checker.ConfirmDiscoveryRunEnded(ctx, run.PolicyReservationID, outcome); err != nil {
			apilog.WithFields(apilog.Fields{"run_id": run.ID, "error": err.Error()}).
				Warn("run confirmer: policy confirm failed; will retry on next tick")
			continue
		}
		if err := runRepo.ClearPolicyReservationID(ctx, run.ID); err != nil {
			apilog.WithFields(apilog.Fields{"run_id": run.ID, "error": err.Error()}).
				Warn("run confirmer: clearing reservation id failed; may double-confirm on next tick")
		}
	}
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
