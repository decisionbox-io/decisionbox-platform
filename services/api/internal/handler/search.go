package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/google/uuid"
)

// askKnowledgeTopK is the maximum number of project knowledge source chunks
// to include in /ask responses. Tuned to give the LLM broad context while
// staying well within typical context windows.
const (
	askKnowledgeTopK     = 8
	askKnowledgeMinScore = 0.4
	// askKnowledgeTimeout caps knowledge-source retrieval (embedding + vector
	// search) so a slow provider cannot stall the /ask handler indefinitely.
	// Mirrors the agent's knowledgeMaxRetrievalPerPhase.
	askKnowledgeTimeout = 3 * time.Second
)

// SearchHandler handles semantic search endpoints.
type SearchHandler struct {
	projectRepo    database.ProjectRepo
	insightRepo    database.InsightRepo
	recRepo        database.RecommendationRepo
	historyRepo    database.SearchHistoryRepo
	sessionRepo    database.AskSessionRepo
	secretProvider gosecrets.Provider
	vectorStore    vectorstore.Provider // nil if Qdrant not configured
}

func NewSearchHandler(
	projectRepo database.ProjectRepo,
	insightRepo database.InsightRepo,
	recRepo database.RecommendationRepo,
	historyRepo database.SearchHistoryRepo,
	sessionRepo database.AskSessionRepo,
	secretProvider gosecrets.Provider,
	vectorStore vectorstore.Provider,
) *SearchHandler {
	return &SearchHandler{
		projectRepo:    projectRepo,
		insightRepo:    insightRepo,
		recRepo:        recRepo,
		historyRepo:    historyRepo,
		sessionRepo:    sessionRepo,
		secretProvider: secretProvider,
		vectorStore:    vectorStore,
	}
}

type searchRequest struct {
	Query    string        `json:"query"`
	Types    []string      `json:"types,omitempty"`
	Limit    int           `json:"limit,omitempty"`
	MinScore float64       `json:"min_score,omitempty"`
	Filters  searchFilters `json:"filters,omitempty"`
}

type searchFilters struct {
	Severity     string `json:"severity,omitempty"`
	AnalysisArea string `json:"analysis_area,omitempty"`
}

type searchResponse struct {
	Results        []searchResultItem `json:"results"`
	EmbeddingModel string             `json:"embedding_model"`
}

type searchResultItem struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Score        float64 `json:"score"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Severity     string  `json:"severity,omitempty"`
	AnalysisArea string  `json:"analysis_area,omitempty"`
	DiscoveryID  string  `json:"discovery_id"`
	DiscoveredAt string  `json:"discovered_at,omitempty"`
	ProjectID    string  `json:"project_id,omitempty"`
	ProjectName  string  `json:"project_name,omitempty"`
	// Validation carries the trimmed validation verdict so list surfaces
	// (search, cross-project search) can render the status badge inline,
	// matching the insights list and discovery results. Nil when the doc
	// was never validated or for non-validatable result types (e.g.
	// knowledge source chunks). See searchValidationSummary for why this
	// is not the full verifier/refuter payload.
	Validation *searchValidationSummary `json:"validation,omitempty"`
}

// searchValidationSummary is the trimmed validation verdict returned on
// list/search surfaces. The list UI renders only the verdict badge, so
// we expose just the canonical combined status (and the legacy status
// as a fallback) — never the full verifier/refuter payload (claim
// verdicts, evidence rows, SQL, token metadata), which would bloat
// broad search responses and leak raw validation evidence.
type searchValidationSummary struct {
	Combined string `json:"combined,omitempty"`
	Status   string `json:"status,omitempty"`
}

// validationSummary trims a full InsightValidation down to the fields
// the badge needs. Returns nil when there is no verdict to show so the
// field is omitted and the client falls back to its neutral state.
func validationSummary(v *commonmodels.InsightValidation) *searchValidationSummary {
	if v == nil || (v.Combined == "" && v.Status == "") {
		return nil
	}
	return &searchValidationSummary{
		Combined: string(v.Combined),
		Status:   v.Status,
	}
}

// Search performs project-scoped semantic search.
// POST /api/v1/projects/{id}/search
func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project ID is required")
		return
	}

	if h.vectorStore == nil {
		writeError(w, http.StatusServiceUnavailable, "vector search is not configured (QDRANT_URL not set)")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 40<<20) // 40 MB limit

	var req searchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	ctx := r.Context()

	// Load project to get embedding config
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if project.Embedding.Provider == "" {
		writeError(w, http.StatusBadRequest, "embedding provider not configured for this project")
		return
	}

	// Create embedding provider for this project
	embProvider, err := h.createEmbeddingProvider(ctx, project.Embedding.Provider, project.Embedding.Model, projectID, project.Embedding.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create embedding provider")
		return
	}

	// Embed the query
	vectors, err := embProvider.Embed(ctx, []string{req.Query})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to embed query")
		return
	}

	// Search Qdrant
	searchResults, err := h.vectorStore.Search(ctx, vectors[0], vectorstore.SearchOpts{
		ProjectIDs:     []string{projectID},
		Types:          req.Types,
		EmbeddingModel: embProvider.ModelName(),
		Severity:       req.Filters.Severity,
		AnalysisArea:   req.Filters.AnalysisArea,
		Limit:          req.Limit,
		MinScore:       req.MinScore,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	// Fetch full documents from MongoDB and build response
	items := h.enrichResults(ctx, searchResults)

	// Save search history (fire and forget — background context so it survives request cancellation)
	bgCtx, bgCancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		defer bgCancel()
		h.saveSearchHistory(bgCtx, projectID, req, items)
	}()

	writeJSON(w, http.StatusOK, searchResponse{
		Results:        items,
		EmbeddingModel: embProvider.ModelName(),
	})
}

// createEmbeddingProvider creates an embedding provider for a project.
// The projectConfig map carries non-credential provider settings the
// dashboard saved (auth_method, project_id, location, region, role_arn,
// …); the agent's initEmbeddingProvider does the same merge for the
// indexing path so search/ask see the identical factory wiring.
func (h *SearchHandler) createEmbeddingProvider(ctx context.Context, providerName, model, projectID string, projectConfig map[string]string) (goembedding.Provider, error) {
	cfg := goembedding.ProviderConfig{
		"model": model,
	}
	for k, v := range projectConfig {
		cfg[k] = v
	}
	// Per-project secret wins, else the EMBEDDING_API_KEY env fallback:
	// managed-inference gateway mode stores no per-project AI secret, so
	// this API-side search path (which embeds the query) must reach the
	// gateway via env or it would build a credential-less provider and
	// fail. The resolution order lives in gosecrets.ResolveCredential,
	// shared with the agent and the enterprise plugins.
	cred, _, _ := gosecrets.ResolveCredential(ctx, h.secretProvider, projectID, "embedding-credentials", "EMBEDDING_API_KEY")
	if cred != "" {
		cfg["credentials_json"] = cred
	}
	return goembedding.NewProvider(providerName, cfg)
}

// enrichResults fetches full documents from MongoDB for each search result.
func (h *SearchHandler) enrichResults(ctx context.Context, results []vectorstore.SearchResult) []searchResultItem {
	items := make([]searchResultItem, 0, len(results))

	for _, sr := range results {
		docType, _ := sr.Payload["type"].(string)

		item := searchResultItem{
			ID:    sr.ID,
			Type:  docType,
			Score: sr.Score,
		}

		switch docType {
		case "insight":
			if ins, err := h.insightRepo.GetByID(ctx, sr.ID); err == nil {
				item.Name = ins.Name
				item.Description = ins.Description
				item.Severity = ins.Severity
				item.AnalysisArea = ins.AnalysisArea
				item.DiscoveryID = ins.DiscoveryID
				item.DiscoveredAt = ins.DiscoveredAt.Format(time.RFC3339)
				item.ProjectID = ins.ProjectID
				item.Validation = validationSummary(ins.Validation)
			}
		case "recommendation":
			if rec, err := h.recRepo.GetByID(ctx, sr.ID); err == nil {
				item.Name = rec.Title
				item.Description = rec.Description
				item.DiscoveryID = rec.DiscoveryID
				item.ProjectID = rec.ProjectID
				item.Validation = validationSummary(rec.Validation)
			}
		}

		// Fall back to vector payload for discovery_id if MongoDB document didn't have it
		if item.DiscoveryID == "" {
			if did, ok := sr.Payload["discovery_id"].(string); ok {
				item.DiscoveryID = did
			}
		}

		items = append(items, item)
	}

	return items
}

// hydrateSeed resolves the "Ask about this" seed for a new conversation into the
// persisted session seed. It reads the insight / recommendation and verifies it
// belongs to projectID (InsightRepo.GetByID / RecommendationRepo.GetByID are not
// project-scoped, so a client-supplied id from another project must be rejected).
// Falls back to the client-supplied text only when the id can't be resolved.
// Returns nil when there is nothing to ground on.
func (h *SearchHandler) hydrateSeed(ctx context.Context, projectID string, in *seedContextReq) *commonmodels.AskSessionSeed {
	if in == nil || in.ID == "" {
		return nil
	}
	kind := in.Type
	if kind != "insight" && kind != "recommendation" {
		return nil
	}
	seed := &commonmodels.AskSessionSeed{Type: kind, ID: in.ID}
	switch kind {
	case "insight":
		if ins, err := h.insightRepo.GetByID(ctx, in.ID); err == nil && ins != nil && ins.ProjectID == projectID {
			seed.Label = ins.Name
			seed.Text = boundedSeedText(ins.Description, askSeedTextCap)
			return seed
		}
	case "recommendation":
		if rec, err := h.recRepo.GetByID(ctx, in.ID); err == nil && rec != nil && rec.ProjectID == projectID {
			seed.Label = rec.Title
			seed.Text = boundedSeedText(rec.Description, askSeedTextCap)
			return seed
		}
	}
	// Unresolved id (unknown / wrong project / non-entity page) — fall back to
	// the client text, which is never treated as authoritative.
	if t := strings.TrimSpace(in.Text); t != "" {
		seed.Text = boundedSeedText(t, askSeedTextCap)
		return seed
	}
	return nil
}

// boundedSeedText trims and rune-caps seed text.
func boundedSeedText(s string, limit int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// saveSearchHistory records the search for analytics.
func (h *SearchHandler) saveSearchHistory(ctx context.Context, projectID string, req searchRequest, items []searchResultItem) {
	topIDs := make([]string, 0, len(items))
	for i, item := range items {
		if i >= 5 {
			break
		}
		topIDs = append(topIDs, item.ID)
	}

	var topScore float64
	if len(items) > 0 {
		topScore = items[0].Score
	}

	entry := &commonmodels.SearchHistory{
		ID:             uuid.New().String(),
		UserID:         "anonymous", // NoAuth default — enterprise overrides
		ProjectID:      projectID,
		Query:          req.Query,
		Type:           "search",
		ResultsCount:   len(items),
		TopResultIDs:   topIDs,
		TopResultScore: topScore,
		CreatedAt:      time.Now(),
	}

	if err := h.historyRepo.Save(ctx, entry); err != nil {
		apilog.WithError(err).Warn("Failed to save search history")
	}
}

// crossSearchRequest is the request body for cross-project search.
type crossSearchRequest struct {
	Query          string   `json:"query"`
	EmbeddingModel string   `json:"embedding_model"`
	Types          []string `json:"types,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	MinScore       float64  `json:"min_score,omitempty"`
}

type crossSearchResponse struct {
	Results          []searchResultItem `json:"results"`
	ProjectsSearched int                `json:"projects_searched"`
	ProjectsExcluded int                `json:"projects_excluded"`
	ExcludedReason   string             `json:"excluded_reason,omitempty"`
}

// CrossProjectSearch performs search across all projects using the same embedding model.
// POST /api/v1/search
func (h *SearchHandler) CrossProjectSearch(w http.ResponseWriter, r *http.Request) {
	if h.vectorStore == nil {
		writeError(w, http.StatusServiceUnavailable, "vector search is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 40<<20) // 40 MB limit

	var req crossSearchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.EmbeddingModel == "" {
		writeError(w, http.StatusBadRequest, "embedding_model is required for cross-project search")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 200 {
		req.Limit = 200
	}

	ctx := r.Context()

	// List all projects
	allProjects, err := h.projectRepo.List(ctx, 1000, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	// Filter projects by embedding model
	var matchingIDs []string
	excluded := 0
	for _, p := range allProjects {
		if p.Embedding.Model == req.EmbeddingModel {
			matchingIDs = append(matchingIDs, p.ID)
		} else if p.Embedding.Provider != "" {
			excluded++
		}
	}

	if len(matchingIDs) == 0 {
		writeJSON(w, http.StatusOK, crossSearchResponse{
			Results:          []searchResultItem{},
			ProjectsExcluded: excluded,
			ExcludedReason:   "different embedding model",
		})
		return
	}

	// Find a project with a valid embedding provider to embed the query
	var embProvider goembedding.Provider
	for _, p := range allProjects {
		if p.Embedding.Model == req.EmbeddingModel && p.Embedding.Provider != "" {
			embProvider, err = h.createEmbeddingProvider(ctx, p.Embedding.Provider, req.EmbeddingModel, p.ID, p.Embedding.Config)
			if err == nil {
				break
			}
		}
	}
	if embProvider == nil {
		writeError(w, http.StatusInternalServerError, "failed to create embedding provider")
		return
	}

	vectors, err := embProvider.Embed(ctx, []string{req.Query})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to embed query")
		return
	}

	results, err := h.vectorStore.Search(ctx, vectors[0], vectorstore.SearchOpts{
		ProjectIDs:     matchingIDs,
		Types:          req.Types,
		EmbeddingModel: req.EmbeddingModel,
		Limit:          req.Limit,
		MinScore:       req.MinScore,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	items := h.enrichResults(ctx, results)

	// Enrich with project names
	projectNames := make(map[string]string)
	for _, p := range allProjects {
		projectNames[p.ID] = p.Name
	}
	for i := range items {
		items[i].ProjectName = projectNames[items[i].ProjectID]
	}

	writeJSON(w, http.StatusOK, crossSearchResponse{
		Results:          items,
		ProjectsSearched: len(matchingIDs),
		ProjectsExcluded: excluded,
		ExcludedReason:   "different embedding model",
	})
}

// askRequest is the request body for RAG Q&A.
type askRequest struct {
	Question  string `json:"question"`
	Limit     int    `json:"limit,omitempty"`
	SessionID string `json:"session_id,omitempty"` // existing session for multi-turn; empty = new session
	// SeedContext anchors a NEW conversation to one insight / recommendation the
	// user launched "Ask about this" from. Honored only when creating a session
	// (a follow-up reuses the seed persisted on the session). Grounding text is
	// hydrated server-side by {Type,ID} scoped to the project. nil = generic chat.
	SeedContext *seedContextReq `json:"seed_context,omitempty"`
}

// seedContextReq is the client seed reference; Text is a fallback used only when
// the id can't be resolved in this project.
type seedContextReq struct {
	Type string `json:"type"` // "insight" | "recommendation"
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
}

// askSeedTextCap bounds the hydrated seed text fed into the prompt / persisted
// on the session so a long description can't dominate the context window.
const askSeedTextCap = 800

type askResponse struct {
	Answer       string             `json:"answer"`
	Sources      []searchResultItem `json:"sources"`
	Model        string             `json:"model"`
	SessionID    string             `json:"session_id"`
	InputTokens  int                `json:"input_tokens,omitempty"`
	OutputTokens int                `json:"output_tokens,omitempty"`
}

// Ask performs RAG Q&A: search + LLM synthesis.
// POST /api/v1/projects/{id}/ask
func (h *SearchHandler) Ask(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project ID is required")
		return
	}

	if h.vectorStore == nil {
		writeError(w, http.StatusServiceUnavailable, "vector search is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 40<<20) // 40 MB limit

	var req askRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 5
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	ctx := r.Context()

	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if project.Embedding.Provider == "" {
		writeErrorCode(w, http.StatusPreconditionFailed,
			ErrCodeEmbeddingNotConfigured,
			"embedding provider not configured for this project",
			"")
		return
	}

	// Gate /ask on schema-index readiness (plan §8.6 — same contract as
	// POST /discover). Empty status is treated as pre-migration and the
	// user is pointed at /reindex to unblock. Pending/indexing returns
	// 409 so the dashboard can surface the banner without a toast; the
	// client polls /schema-index/status for the next state.
	switch project.SchemaIndexStatus {
	case models.SchemaIndexStatusReady:
		// ok — proceed
	case models.SchemaIndexStatusPendingIndexing, models.SchemaIndexStatusIndexing:
		writeError(w, http.StatusConflict, "schema index is not ready yet — poll /api/v1/projects/"+projectID+"/schema-index/status")
		return
	case models.SchemaIndexStatusFailed:
		writeError(w, http.StatusConflict, "schema indexing failed: "+project.SchemaIndexError+" — click Retry indexing in project settings")
		return
	case "":
		// Pre-migration project — skipped by the bootstrap sweep because
		// the migration worker only runs on startup, or because this
		// instance hasn't rebooted since shipping the feature. Users can
		// unblock with POST /reindex.
		writeError(w, http.StatusConflict, "project has not been indexed yet — trigger POST /api/v1/projects/"+projectID+"/reindex first")
		return
	}

	// Embed the question
	embProvider, err := h.createEmbeddingProvider(ctx, project.Embedding.Provider, project.Embedding.Model, projectID, project.Embedding.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create embedding provider")
		return
	}

	vectors, err := embProvider.Embed(ctx, []string{req.Question})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to embed question")
		return
	}

	// Search for relevant context
	searchResults, err := h.vectorStore.Search(ctx, vectors[0], vectorstore.SearchOpts{
		ProjectIDs:     []string{projectID},
		EmbeddingModel: embProvider.ModelName(),
		Limit:          req.Limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "context search failed")
		return
	}

	insights := h.enrichResults(ctx, searchResults)

	// Retrieve project knowledge source chunks (no-op without enterprise plugin
	// or when no sources are indexed). Use top-K=8 to give the LLM broad context.
	retrieveCtx, cancelRetrieve := context.WithTimeout(ctx, askKnowledgeTimeout)
	knowledgeChunks, knowledgeErr := gosources.GetProvider().RetrieveContext(retrieveCtx, projectID, req.Question, gosources.RetrieveOpts{
		Limit:    askKnowledgeTopK,
		MinScore: askKnowledgeMinScore,
	})
	cancelRetrieve()
	if knowledgeErr != nil {
		apilog.WithError(knowledgeErr).Warn("Knowledge source retrieval failed for /ask; continuing without source context")
		knowledgeChunks = nil
	}

	if len(insights) == 0 && len(knowledgeChunks) == 0 {
		writeJSON(w, http.StatusOK, askResponse{
			Answer:    "No relevant insights or knowledge sources found for this question. Try running a discovery first, attaching documents in Knowledge Sources, or rephrasing your question.",
			Sources:   []searchResultItem{},
			Model:     project.LLM.Model,
			SessionID: req.SessionID,
		})
		return
	}

	// Output language is driven by Project.Language (Empty falls back to
	// "English" via EffectiveLanguage). The directive is explicit so the
	// model translates context that may already be in another language
	// rather than mirroring it. Technical fields (citation markers,
	// SQL/identifiers if any leak into context) stay in English.
	systemPrompt := fmt.Sprintf(
		"You are a data analyst assistant for DecisionBox. Answer questions using the provided insights, recommendations, and project knowledge sources. "+
			"Cite insights and recommendations as [1], [2], etc. Cite knowledge sources as [s1], [s2], etc. "+
			"If the provided context doesn't contain enough information, say so. "+
			"Write your answer in %s. The retrieved context may be in a different language — translate as needed and do not mirror it. "+
			"Keep technical tokens (SQL, column names, identifiers, citation markers) in English.",
		project.EffectiveLanguage(),
	)

	// Resolve the conversation seed for a NEW session (existing sessions carry
	// their persisted seed, read below). The block is appended to the system
	// prompt once the session is known; the exact-fit check further down guards
	// against the (bounded) addition overflowing the window.
	var seed *commonmodels.AskSessionSeed
	if req.SessionID == "" {
		seed = h.hydrateSeed(ctx, projectID, req.SeedContext)
	}

	llmProvider, err := h.createLLMProvider(ctx, project, projectID)
	if err != nil {
		writeErrorCode(w, http.StatusPreconditionFailed,
			ErrCodeLLMNotConfigured,
			"LLM provider not configured for this project",
			err.Error())
		return
	}

	// Token-aware prompt assembly. The budget walk uses
	// gollm.ApproximateCounter throughout (rune/4) for two reasons:
	//
	//  1. Trim/shrink decisions touch every history pair, every RAG
	//     shrink iteration, and the assembled prompt — call counts
	//     scale with session size. With an exact counter that makes
	//     a network round-trip (Anthropic /count_tokens, Vertex
	//     :countTokens), the per-Ask latency would balloon by
	//     20–100×.
	//  2. The 15% approximate-tier safety margin is wide enough to
	//     absorb rune/4's drift on every prompt shape we've seen
	//     (English, code, CJK, mixed).
	//
	// We then do ONE exact verification on the assembled request
	// (system + history + prompt) via the provider's
	// TokenCounterProvider (Claude /count_tokens, OpenAI tiktoken,
	// Vertex Gemini :countTokens, …). If exact reveals an overflow
	// the approximate walk missed, the gate returns 413. If the
	// provider has no exact counter (Bedrock, Ollama, …), the
	// approximate walk's 15% margin is the safety net.
	counter := gollm.ApproximateCounter{}
	// EffectiveInputWindow respects per-deployment overrides (Ollama's
	// num_ctx) so the budgeter trims to the operator-chosen window
	// instead of letting the request hit the server's Truncate=false
	// guard with an oversize prompt.
	modelMaxInput := gollm.GetEffectiveInputWindow(project.LLM.Provider, project.LLM.Model, project.LLM.Config)
	budget := gollm.NewBudget(modelMaxInput, askMaxOutputTokens, askReservedSystemTokens, false)

	questionTokens, _ := counter.Count(ctx, req.Question)
	available := budget.Available() - questionTokens
	if available <= 0 {
		writeErrorCode(w, http.StatusRequestEntityTooLarge,
			ErrCodeContextOverflow,
			"the question alone exceeds this model's input window",
			fmt.Sprintf("model=%s window=%d question_tokens=%d",
				project.LLM.Model, modelMaxInput, questionTokens))
		return
	}

	// Knowledge section is fixed for this turn (the retriever already
	// chose top-K), so count it once and have the RAG shrink loop
	// account for it as a fixed cost. Otherwise a project with
	// large knowledge sources could pass the per-RAG budget but still
	// overflow once knowledge is added.
	knowledgeSection := gosources.FormatPromptSection(knowledgeChunks)
	knowledgeTokens, _ := counter.Count(ctx, knowledgeSection)

	ragBudget := available - knowledgeTokens
	contextStr, ragTokens, keptInsights, _ := fitRAGContext(ctx, counter, insights, ragBudget)

	// If RAG context + knowledge still overflows even at the
	// askMinRAGItems floor, surface a typed 413 rather than letting
	// the provider 4xx with a generic message.
	if ragTokens+knowledgeTokens > available && keptInsights <= askMinRAGItems {
		writeErrorCode(w, http.StatusRequestEntityTooLarge,
			ErrCodeContextOverflow,
			"retrieved context plus this question exceeds the model's input window",
			fmt.Sprintf("model=%s window=%d question=%d rag=%d knowledge=%d",
				project.LLM.Model, modelMaxInput, questionTokens, ragTokens, knowledgeTokens))
		return
	}

	prompt := fmt.Sprintf("Context from %d relevant insights/recommendations:\n\n%s\n\n%s\nQuestion: %s", keptInsights, contextStr, knowledgeSection, req.Question)
	promptTokens, _ := counter.Count(ctx, prompt)

	// The earlier "rag + knowledge > available" check counts the raw
	// citation + chunk bodies but not the wrapping prompt scaffolding
	// ("Context from N relevant insights/recommendations:\n\n…
	// Question: …"). On tight budgets that wrapper can be the
	// difference between fitting and overflowing, so we surface the
	// 413 here rather than letting the provider 4xx.
	if promptTokens > budget.Available() {
		writeErrorCode(w, http.StatusRequestEntityTooLarge,
			ErrCodeContextOverflow,
			"the assembled question + retrieved context exceeds this model's input window",
			fmt.Sprintf("model=%s window=%d prompt_tokens=%d available=%d",
				project.LLM.Model, modelMaxInput, promptTokens, budget.Available()))
		return
	}

	// Budget remaining for history = Available - prompt-shaped tokens.
	// The prompt already includes the question + RAG + knowledge; we
	// don't double-count.
	historyBudget := budget.Available() - promptTokens

	// Build messages with conversation history from session for
	// multi-turn context. The walk drops oldest pairs first; the
	// current user prompt always rides at the end.
	var messages []gollm.Message
	if req.SessionID != "" {
		session, err := h.sessionRepo.GetByID(ctx, req.SessionID)
		if err == nil && session != nil {
			if session.ProjectID != projectID {
				writeError(w, http.StatusBadRequest, "session does not belong to this project")
				return
			}
			// A follow-up turn stays anchored to the seed persisted at creation.
			seed = session.SeedContext
			trimmed, _ := trimMessagesByTokens(ctx, session.Messages, counter, historyBudget)
			messages = append(messages, trimmed...)
		}
	}
	messages = append(messages, gollm.Message{Role: "user", Content: prompt})

	// Anchor the answer on the seeded insight / recommendation, if any. Appended
	// after the token budget is computed; the exact-fit verification below uses
	// this final system prompt, so a (bounded) overflow still surfaces as a 413.
	systemPrompt += seed.PromptBlock()

	// Single exact verification on the fully assembled request.
	// Catches the rare case where the approximate walk under-counted
	// enough to slip past the budget but exact reveals an actual
	// overflow. Provider exact counter not available, transient
	// upstream error, or context-cancelled → silently fall through
	// and let provider report any overflow as a typed 413 via
	// classifyLLMError below.
	if overflow, ok := verifyExactPromptFits(ctx, llmProvider, project.LLM.Model, systemPrompt, messages, modelMaxInput); ok && overflow != nil {
		writeErrorCode(w, http.StatusRequestEntityTooLarge,
			ErrCodeContextOverflow,
			"the assembled prompt exceeds the model's input window (verified via exact counter)",
			overflow.Error())
		return
	}

	// Temperature omitted: Bedrock + direct Anthropic reject the field
	// on Opus 4.x extended-thinking models. Synthesis already grounds the
	// answer in cited insights/recommendations + knowledge chunks, so the
	// model's default sampling is acceptable here.
	chatResp, err := llmProvider.Chat(ctx, gollm.ChatRequest{
		Model:        project.LLM.Model,
		SystemPrompt: systemPrompt,
		Messages:     messages,
		MaxTokens:    askMaxOutputTokens,
	})
	if err != nil {
		status, code, msg, details := classifyLLMError(err)
		writeErrorCode(w, status, code, msg, details)
		return
	}

	// Build session message — include both insight/recommendation citations
	// and knowledge source chunks so the session record reflects everything
	// the answer was grounded on.
	sessionSources := make([]commonmodels.AskSessionSource, 0, len(insights)+len(knowledgeChunks))
	for _, s := range insights {
		sessionSources = append(sessionSources, commonmodels.AskSessionSource{
			ID: s.ID, Type: s.Type, Name: s.Name, Score: s.Score,
			Severity: s.Severity, AnalysisArea: s.AnalysisArea,
			Description: s.Description, DiscoveryID: s.DiscoveryID,
		})
	}
	for _, c := range knowledgeChunks {
		sessionSources = append(sessionSources, commonmodels.AskSessionSource{
			ID:          chunkCitationID(c),
			Type:        "source_chunk",
			Name:        c.SourceName,
			Score:       c.Score,
			Description: truncateForSession(c.Text),
		})
	}
	msg := commonmodels.AskSessionMessage{
		Question:     req.Question,
		Answer:       chatResp.Content,
		Sources:      sessionSources,
		Model:        chatResp.Model,
		InputTokens:  chatResp.Usage.InputTokens,
		OutputTokens: chatResp.Usage.OutputTokens,
		CreatedAt:    time.Now(),
	}

	// Create or append to session
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
		session := &commonmodels.AskSession{
			ID:          sessionID,
			ProjectID:   projectID,
			UserID:      "anonymous",
			Title:       req.Question,
			Messages:    []commonmodels.AskSessionMessage{msg},
			SeedContext: seed,
		}
		if err := h.sessionRepo.Create(ctx, session); err != nil {
			apilog.WithError(err).Warn("Failed to create ask session")
		}
	} else {
		if err := h.sessionRepo.AppendMessage(ctx, sessionID, msg); err != nil {
			apilog.WithError(err).Warn("Failed to append to ask session")
		}
	}

	// Build response Sources: insight items first, then knowledge chunks
	// surfaced as searchResultItem so the dashboard can render both lists.
	respSources := make([]searchResultItem, 0, len(insights)+len(knowledgeChunks))
	respSources = append(respSources, insights...)
	for _, c := range knowledgeChunks {
		respSources = append(respSources, searchResultItem{
			ID:          chunkCitationID(c),
			Type:        "source_chunk",
			Score:       c.Score,
			Name:        c.SourceName,
			Description: truncateForSession(c.Text),
		})
	}

	writeJSON(w, http.StatusOK, askResponse{
		Answer:       chatResp.Content,
		Sources:      respSources,
		Model:        chatResp.Model,
		SessionID:    sessionID,
		InputTokens:  chatResp.Usage.InputTokens,
		OutputTokens: chatResp.Usage.OutputTokens,
	})
}

// truncateForSession trims chunk text to a reasonable size for storage and
// for display in citation lists. Full chunk text is not stored on the session
// record — the source itself is the authoritative copy. Truncation is by
// rune count to avoid splitting multi-byte UTF-8 runes (Portuguese, CJK, etc.).
func truncateForSession(s string) string {
	const max = 400
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// chunkCitationID builds a stable, chunk-unique identifier for session storage
// and API responses. Multiple chunks from the same source would otherwise
// collide on SourceID when top-K retrieval returns more than one per document.
func chunkCitationID(c gosources.Chunk) string {
	return fmt.Sprintf("%s#%d", c.SourceID, c.Position)
}

// createLLMProvider creates an LLM provider for a project's RAG answer synthesis.
func (h *SearchHandler) createLLMProvider(ctx context.Context, project *models.Project, projectID string) (gollm.Provider, error) {
	// Per-project secret wins, else the LLM_API_KEY env fallback: managed
	// mode stores no per-project AI secret, so Ask's RAG synthesis (which
	// runs in the API process) must reach the gateway via env. Resolution
	// order shared with the agent via gosecrets.ResolveCredential.
	apiKey, _, _ := gosecrets.ResolveCredential(ctx, h.secretProvider, projectID, "llm-credentials", "LLM_API_KEY")

	cfg := gollm.ProviderConfig{
		"credentials_json": apiKey,
		"model":            project.LLM.Model,
	}
	for k, v := range project.LLM.Config {
		cfg[k] = v
	}

	return gollm.NewProvider(project.LLM.Provider, cfg)
}

// ListHistory returns recent search/ask queries for a project.
// GET /api/v1/projects/{id}/search/history?limit=20
func (h *SearchHandler) ListHistory(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project ID is required")
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		limit = parsed
	}

	entries, err := h.historyRepo.ListByProject(r.Context(), projectID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list search history")
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

// ListAskSessions returns recent ask sessions for a project.
// GET /api/v1/projects/{id}/ask/sessions?limit=20
func (h *SearchHandler) ListAskSessions(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project ID is required")
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		limit = parsed
	}

	sessions, err := h.sessionRepo.ListByProject(r.Context(), projectID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	writeJSON(w, http.StatusOK, sessions)
}

// GetAskSession returns a full ask session with all messages.
// GET /api/v1/projects/{id}/ask/sessions/{sessionId}
func (h *SearchHandler) GetAskSession(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	sessionID := r.PathValue("sessionId")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session ID is required")
		return
	}

	session, err := h.sessionRepo.GetByID(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if session.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	writeJSON(w, http.StatusOK, session)
}

// DeleteAskSession deletes an ask session.
// DELETE /api/v1/projects/{id}/ask/sessions/{sessionId}
func (h *SearchHandler) DeleteAskSession(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	sessionID := r.PathValue("sessionId")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session ID is required")
		return
	}

	// Verify session belongs to this project
	session, err := h.sessionRepo.GetByID(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if session.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if err := h.sessionRepo.Delete(r.Context(), sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
