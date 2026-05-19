package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gocatalog "github.com/decisionbox-io/decisionbox/libs/go-common/llm/catalog"
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

// ListExtendedLLMModels returns project-scoped LLM model entries
// contributed by any registered external model registry. Empty by
// default (no extenders registered); plugins may register entries via
// libs/go-common/llm/catalog.RegisterExtender. The response shape
// matches the per-model entries inside ListLLMProviders so the
// dashboard can merge the two lists without reshaping.
//
// GET /api/v1/projects/{id}/llm/extended-models
func (h *ProvidersHandler) ListExtendedLLMModels(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	if pid == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	entries, err := gocatalog.Extend(r.Context(), pid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load extended models: "+err.Error())
		return
	}
	// Flatten to the same ModelInfo shape the built-in catalog exposes,
	// so the dashboard's model picker can render both lists with a
	// single component.
	out := make([]gollm.ModelInfo, 0, len(entries))
	for _, e := range entries {
		display := e.DisplayName
		if display == "" {
			display = e.ID
		}
		out = append(out, gollm.ModelInfo{
			ID:                    e.ID,
			DisplayName:           display,
			Wire:                  string(e.Wire),
			MaxOutputTokens:       e.MaxOutputTokens,
			MaxInputTokens:        e.MaxInputTokens,
			InputPricePerMillion:  e.Pricing.InputPerMillion,
			OutputPricePerMillion: e.Pricing.OutputPerMillion,
			Lifecycle:             e.Lifecycle,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, out)
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

// fetchLiveModels asks the provider to list models and falls back to
// an empty list when listing isn't supported. Same contract as the
// embedding-side helper:
//
//   - cfg["model"] is stripped before construction. The model field is
//     irrelevant to ListModels for every supported provider (Bedrock
//     ListFoundationModels, OpenAI /v1/models, Vertex
//     publishers/google/models, Azure Foundry deployments, Ollama
//     /api/tags). Stripping prevents factories that probe the model
//     upstream from breaking listing.
//
//   - If the factory rejects an empty model (Chat-path validators) the
//     helper returns (nil, nil) — the dashboard then renders free-text
//     + catalog, no user-visible error.
//
//   - If the constructed provider doesn't implement ModelLister, also
//     (nil, nil).
func fetchLiveModels(ctx context.Context, provider string, cfg map[string]string) ([]gollm.RemoteModel, error) {
	cfgNoModel := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if k != "model" {
			cfgNoModel[k] = v
		}
	}
	prov, err := gollm.NewProvider(provider, gollm.ProviderConfig(cfgNoModel))
	if err != nil {
		return nil, nil
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

// projectLiveModelsRequest is the body of POST
// /api/v1/projects/{id}/providers/llm/models/live. The optional Slot field
// picks which project slot to read — "llm" (default) for the analysis LLM,
// "blurb_llm" for the schema-index blurb LLM. The slot determines both the
// project field (project.LLM vs project.BlurbLLM) and the secret key
// (llm-credentials vs blurb-llm-credentials).
//
// When slot=blurb_llm but the project has no blurb_llm configured, the
// handler falls back to the analysis LLM slot (matching the agent's
// resolveBlurbLLM behaviour — a missing blurb_llm means "reuse analysis").
type projectLiveModelsRequest struct {
	Slot string `json:"slot,omitempty"`
}

// ListLiveLLMModelsForProject runs the same merge as ListLiveLLMModels
// but pulls the API key from the project's saved secret (if any) so
// the user doesn't re-enter it on the settings screen. Cloud providers
// that use ambient credentials (Bedrock, Vertex) work here too — the
// factory picks up IAM / ADC from the environment.
//
// The request body may include {"slot": "blurb_llm"} to read the
// project's blurb-LLM slot instead of the default analysis-LLM slot.
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

	// Body is optional — an empty body is the legacy {slot: "llm"} call.
	var body projectLiveModelsRequest
	if r.ContentLength > 0 {
		// Tolerate an empty `{}` body too: io.EOF from Decode is the
		// idiomatic empty-body signal. Use errors.Is rather than a
		// string compare against "EOF" so any future error wrapping
		// (or a different decoder error type that happens to stringify
		// to "EOF") still routes correctly. Real malformed JSON falls
		// through to the 400 below.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	slot := body.Slot
	if slot == "" {
		slot = "llm"
	}
	if slot != "llm" && slot != "blurb_llm" {
		writeError(w, http.StatusBadRequest, "invalid slot: "+slot+" (must be llm or blurb_llm)")
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

	// Resolve the slot to a (provider, model, config, []secretKey) tuple.
	// For slot=blurb_llm the secret lookup order matches the agent's
	// resolveBlurbLLM (index_schema.go): blurb-llm-credentials first,
	// then fall back to llm-credentials. This applies whether or not
	// project.BlurbLLM is populated — a user can configure a blurb-
	// specific credential even when blurb shares the analysis provider,
	// and the dashboard's "Load models" must read the same credential
	// the agent would read at indexing time.
	var (
		provider   string
		modelID    string
		slotCfg    map[string]string
		secretKeys []string
	)
	switch slot {
	case "blurb_llm":
		if project.BlurbLLM != nil && project.BlurbLLM.Provider != "" {
			provider = project.BlurbLLM.Provider
			modelID = project.BlurbLLM.Model
			slotCfg = project.BlurbLLM.Config
		} else {
			// No blurb override — borrow the analysis LLM's provider/
			// model/config but still prefer the blurb-specific secret
			// per the agent's resolveBlurbLLM contract.
			provider = project.LLM.Provider
			modelID = project.LLM.Model
			slotCfg = project.LLM.Config
		}
		secretKeys = []string{"blurb-llm-credentials", "llm-credentials"}
	default:
		provider = project.LLM.Provider
		modelID = project.LLM.Model
		slotCfg = project.LLM.Config
		secretKeys = []string{"llm-credentials"}
	}

	if provider == "" {
		writeError(w, http.StatusBadRequest, "project has no "+slot+" provider configured")
		return
	}

	meta, ok := gollm.GetProviderMeta(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider on project: "+provider)
		return
	}

	// Build config from the slot's config + the stored secret.
	// For the *list* call we only need credentials + connection params;
	// fetchLiveModels fills in a placeholder model for factories that
	// require one at construction time.
	cfg := map[string]string{}
	for k, v := range slotCfg {
		cfg[k] = v
	}
	if modelID != "" {
		cfg["model"] = modelID
	}
	if h.secretProvider != nil {
		// First non-empty secret wins, mirroring the agent's lookup
		// order (blurb-specific takes precedence even when blurb
		// borrows the analysis provider's config).
		for _, key := range secretKeys {
			value, err := h.secretProvider.Get(r.Context(), pid, key)
			if err == nil && value != "" {
				cfg["credentials_json"] = value
				break
			}
		}
	}
	// cfg holds the secret for the duration of this handler; it goes
	// out of scope on return. Go strings are immutable so active
	// scrubbing is not possible; we rely on scope + GC.

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	live, liveErr := fetchLiveModels(ctx, provider, cfg)
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
	// CatalogModels() flattens the provider's []ModelEntry to one
	// ModelInfo row per canonical ID — aliases stay internal so the
	// combobox isn't doubled. Matching against the catalog uses the
	// full ModelEntry list (including aliases) so a live row whose ID
	// is registered as an alias gets merged onto its canonical row.
	catalog := meta.CatalogModels()
	canonicalByID := make(map[string]string, len(meta.Models))
	for _, e := range meta.Models {
		canonicalByID[e.ID] = e.ID
		for _, a := range e.Aliases {
			canonicalByID[a] = e.ID
		}
	}
	catalogByID := make(map[string]gollm.ModelInfo, len(catalog))
	for _, m := range catalog {
		catalogByID[m.ID] = m
	}

	merged := make(map[string]liveModelsResponse, len(catalogByID)+len(live))
	for id, m := range catalogByID {
		// Catalog rows are dispatchable by construction — the
		// provider curated them and knows how to call them. The
		// wire field may be WireUnknown for single-wire providers
		// (Ollama) where dispatch has no wire switch; that does not
		// imply non-dispatchability.
		merged[id] = liveModelsResponse{
			Source:       "catalog",
			Dispatchable: true,
			ModelInfo:    m,
		}
	}
	for _, lm := range live {
		// If the live ID matches an alias, project onto the canonical
		// row so we don't double-list the same model. Suppressed for
		// providers (Ollama) where the canonical ID is NOT dispatchable
		// — the user must save the exact live ID the upstream returns.
		canonical := lm.ID
		if !meta.PreferLiveModelID {
			if c, ok := canonicalByID[lm.ID]; ok {
				canonical = c
			}
		}
		row, ok := merged[canonical]
		if ok {
			row.Source = "both"
			if lm.DisplayName != "" {
				row.DisplayName = lm.DisplayName
			}
			if lm.Lifecycle != "" {
				row.Lifecycle = lm.Lifecycle
			}
			merged[canonical] = row
		} else {
			inferred := ""
			if meta.FamilyInferrer != nil {
				inferred = string(meta.FamilyInferrer(lm.ID))
			}
			// Wire-blind providers (Ollama today) dispatch every model
			// ID through one SDK path with no wire switch, so live
			// rows are always dispatchable regardless of inference.
			dispatchable := meta.DispatchAnyModelID ||
				(inferred != "" && inferred != string(gollm.WireUnknown))
			merged[lm.ID] = liveModelsResponse{
				Source:       "live",
				Dispatchable: dispatchable,
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
	// Forward the project's saved non-credential settings (auth_method,
	// region, project_id, location, role_arn, …) so the live-list
	// instantiation mirrors what the agent does at indexing time.
	for k, v := range project.Embedding.Config {
		cfg[k] = v
	}
	// Pull the stored credential so the live call has credentials
	// without the user re-typing. Match the agent's lookup so
	// project-scoped list works exactly like a real indexing run.
	if h.secretProvider != nil {
		if key, err := h.secretProvider.Get(r.Context(), pid, "embedding-credentials"); err == nil && key != "" {
			cfg["credentials_json"] = key
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	live, liveErr := fetchLiveEmbeddingModels(ctx, providerID, cfg)
	writeEmbeddingLiveModelsResponse(w, meta, live, liveErr)
}

// fetchLiveEmbeddingModels asks the provider to list models and falls
// back to an empty list when listing isn't supported. The semantics:
//
//   - "If the provider implements ModelLister, use it" — strip
//     cfg["model"] before construction so listing never depends on a
//     user-selected (or form-defaulted) model that may not exist on the
//     upstream server. The model field is semantically irrelevant to
//     the list operation; every supported provider's list API is
//     model-agnostic (Bedrock ListFoundationModels, OpenAI /v1/models,
//     Voyage /v1/models, Vertex publishers/google/models, Azure OpenAI
//     deployments, Ollama /api/tags).
//
//   - "Otherwise return empty" — when the factory rejects an empty
//     model (the runtime Embed() path validators), or when the
//     constructed provider doesn't implement ModelLister, return
//     (nil, nil). The dashboard renders free-text + catalog so the
//     user can type a model name; no upstream-error noise is surfaced.
//
// This matches the user-facing contract: pick a provider, see what
// the server has if it can tell us, otherwise type whatever you want.
func fetchLiveEmbeddingModels(ctx context.Context, providerID string, cfg map[string]string) ([]goembedding.RemoteModel, error) {
	cfgNoModel := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if k != "model" {
			cfgNoModel[k] = v
		}
	}
	prov, err := goembedding.NewProvider(providerID, goembedding.ProviderConfig(cfgNoModel))
	if err != nil {
		// Factory rejected the empty model — provider can't list
		// without it. Fall back to "let the user type free text".
		return nil, nil
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
