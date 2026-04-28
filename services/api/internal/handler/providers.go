package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/database"
)

// ProvidersHandler handles provider listing endpoints. The repo +
// secret-provider deps are optional (nil-safe): they are only needed by
// the project-scoped live-models endpoint, which re-uses the stored
// credentials instead of asking the caller to re-enter them.
type ProvidersHandler struct {
	projectRepo    database.ProjectRepo
	secretProvider secrets.Provider
}

func NewProvidersHandler() *ProvidersHandler {
	return &ProvidersHandler{}
}

// NewProvidersHandlerWithProject returns a handler wired up for the
// project-scoped live-list endpoint. Callers that only need the
// cloud-neutral listing endpoints can keep using NewProvidersHandler().
func NewProvidersHandlerWithProject(projectRepo database.ProjectRepo, secretProvider secrets.Provider) *ProvidersHandler {
	return &ProvidersHandler{projectRepo: projectRepo, secretProvider: secretProvider}
}

// ListLLMProviders returns registered LLM providers with config metadata.
// GET /api/v1/providers/llm
func (h *ProvidersHandler) ListLLMProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, gollm.RegisteredProvidersMeta())
}

// ListWarehouseProviders returns registered warehouse providers with config metadata.
// GET /api/v1/providers/warehouse
func (h *ProvidersHandler) ListWarehouseProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, gowarehouse.RegisteredProvidersMeta())
}

// ListEmbeddingProviders returns registered embedding providers with config metadata.
// GET /api/v1/providers/embedding
func (h *ProvidersHandler) ListEmbeddingProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, goembedding.RegisteredProvidersMeta())
}

// liveModelsRequest is the body of POST /api/v1/providers/llm/{id}/models/live.
// The config map is the same shape as project.llm.config plus any
// credential fields (e.g. api_key) — these are used for the single
// upstream list call and are not persisted anywhere server-side.
type liveModelsRequest struct {
	Config map[string]string `json:"config"`
}

// liveModelsResponse rows are a superset of catalog rows: every live
// model ID is enriched with its catalog metadata when present, and
// catalogued models with no upstream match are included too so the
// dashboard can render the full picker even when the upstream list is
// partial.
type liveModelsResponse struct {
	// Source indicates where the row originated: "live" means it came
	// back from the upstream, "catalog" means it was in our shipped
	// catalog but not in the live list, "both" means both.
	Source string `json:"source"`
	// Dispatchable is true when DecisionBox knows how to talk to this
	// model — either because it is in the catalog, or because the
	// cloud's wire inferrer recognised its family. False for rows the
	// live endpoint returned but whose wire format we haven't
	// implemented (e.g. Amazon Nova / Titan, Cohere, AI21 on Bedrock).
	Dispatchable bool `json:"dispatchable"`
	gollm.ModelInfo
}

// ListLiveLLMModels calls the provider's ModelLister with the supplied
// config, merges the result with the shipped catalog, and returns a
// deduplicated sorted list.
//
// POST /api/v1/providers/llm/{id}/models/live
//
// Credentials arrive in the body, are passed to the provider factory
// for a single read-only call, and are zeroed immediately after. They
// are never logged and never written to the secret store — saving is
// the caller's job at project create/update time.
func (h *ProvidersHandler) ListLiveLLMModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "provider id is required")
		return
	}
	meta, ok := gollm.GetProviderMeta(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider: "+id)
		return
	}

	var body liveModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	// Credentials in body.Config are used for this single call only —
	// the request body is not persisted anywhere and the map goes out
	// of scope when the handler returns. Go strings are immutable so
	// we cannot actively scrub; we rely on scope + GC.

	// Build a 15-second timeout so a hung upstream doesn't block the UI.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	live, liveErr := fetchLiveModels(ctx, id, body.Config)
	writeLiveModelsResponse(w, meta, live, liveErr)
}

// fetchLiveModels calls the provider's ModelLister if the factory
// succeeds and the provider implements the interface. Returns nil + nil
// when the provider does not support live listing (no error, no rows).
//
// A placeholder is injected into cfg["model"] if missing — bedrock /
// vertex-ai / azure-foundry / ollama factories reject an empty model
// field, but ListModels never reads it. Keeping the construction strict
// for the Chat path while letting listing work without a picked model
// is worth the small kludge.
func fetchLiveModels(ctx context.Context, provider string, cfg map[string]string) ([]gollm.RemoteModel, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	if cfg["model"] == "" {
		cfg["model"] = "list-only-placeholder"
	}
	prov, err := gollm.NewProvider(provider, gollm.ProviderConfig(cfg))
	if err != nil {
		return nil, err
	}
	lister, ok := prov.(gollm.ModelLister)
	if !ok {
		return nil, nil
	}
	return lister.ListModels(ctx)
}

func displayOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ListLiveLLMModelsForProject runs the same merge as ListLiveLLMModels
// but pulls the API key from the project's saved secret (if any) so
// the user doesn't re-enter it on the settings screen. Cloud providers
// that use ambient credentials (Bedrock, Vertex) work here too — the
// factory picks up IAM / ADC from the environment.
//
// Requires a handler built via NewProvidersHandlerWithProject — the
// plain NewProvidersHandler() does not wire the project repo and
// should not be routed to this endpoint.
//
// POST /api/v1/projects/{id}/providers/llm/models/live
func (h *ProvidersHandler) ListLiveLLMModelsForProject(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	if pid == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	project, err := h.projectRepo.GetByID(r.Context(), pid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project: "+err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if project.LLM.Provider == "" {
		writeError(w, http.StatusBadRequest, "project has no llm provider configured")
		return
	}

	meta, ok := gollm.GetProviderMeta(project.LLM.Provider)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider on project: "+project.LLM.Provider)
		return
	}

	// Build config from project.llm.config + the stored secret.
	// For the *list* call we only need credentials + connection params;
	// fetchLiveModels fills in a placeholder model for factories that
	// require one at construction time.
	cfg := map[string]string{}
	for k, v := range project.LLM.Config {
		cfg[k] = v
	}
	if project.LLM.Model != "" {
		cfg["model"] = project.LLM.Model
	}
	if h.secretProvider != nil {
		if key, err := h.secretProvider.Get(r.Context(), pid, "llm-api-key"); err == nil && key != "" {
			cfg["api_key"] = key
		}
	}
	// cfg holds the secret for the duration of this handler; it goes
	// out of scope on return. Go strings are immutable so active
	// scrubbing is not possible; we rely on scope + GC.

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	live, liveErr := fetchLiveModels(ctx, project.LLM.Provider, cfg)
	writeLiveModelsResponse(w, meta, live, liveErr)
}

// writeLiveModelsResponse is the shared merge+emit tail for both
// live-list endpoints. Keeps the merge logic in one place.
//
// For each row we compute two derived fields:
//   - Wire (if empty) — inferred from the cloud's prefix table so
//     known-family live-only rows surface with the right wire badge.
//   - Dispatchable — true iff we ended up with a non-empty wire, i.e.
//     DecisionBox can actually call this model. Live rows whose
//     family we don't implement (Nova, Titan, Cohere, …) go out with
//     dispatchable=false so the UI can grey them out.
func writeLiveModelsResponse(w http.ResponseWriter, meta gollm.ProviderMeta, live []gollm.RemoteModel, liveErr error) {
	catalog := make(map[string]gollm.ModelInfo, len(meta.Models))
	for _, m := range meta.Models {
		catalog[m.ID] = m
	}

	merged := make(map[string]liveModelsResponse, len(catalog)+len(live))
	for id, m := range catalog {
		merged[id] = liveModelsResponse{
			Source:       "catalog",
			Dispatchable: m.Wire != "" && m.Wire != string(modelcatalog.Unknown),
			ModelInfo:    m,
		}
	}
	for _, lm := range live {
		row, ok := merged[lm.ID]
		if ok {
			row.Source = "both"
			if lm.DisplayName != "" {
				row.DisplayName = lm.DisplayName
			}
			if lm.Lifecycle != "" {
				row.Lifecycle = lm.Lifecycle
			}
			merged[lm.ID] = row
		} else {
			inferred := string(modelcatalog.InferWire(meta.ID, lm.ID))
			merged[lm.ID] = liveModelsResponse{
				Source:       "live",
				Dispatchable: inferred != "" && inferred != string(modelcatalog.Unknown),
				ModelInfo: gollm.ModelInfo{
					ID:          lm.ID,
					DisplayName: displayOr(lm.DisplayName, lm.ID),
					Wire:        inferred,
					Lifecycle:   lm.Lifecycle,
				},
			}
		}
	}

	out := make([]liveModelsResponse, 0, len(merged))
	for _, row := range merged {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	resp := map[string]interface{}{"models": out}
	if liveErr != nil {
		resp["live_error"] = liveErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Embedding live-list ---
//
// Embedding providers don't have a wire concept (they all speak each
// cloud's own REST shape), so the response is simpler: one row per
// model with id, name, and dimensions. The merge logic is otherwise
// identical to the LLM variant — live rows ship through, catalog rows
// fill in anything the upstream missed, and dimension 0 on a live row
// falls back to the catalog's dimension when the ID matches.

// embeddingLiveModelRow is the wire shape for one row. Keeps the
// catalog's Dimensions name on the live variant so the dashboard has
// one shape to render against.
type embeddingLiveModelRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Dimensions  int    `json:"dimensions"`
	Lifecycle   string `json:"lifecycle,omitempty"`
	Source      string `json:"source"` // "catalog" | "live" | "both"
}

// ListLiveEmbeddingModels hits the provider's ModelLister with the
// supplied config, merges with the shipped catalog, and returns a
// sorted list.
//
// POST /api/v1/providers/embedding/{id}/models/live
func (h *ProvidersHandler) ListLiveEmbeddingModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "provider id is required")
		return
	}
	meta, ok := goembedding.GetProviderMeta(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown embedding provider: "+id)
		return
	}

	var body liveModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	live, liveErr := fetchLiveEmbeddingModels(ctx, id, body.Config)
	writeEmbeddingLiveModelsResponse(w, meta, live, liveErr)
}

// ListLiveEmbeddingModelsForProject is the project-scoped variant that
// pulls the API key from the saved secret so the user doesn't re-enter
// it on the settings screen.
//
// POST /api/v1/projects/{id}/providers/embedding/models/live
func (h *ProvidersHandler) ListLiveEmbeddingModelsForProject(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	if pid == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	project, err := h.projectRepo.GetByID(r.Context(), pid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project: "+err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	providerID := project.Embedding.Provider
	if providerID == "" {
		writeError(w, http.StatusBadRequest, "project has no embedding provider configured")
		return
	}
	meta, ok := goembedding.GetProviderMeta(providerID)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown embedding provider: "+providerID)
		return
	}

	cfg := map[string]string{}
	// Pull the stored key so the live call has credentials without the
	// user re-typing. Match the agent's lookup so project-scoped list
	// works exactly like a real indexing run's embed call.
	if h.secretProvider != nil {
		if key, err := h.secretProvider.Get(r.Context(), pid, "embedding-api-key"); err == nil && key != "" {
			cfg["api_key"] = key
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	live, liveErr := fetchLiveEmbeddingModels(ctx, providerID, cfg)
	writeEmbeddingLiveModelsResponse(w, meta, live, liveErr)
}

// fetchLiveEmbeddingModels constructs the provider and asks it to list
// models if it implements the optional ModelLister capability. Returns
// (nil, nil) for providers that don't — the dashboard then just
// renders the shipped catalog, no user-visible error.
func fetchLiveEmbeddingModels(ctx context.Context, providerID string, cfg map[string]string) ([]goembedding.RemoteModel, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	// Every registered embedding provider factory validates required
	// config fields up-front, and most reject anything not on a strict
	// model allowlist (Bedrock's modelDimensions, Voyage's, etc.). For
	// a list-only call we just need the factory to succeed so
	// ListModels can run — supply the first catalogued model id as a
	// placeholder. ListModels never reads cfg["model"].
	if cfg["model"] == "" {
		if meta, ok := goembedding.GetProviderMeta(providerID); ok && len(meta.Models) > 0 {
			cfg["model"] = meta.Models[0].ID
		}
	}
	prov, err := goembedding.NewProvider(providerID, goembedding.ProviderConfig(cfg))
	if err != nil {
		return nil, err
	}
	lister, ok := prov.(goembedding.ModelLister)
	if !ok {
		return nil, nil
	}
	return lister.ListModels(ctx)
}

// writeEmbeddingLiveModelsResponse returns ONLY the models reported by
// the provider's ListModels. We deliberately skip merging with the
// shipped catalog — the user wants to see what the provider actually
// serves today, and the catalog is just a fallback that the combobox
// already has access to via ProviderMeta.Models.
//
// Enriches live rows with catalog dimensions when the provider doesn't
// report dims (OpenAI's /v1/models endpoint doesn't), so known IDs
// still carry a usable vector size.
func writeEmbeddingLiveModelsResponse(w http.ResponseWriter, meta goembedding.ProviderMeta, live []goembedding.RemoteModel, liveErr error) {
	catalogDims := make(map[string]goembedding.ModelInfo, len(meta.Models))
	for _, m := range meta.Models {
		catalogDims[m.ID] = m
	}

	out := make([]embeddingLiveModelRow, 0, len(live))
	for _, lm := range live {
		row := embeddingLiveModelRow{
			ID:          lm.ID,
			DisplayName: displayOr(lm.DisplayName, lm.ID),
			Dimensions:  lm.Dimensions,
			Lifecycle:   lm.Lifecycle,
			Source:      "live",
		}
		if row.Dimensions == 0 {
			if cat, ok := catalogDims[lm.ID]; ok {
				row.Dimensions = cat.Dimensions
				if row.DisplayName == lm.ID && cat.Name != "" {
					row.DisplayName = cat.Name
				}
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	resp := map[string]interface{}{"models": out}
	if liveErr != nil {
		resp["live_error"] = liveErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
