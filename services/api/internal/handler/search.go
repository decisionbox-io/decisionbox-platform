package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/google/uuid"
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
	ID            string  `json:"id"`
	Type          string  `json:"type"`
	Score         float64 `json:"score"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Severity      string  `json:"severity,omitempty"`
	AnalysisArea  string  `json:"analysis_area,omitempty"`
	DiscoveryID   string  `json:"discovery_id"`
	DiscoveredAt  string  `json:"discovered_at,omitempty"`
	ProjectID     string  `json:"project_id,omitempty"`
	ProjectName   string  `json:"project_name,omitempty"`
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
	embProvider, err := h.createEmbeddingProvider(ctx, project.Embedding.Provider, project.Embedding.Model, projectID)
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
func (h *SearchHandler) createEmbeddingProvider(ctx context.Context, providerName, model, projectID string) (goembedding.Provider, error) {
	apiKey := ""
	if h.secretProvider != nil {
		key, err := h.secretProvider.Get(ctx, projectID, "embedding-api-key")
		if err == nil {
			apiKey = key
		}
	}

	return goembedding.NewProvider(providerName, goembedding.ProviderConfig{
		"api_key": apiKey,
		"model":   model,
	})
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
			}
		case "recommendation":
			if rec, err := h.recRepo.GetByID(ctx, sr.ID); err == nil {
				item.Name = rec.Title
				item.Description = rec.Description
				item.DiscoveryID = rec.DiscoveryID
				item.ProjectID = rec.ProjectID
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
			embProvider, err = h.createEmbeddingProvider(ctx, p.Embedding.Provider, req.EmbeddingModel, p.ID)
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
}

type askResponse struct {
	Answer    string             `json:"answer"`
	Sources   []searchResultItem `json:"sources"`
	Model     string             `json:"model"`
	SessionID string             `json:"session_id"`
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
		writeError(w, http.StatusBadRequest, "embedding provider not configured")
		return
	}

	// Embed the question
	embProvider, err := h.createEmbeddingProvider(ctx, project.Embedding.Provider, project.Embedding.Model, projectID)
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

	sources := h.enrichResults(ctx, searchResults)

	if len(sources) == 0 {
		writeJSON(w, http.StatusOK, askResponse{
			Answer:    "No relevant insights found for this question. Try running a discovery first or rephrasing your question.",
			Sources:   []searchResultItem{},
			Model:     project.LLM.Model,
			SessionID: req.SessionID,
		})
		return
	}

	// Build context from sources for LLM
	var contextParts []string
	for i, s := range sources {
		contextParts = append(contextParts, fmt.Sprintf("[%d] %s: %s (score: %.2f)", i+1, s.Name, s.Description, s.Score))
	}
	contextStr := strings.Join(contextParts, "\n")

	// Call LLM to synthesize answer
	llmProvider, err := h.createLLMProvider(ctx, project, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create LLM provider")
		return
	}

	systemPrompt := "You are a data analyst assistant for DecisionBox. Answer questions based on the provided insights and recommendations from previous discovery runs. Always cite your sources by number (e.g., [1], [2]). If the provided context doesn't contain enough information, say so."

	prompt := fmt.Sprintf("Context from %d relevant insights/recommendations:\n\n%s\n\nQuestion: %s", len(sources), contextStr, req.Question)

	// Build messages with conversation history from session for multi-turn context
	var messages []gollm.Message
	if req.SessionID != "" {
		session, err := h.sessionRepo.GetByID(ctx, req.SessionID)
		if err == nil && session != nil {
			if session.ProjectID != projectID {
				writeError(w, http.StatusBadRequest, "session does not belong to this project")
				return
			}
			msgs := session.Messages
			if len(msgs) > 10 {
				msgs = msgs[len(msgs)-10:]
			}
			for _, m := range msgs {
				messages = append(messages,
					gollm.Message{Role: "user", Content: m.Question},
					gollm.Message{Role: "assistant", Content: m.Answer},
				)
			}
		}
	}
	messages = append(messages, gollm.Message{Role: "user", Content: prompt})

	chatResp, err := llmProvider.Chat(ctx, gollm.ChatRequest{
		Model:        project.LLM.Model,
		SystemPrompt: systemPrompt,
		Messages:     messages,
		MaxTokens:    2048,
		Temperature:  0.3,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LLM synthesis failed")
		return
	}

	// Build session message
	sessionSources := make([]commonmodels.AskSessionSource, 0, len(sources))
	for _, s := range sources {
		sessionSources = append(sessionSources, commonmodels.AskSessionSource{
			ID: s.ID, Type: s.Type, Name: s.Name, Score: s.Score,
			Severity: s.Severity, AnalysisArea: s.AnalysisArea,
			Description: s.Description, DiscoveryID: s.DiscoveryID,
		})
	}
	msg := commonmodels.AskSessionMessage{
		Question:   req.Question,
		Answer:     chatResp.Content,
		Sources:    sessionSources,
		Model:      chatResp.Model,
		TokensUsed: chatResp.Usage.InputTokens + chatResp.Usage.OutputTokens,
		CreatedAt:  time.Now(),
	}

	// Create or append to session
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
		session := &commonmodels.AskSession{
			ID:        sessionID,
			ProjectID: projectID,
			UserID:    "anonymous",
			Title:     req.Question,
			Messages:  []commonmodels.AskSessionMessage{msg},
		}
		if err := h.sessionRepo.Create(ctx, session); err != nil {
			apilog.WithError(err).Warn("Failed to create ask session")
		}
	} else {
		if err := h.sessionRepo.AppendMessage(ctx, sessionID, msg); err != nil {
			apilog.WithError(err).Warn("Failed to append to ask session")
		}
	}

	writeJSON(w, http.StatusOK, askResponse{
		Answer:    chatResp.Content,
		Sources:   sources,
		Model:     chatResp.Model,
		SessionID: sessionID,
	})
}

// createLLMProvider creates an LLM provider for a project's RAG answer synthesis.
func (h *SearchHandler) createLLMProvider(ctx context.Context, project *models.Project, projectID string) (gollm.Provider, error) {
	apiKey := ""
	if h.secretProvider != nil {
		key, err := h.secretProvider.Get(ctx, projectID, "llm-api-key")
		if err == nil {
			apiKey = key
		}
	}

	cfg := gollm.ProviderConfig{
		"api_key": apiKey,
		"model":   project.LLM.Model,
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

