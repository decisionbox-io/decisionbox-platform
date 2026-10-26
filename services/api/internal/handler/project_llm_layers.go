package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/managedai"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// normalizeLLMLayerConfig canonicalises a request's per-layer configuration
// in place: provider-entry ids and layer provider references are normalised
// so an id-less entry and an explicit "default" are one entry everywhere, and
// empty maps become nil so the repository's clear path can fire.
func normalizeLLMLayerConfig(p *models.Project) {
	for i := range p.LLMProviders {
		p.LLMProviders[i].ID = llmlayer.NormalizeProviderID(strings.TrimSpace(p.LLMProviders[i].ID))
		p.LLMProviders[i].Provider = strings.TrimSpace(p.LLMProviders[i].Provider)
	}
	if len(p.LLMProviders) == 0 {
		p.LLMProviders = nil
	}
	if len(p.LayerModels) == 0 {
		p.LayerModels = nil
		return
	}
	for key, lm := range p.LayerModels {
		lm.ProviderID = llmlayer.NormalizeProviderID(strings.TrimSpace(lm.ProviderID))
		lm.Model = strings.TrimSpace(lm.Model)
		p.LayerModels[key] = lm
	}
}

// validateLLMLayerConfig checks a project's per-layer LLM configuration and
// returns a user-facing message, or "" when it is acceptable.
//
// Everything here is a client error worth naming precisely: a layer key typo
// would otherwise be stored and silently ignored forever, and a layer pointing
// at an entry that does not exist would quietly run on the default model.
func validateLLMLayerConfig(p *models.Project) string {
	if len(p.LLMProviders) == 0 && len(p.LayerModels) == 0 {
		return ""
	}

	// --- provider entries ---
	seen := map[string]bool{}
	hasDefault := false
	for _, e := range p.LLMProviders {
		if err := llmlayer.ValidateProviderID(e.ID); err != nil {
			return "llm_providers: " + err.Error()
		}
		if seen[e.ID] {
			return fmt.Sprintf("llm_providers: duplicate provider id %q (an id-less entry and %q are the same entry)", e.ID, llmlayer.DefaultProviderID)
		}
		seen[e.ID] = true
		if e.ID == llmlayer.DefaultProviderID {
			hasDefault = true
		}
		if e.Provider == "" {
			return fmt.Sprintf("llm_providers: entry %q has no provider", e.ID)
		}
		if msg := validateLLMConfig(e.Provider, e.Config); msg != "" {
			return fmt.Sprintf("llm_providers[%s].config: %s", e.ID, strings.TrimPrefix(msg, "llm.config."))
		}
	}
	if len(p.LLMProviders) > 0 && !hasDefault {
		// The default entry is reserved, not a pointer: an older agent,
		// sources-worker or fileingestion-worker resolves its credential from
		// the fixed "llm-credentials" key, so there must always be an entry
		// that owns it.
		return fmt.Sprintf("llm_providers: one entry must have the reserved id %q — it is the default every layer inherits and the only entry an older agent image can read credentials for", llmlayer.DefaultProviderID)
	}

	// --- layer rows ---
	if len(p.LayerModels) > 0 {
		if _, ok := p.LayerModels[llmlayer.DefaultLayer]; !ok {
			return fmt.Sprintf("layer_models: a %q row is required — it is what every layer without its own row uses", llmlayer.DefaultLayer)
		}
	}
	cfg := p.LLMLayerConfig()
	for _, key := range sortedLayerKeys(p.LayerModels) {
		lm := p.LayerModels[key]
		if !llmlayer.ValidateLayerKey(key) {
			return fmt.Sprintf("layer_models: unknown layer %q", key)
		}
		if _, ok := cfg.ProviderByID(lm.ProviderID); !ok {
			return fmt.Sprintf("layer_models[%s]: no provider entry with id %q", key, lm.ProviderID)
		}
		spec, err := cfg.SpecUnchecked(key)
		if err != nil {
			return fmt.Sprintf("layer_models[%s]: %s", key, err.Error())
		}
		if msg := validateLLMConfig(spec.Provider, lm.Config); msg != "" {
			return fmt.Sprintf("layer_models[%s].config: %s", key, strings.TrimPrefix(msg, "llm.config."))
		}
		// The layer's own pre-flight rule. Validate() applies it for a real
		// layer key; the default row is checked against every layer that would
		// inherit it, so "apply to all" with a reasoning model cannot sneak a
		// refused model into table descriptions.
		if err := spec.Validate(); err != nil {
			return fmt.Sprintf("layer_models[%s]: %s", key, err.Error())
		}
	}
	// Whatever the default row resolves to is also what an un-overridden
	// table-description layer will use, so it has to satisfy that layer's rule.
	if _, ok := p.LayerModels[llmlayer.DefaultLayer]; ok {
		if _, overridden := p.LayerModels[llmlayer.LayerTableDescriptions]; !overridden {
			spec, err := cfg.SpecUnchecked(llmlayer.LayerTableDescriptions)
			if err == nil {
				if err := spec.Validate(); err != nil {
					return fmt.Sprintf("layer_models[%s]: %s (set a separate %s model, or pick a non-reasoning default)",
						llmlayer.DefaultLayer, err.Error(), llmlayer.LayerTableDescriptions)
				}
			}
		}
	}
	return ""
}

// sortedLayerKeys gives validation a deterministic order so the SAME bad
// request always produces the same message — otherwise map iteration makes
// the error the client sees depend on chance.
func sortedLayerKeys(m map[string]llmlayer.LayerModel) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkLLMProviderEntriesAllowed runs the plan's provider allow-list for
// EVERY provider entry, not just the legacy llm.provider.
//
// Without the loop a tenant adds a disallowed provider as a secondary entry
// and uses it for every layer. It also closes a gap that predates this
// feature: blurb_llm.provider was never allow-listed at all.
func checkLLMProviderEntriesAllowed(ctx context.Context, p *models.Project) error {
	checker := policy.GetChecker()
	checked := map[string]bool{}
	for _, e := range p.EffectiveLLMProviders() {
		if e.Provider == "" || checked[e.Provider] {
			continue
		}
		checked[e.Provider] = true
		if err := checker.CheckLLMProviderAllowed(ctx, "", e.Provider); err != nil {
			return err
		}
	}
	return nil
}

// llmProviderRemovalConflict reports the layers that still reference a
// provider entry the request removes, so the user is told what to reassign
// rather than discovering later that a layer silently fell back.
//
// Returns "" when the removal is acceptable.
func llmProviderRemovalConflict(existing, incoming *models.Project) string {
	if len(incoming.LLMProviders) == 0 {
		// Clearing the whole configuration also clears the layer rows; there
		// is nothing left to dangle.
		return ""
	}
	kept := map[string]bool{}
	for _, e := range incoming.LLMProviders {
		kept[llmlayer.NormalizeProviderID(e.ID)] = true
	}
	// Only entries that actually EXISTED can be removed. A layer naming an id
	// that was never there is a different mistake with a different message,
	// and validateLLMLayerConfig reports that one.
	existed := map[string]bool{}
	for _, e := range existing.LLMProviders {
		existed[llmlayer.NormalizeProviderID(e.ID)] = true
	}

	// Check against the layer set the write will leave behind.
	layers := incoming.LayerModels
	if layers == nil {
		layers = existing.LayerModels
	}
	orphaned := map[string][]string{}
	for key, lm := range layers {
		id := llmlayer.NormalizeProviderID(lm.ProviderID)
		if !kept[id] && existed[id] {
			orphaned[id] = append(orphaned[id], key)
		}
	}
	if len(orphaned) == 0 {
		return ""
	}
	ids := make([]string, 0, len(orphaned))
	for id := range orphaned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		sort.Strings(orphaned[id])
		parts = append(parts, fmt.Sprintf("%q is still used by %s", id, strings.Join(orphaned[id], ", ")))
	}
	return "cannot remove a provider still in use: " + strings.Join(parts, "; ") + " — reassign those layers first"
}

// dualWriteLegacyLLM mirrors the resolved layer configuration back onto the
// legacy fields, so an agent, sources-worker or fileingestion-worker image
// older than this release keeps reading a truthful single-provider setup.
//
//	llm                  <- the default layer's (provider, model, merged config)
//	blurb_llm            <- the table-description layer, when it differs
//	reasoning_enabled    <- the default layer's resolved value
//	prompt_cache_enabled <- the default layer's resolved value
//
// It holds only because the default entry is always the reserved id "default",
// so llm-credentials is still its secret key.
func dualWriteLegacyLLM(p *models.Project) {
	if len(p.LayerModels) == 0 {
		return
	}
	cfg := p.LLMLayerConfig()
	def, err := cfg.SpecUnchecked(llmlayer.DefaultLayer)
	if err != nil {
		return
	}

	p.LLM = models.LLMConfig{
		Provider: def.Provider,
		Model:    def.Model,
		Config:   legacyConfigFrom(def),
	}
	reasoning := def.Reasoning
	promptCache := def.PromptCache
	p.ReasoningEnabled = &reasoning
	p.PromptCacheEnabled = &promptCache

	td, err := cfg.SpecUnchecked(llmlayer.LayerTableDescriptions)
	switch {
	case err != nil:
		// Leave the mirror as it is rather than guessing.
	case td.Provider == def.Provider && td.Model == def.Model:
		// Equal to the default: an older image reusing the analysis slot is
		// correct, and leaving a stale override would make it wrong.
		p.BlurbLLM = nil
	default:
		p.BlurbLLM = &models.BlurbLLMConfig{
			Provider: td.Provider,
			Model:    td.Model,
			Config:   legacyConfigFrom(td),
		}
	}
}

// legacyConfigFrom renders a resolved spec's merged config for the legacy
// mirror.
//
// prompt_cache and reasoning_enabled are written in as config entries too,
// not only onto the project fields, because that is where an older image's
// provider factory reads prompt caching from and where the orchestrator ORs
// reasoning in from.
func legacyConfigFrom(spec llmlayer.LayerSpec) map[string]string {
	if len(spec.Config) == 0 && !spec.Reasoning && spec.PromptCache {
		return nil
	}
	out := make(map[string]string, len(spec.Config)+2)
	for k, v := range spec.Config {
		out[k] = v
	}
	out[gollm.PromptCacheKey] = boolConfigValue(spec.PromptCache)
	if spec.Reasoning {
		out[gollm.ReasoningEnabledKey] = "true"
	} else {
		delete(out, gollm.ReasoningEnabledKey)
	}
	return out
}

func boolConfigValue(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// layerDescriptorsResponse is the body of GET /api/v1/llm/layers.
type layerDescriptorsResponse struct {
	// Version is llmlayer.ConfigVersion — the generation a project must be
	// configured at. The dashboard compares it with the project's own.
	Version int                   `json:"version"`
	Layers  []llmlayer.Descriptor `json:"layers"`
	// DefaultLayerKey is the reserved layer_models key holding the pair every
	// layer without its own row inherits.
	DefaultLayerKey string `json:"default_layer_key"`
}

// ListLLMLayers returns the layer vocabulary as DATA, so the dashboard holds
// no copy of the list and a layer added server-side appears in the UI without
// a front-end release.
//
// GET /api/v1/llm/layers
func (h *ProvidersHandler) ListLLMLayers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, layerDescriptorsResponse{
		Version:         llmlayer.ConfigVersion,
		Layers:          llmlayer.Layers(),
		DefaultLayerKey: llmlayer.DefaultLayer,
	})
}

// projectLLMConfigResponse is the body of GET
// /api/v1/projects/{id}/llm/config: the project's per-layer configuration as
// the settings page should render it.
type projectLLMConfigResponse struct {
	// Configured is false for a project that has never been through settings.
	Configured bool `json:"configured"`
	// Version is the project's stored llm_config_version.
	Version int `json:"version"`
	// RequiredVersion is llmlayer.ConfigVersion.
	RequiredVersion int `json:"required_version"`
	// Providers and LayerModels are the project's stored configuration, or —
	// for an unconfigured project — the values SEEDED from its legacy fields,
	// which is what makes the forced trip through settings a review rather
	// than a seventeen-row data-entry exercise.
	Providers   []llmlayer.ProviderEntry       `json:"providers"`
	LayerModels map[string]llmlayer.LayerModel `json:"layer_models"`
	// Seeded reports that the values above are derived and not yet saved.
	Seeded bool `json:"seeded"`
}

// GetProjectLLMConfig returns the per-layer configuration for the settings
// page, seeding it from the legacy fields when the project has never been
// configured.
//
// The seed is returned here rather than in the project document so that a
// client which round-trips GET /projects/{id} into a PUT cannot silently
// configure the project and skip the review the gate exists to force.
//
// GET /api/v1/projects/{id}/llm/config
func (h *ProjectsHandler) GetProjectLLMConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	project, err := h.repo.GetByID(r.Context(), id)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	resp := projectLLMConfigResponse{
		Configured:      project.LLMConfigured(),
		Version:         project.LLMConfigVersion,
		RequiredVersion: llmlayer.ConfigVersion,
		Providers:       project.LLMProviders,
		LayerModels:     project.LayerModels,
	}
	if len(resp.Providers) == 0 && len(resp.LayerModels) == 0 {
		resp.Providers, resp.LayerModels = project.LLMLayerConfig().SeedFromLegacy()
		resp.Seeded = true
	}
	if resp.LayerModels == nil {
		resp.LayerModels = map[string]llmlayer.LayerModel{}
	}
	if resp.Providers == nil {
		resp.Providers = []llmlayer.ProviderEntry{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// applyLLMLayerUpdate merges the request's per-layer LLM configuration onto
// the stored project, runs every guard the plural shape newly requires,
// stamps the configuration version and dual-writes the legacy mirror.
//
// Writes the error response and returns false on refusal, following the
// rejectAnchoringPromotion idiom already used in this handler. The returned
// slice names the provider entries this write removed, so their credentials
// can be neutralised once the save succeeds.
func (h *ProjectsHandler) applyLLMLayerUpdate(
	w http.ResponseWriter,
	r *http.Request,
	existing, incoming *models.Project,
	sentFields map[string]json.RawMessage,
) (bool, []llmlayer.ProviderEntry) {
	_, providersSent := sentFields["llm_providers"]
	_, layersSent := sentFields["layer_models"]
	if !providersSent && !layersSent {
		// Nothing per-layer in this request. The legacy merge above still
		// applies, so re-run the dual-write only when the project is already
		// configured — otherwise an unrelated edit would stamp it.
		return true, nil
	}

	removed := existing.LLMProviders

	if providersSent {
		incomingCopy := incoming.LLMProviders
		if msg := llmProviderRemovalConflict(existing, incoming); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return false, nil
		}
		existing.LLMProviders = incomingCopy
	}
	if layersSent {
		existing.LayerModels = incoming.LayerModels
	}
	normalizeLLMLayerConfig(existing)

	if msg := validateLLMLayerConfig(existing); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return false, nil
	}
	if err := checkLLMProviderEntriesAllowed(r.Context(), existing); err != nil {
		if writePolicyError(w, err) {
			return false, nil
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return false, nil
	}

	stampLLMConfigVersion(existing)
	dualWriteLegacyLLM(existing)

	if !providersSent {
		return true, nil
	}
	return true, removedProviderEntries(removed, existing.LLMProviders)
}

// stampLLMConfigVersion records that this project has been configured for the
// current layer vocabulary. Saving the configuration is what clears the gate.
func stampLLMConfigVersion(p *models.Project) {
	if len(p.LayerModels) == 0 {
		// A configuration cleared back to nothing is unconfigured again — the
		// alternative is a project marked configured with no rows to show.
		p.LLMConfigVersion = 0
		return
	}
	if p.LLMConfigVersion < llmlayer.ConfigVersion {
		p.LLMConfigVersion = llmlayer.ConfigVersion
	}
}

// removedProviderEntries returns the entries present before a write and
// absent after it.
func removedProviderEntries(before, after []llmlayer.ProviderEntry) []llmlayer.ProviderEntry {
	if len(before) == 0 {
		return nil
	}
	kept := make(map[string]bool, len(after))
	for _, e := range after {
		kept[llmlayer.NormalizeProviderID(e.ID)] = true
	}
	var out []llmlayer.ProviderEntry
	for _, e := range before {
		if !kept[llmlayer.NormalizeProviderID(e.ID)] {
			out = append(out, e)
		}
	}
	return out
}

// neutraliseProviderSecrets best-effort blanks the stored credentials of
// removed provider entries.
//
// secrets.Provider has no per-key Delete — the cloud backends route deletion
// through IAM-audited tooling on purpose — so an empty Set is the available
// equivalent, matching what the datasource handler does when a datasource is
// removed. Best-effort: the configuration change itself has already landed,
// and failing the response afterwards would be a lie about what was saved.
//
// A reserved entry is never touched: its secret is the legacy key an older
// agent image still reads.
func (h *ProjectsHandler) neutraliseProviderSecrets(ctx context.Context, projectID string, removed []llmlayer.ProviderEntry) {
	if h.secretProvider == nil {
		return
	}
	for _, e := range removed {
		id := llmlayer.NormalizeProviderID(e.ID)
		if llmlayer.IsReservedProviderID(id) {
			continue
		}
		if err := h.secretProvider.Set(ctx, projectID, llmlayer.CredentialsKey(id), ""); err != nil {
			apilog.WithFields(apilog.Fields{
				"project_id":  projectID,
				"provider_id": id,
				"error":       err.Error(),
			}).Warn("Failed to neutralise the credential of a removed LLM provider entry")
		}
	}
}

// applyLLMLayerCreate settles the per-layer configuration of a NEW project.
//
// A create is an explicit configuration act — the user has just chosen a
// provider and a model — so a created project is configured and never born
// gated. A body that carries no layer rows is seeded from the llm / blurb_llm
// fields it did carry, which is also what keeps a client that only knows the
// legacy shape working.
//
// Writes the error response and returns false on refusal.
func applyLLMLayerCreate(w http.ResponseWriter, r *http.Request, p *models.Project) bool {
	normalizeLLMLayerConfig(p)
	if msg := validateLLMLayerConfig(p); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return false
	}
	if len(p.LayerModels) == 0 {
		p.SeedLLMLayersFromLegacy()
	}
	if err := checkLLMProviderEntriesAllowed(r.Context(), p); err != nil {
		if writePolicyError(w, err) {
			return false
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return false
	}
	stampLLMConfigVersion(p)
	dualWriteLegacyLLM(p)
	return true
}

// stampLLMConfigRequired sets the read-time llm_config_required flag the
// dashboard's project-scoped guard reads, so the guard needs no second
// request.
//
// Always false on a managed deployment: the AI configuration surface is
// hidden there, so there is nothing for a user to define and nowhere to send
// them. That is belt and braces beside managedai.Apply stamping the version
// on every write — an old cloud project carries no version until something
// writes it.
func (h *ProjectsHandler) stampLLMConfigRequired(p *models.Project) {
	if p == nil {
		return
	}
	p.LLMConfigRequired = !managedai.Enabled() && !p.LLMConfigured()
}
