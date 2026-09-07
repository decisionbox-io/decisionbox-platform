package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/managedai"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// languageMaxLen caps Project.Language to a value short enough that no
// reasonable language name (English, Türkçe, Português, Bahasa Indonesia,
// Chinese (Traditional), …) needs more space, while keeping the field
// well below any prompt-injection-shaped payload size. The exact value
// is also asserted by tests, so changing it is intentional.
const languageMaxLen = 64

// sanitizeLanguage normalizes and validates a Project.Language value
// coming off the wire. The field is rendered into LLM system prompts
// verbatim via {{LANGUAGE}}, so untrusted input could inject directives
// like newlines + "ignore previous instructions" — we hard-reject any
// control characters and cap length.
//
// Empty after trim returns ("", nil) — meaning "no value", which the
// orchestrator's EffectiveLanguage falls back to "English" for. That
// preserves the legacy semantics for projects that never set Language.
//
// Allowed values: any UTF-8 printable text up to languageMaxLen runes,
// no control characters (including \n, \t, \r), no leading/trailing
// whitespace. We deliberately do NOT enforce an allowlist — users can
// pick any human language name (incl. dialects we haven't pre-listed)
// and the LLM can interpret it; the constraint is structural, not
// vocabulary-based.
func sanitizeLanguage(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("language must be valid UTF-8")
	}
	if utf8.RuneCountInString(s) > languageMaxLen {
		return "", fmt.Errorf("language must be %d characters or fewer", languageMaxLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("language must not contain control characters")
		}
	}
	return s, nil
}

// validateLLMConfig surfaces malformed llm.config values at write time so a
// typo like wire_override="antropik" is rejected by the API instead of
// being accepted and then failing at agent-run time with a less obvious
// error.
//
// When provider is non-empty and has a registered wire_override
// ConfigField with Options, the accepted values are scoped to that
// provider's supported wires (e.g. bedrock only offers anthropic +
// openai-compat). Otherwise we fall back to the generic catalog-wide
// parse so providers that don't register options still get syntax
// validation. Returns a user-facing message on failure, or "" on pass.
func validateLLMConfig(provider string, cfg map[string]string) string {
	raw, ok := cfg["wire_override"]
	if !ok || raw == "" {
		return ""
	}

	// Provider-scoped check: if the provider exposes wire_override as a
	// dropdown, use its Options as the authoritative allowlist.
	if provider != "" {
		if meta, ok := gollm.GetProviderMeta(provider); ok {
			for _, f := range meta.ConfigFields {
				if f.Key != "wire_override" || len(f.Options) == 0 {
					continue
				}
				for _, o := range f.Options {
					if o.Value == raw {
						return ""
					}
				}
				accepted := make([]string, 0, len(f.Options))
				for _, o := range f.Options {
					if o.Value != "" {
						accepted = append(accepted, o.Value)
					}
				}
				return fmt.Sprintf(
					"llm.config.wire_override: %q is not supported by provider %q; use one of %s",
					raw, provider, strings.Join(accepted, ", "),
				)
			}
		}
	}

	// Fallback: generic wire-syntax check.
	if !gollm.ParseWire(raw).Valid() {
		return fmt.Sprintf(
			"llm.config.wire_override: %q is not a valid wire; use one of %s, %s, %s",
			raw, gollm.WireAnthropic, gollm.WireOpenAICompat, gollm.WireGoogleNative,
		)
	}
	return ""
}

// ProjectRunSummaryProvider supplies the most recent discovery run per
// project so the List/Get handlers can stamp the read-time
// last_run_status / last_run_at / last_run_completed_at fields the
// dashboard project cards render. Satisfied by
// database.RunRepository.LatestByProjects; kept as a narrow in-package
// interface so the handler stays unit-testable without Mongo.
type ProjectRunSummaryProvider interface {
	LatestByProjects(ctx context.Context, projectIDs []string) (map[string]*models.DiscoveryRun, error)
}

// ProjectsHandler handles project CRUD endpoints.
type ProjectsHandler struct {
	repo           database.ProjectRepo
	domainPackRepo database.DomainPackRepo
	dropper        CollectionDropper         // optional: Qdrant per-project collection
	secretProvider secrets.Provider          // optional: only mongo-backed providers get swept
	indexCanceller IndexCanceller            // optional: detect in-flight indexing for 409
	runSummaries   ProjectRunSummaryProvider // optional: latest-run enrichment for List/Get
}

func NewProjectsHandler(repo database.ProjectRepo, domainPackRepo database.DomainPackRepo) *ProjectsHandler {
	return &ProjectsHandler{repo: repo, domainPackRepo: domainPackRepo}
}

// WithRunSummaries attaches the latest-run lookup used to populate the
// read-time last_run_* fields on List/Get responses. Nullable — when
// unset, those endpoints simply omit the run summary (the project data
// itself is unaffected).
func (h *ProjectsHandler) WithRunSummaries(runSummaries ProjectRunSummaryProvider) *ProjectsHandler {
	h.runSummaries = runSummaries
	return h
}

// enrichLastRun stamps the read-time last_run_status / last_run_at /
// last_run_completed_at fields on each project from its most recent
// discovery run. Best-effort: with no provider wired, or if the lookup
// fails, the projects are returned unchanged (a run-summary outage must
// not break the project list itself). One batched query covers the
// whole page.
func (h *ProjectsHandler) enrichLastRun(ctx context.Context, projects []*models.Project) {
	if h.runSummaries == nil || len(projects) == 0 {
		return
	}

	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if p != nil && p.ID != "" {
			ids = append(ids, p.ID)
		}
	}

	runs, err := h.runSummaries.LatestByProjects(ctx, ids)
	if err != nil {
		apilog.WithError(err).Warn("failed to load latest runs for project list; returning projects without run status")
		return
	}

	for _, p := range projects {
		if p == nil {
			continue
		}
		run, ok := runs[p.ID]
		if !ok || run == nil {
			continue
		}
		p.LastRunStatus = run.Status
		startedAt := run.StartedAt
		p.LastRunAt = &startedAt
		p.LastRunCompletedAt = run.CompletedAt
	}
}

// WithDeleteCascadeDeps attaches the optional dependencies the Delete
// endpoint needs to fully wipe a project (Qdrant collection drop,
// secret sweep, in-flight detection). All three are nullable — when
// any is nil, that subsystem's cleanup step is skipped, which matches
// the community Qdrant-less / external-secret-manager builds.
func (h *ProjectsHandler) WithDeleteCascadeDeps(dropper CollectionDropper, secretProvider secrets.Provider, indexCanceller IndexCanceller) *ProjectsHandler {
	h.dropper = dropper
	h.secretProvider = secretProvider
	h.indexCanceller = indexCanceller
	return h
}

// Create creates a new project.
// POST /api/v1/projects
func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.Project
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if p.Domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	if p.Category == "" {
		writeError(w, http.StatusBadRequest, "category is required")
		return
	}
	// Force ready state on every core-route create. Plugins that
	// need to start a project in a lifecycle state they own mount
	// their own creation endpoint via apiserver.RegisterRouteGroup;
	// the core route refuses to persist any other value because:
	//   - PUT /projects/{id} silently drops state changes (so the
	//     project can't be repaired through the public API).
	//   - POST /discover refuses any non-ready state.
	// Together those would trap a project in an unrecoverable shape
	// if a stale client or unrelated POST included a plugin-owned
	// state string.
	p.State = models.ProjectStateReady

	// Managed-inference override (no-op unless AI_GATEWAY_URL is set):
	// replace whatever LLM/blurb/embedding config the request carried
	// with the operator-configured gateway preset before anything else
	// looks at it. Whole-object replacement (not field-patching) so a
	// crafted Config["credentials_json"]/["base_url"] cannot survive.
	// Placed right after decode so validation, the provider allow-list,
	// and persistence all see the canonical gateway config — a crafted
	// POST is overridden (200), never rejected.
	managedai.Apply(&p)

	if msg := validateLLMConfig(p.LLM.Provider, p.LLM.Config); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// Sanitize Language at create time too — the field flows into LLM
	// system prompts, so the same prompt-injection guard applies.
	if cleanLang, err := sanitizeLanguage(p.Language); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	} else {
		p.Language = cleanLang
	}

	// Reject a split-brain body that sets BOTH the legacy `warehouse` field and
	// the new `warehouses` slice. EffectiveWarehouses() would silently pick the
	// slice and persist a divergent legacy field, so any code still reading
	// project.Warehouse directly would act on a different datasource than the
	// agent's EffectiveWarehouses()-based paths. Require one canonical shape.
	if p.Warehouse.Provider != "" && len(p.Warehouses) > 0 {
		writeError(w, http.StatusBadRequest,
			"set either the legacy `warehouse` field or `warehouses`, not both")
		return
	}

	if !rejectAnchoringPromotion(w, p.EffectiveWarehouses()) {
		return
	}
	// No project id: the refusal happens before the insert that assigns one,
	// so there is nothing yet to attribute this to but the providers. Passing
	// p.ID here would look like attribution and be empty in practice.
	if !rejectUnanchoredProject(w, p.EffectiveWarehouses(), telemetry.AnchoringAtProjectCreate, "") {
		return
	}

	// Multi-warehouse projects cannot be configured through this create path:
	// it neither enforces the multi_warehouse_enabled feature gate nor reserves
	// data-source quota per warehouse (each warehouse bills as one data source,
	// and the reconciliation counters would also have to sum warehouses). Adding
	// more than one datasource must go through the dedicated, gated
	// warehouse-management flow. A single warehouse — via the legacy `warehouse`
	// field OR a one-entry `warehouses` — is allowed and treated identically
	// below (one data source, one index).
	if len(p.EffectiveWarehouses()) > 1 {
		writeError(w, http.StatusBadRequest,
			"multiple data sources cannot be configured at project creation; create the project with a single warehouse, then add more through warehouse management")
		return
	}

	// Normalize a single-datasource create supplied via the new `warehouses`
	// slice down to the canonical legacy `warehouse` shape. Existing readers
	// still consume project.warehouse (the settings UI, the data-source
	// reconciliation counter), and the agent's EffectiveWarehouses() synthesizes
	// the same single default warehouse from it — so both sides agree and no
	// project is left looking warehouse-less. The id is cleared so it resolves to
	// the default and keeps the legacy "warehouse-credentials" secret key.
	if len(p.Warehouses) == 1 {
		p.Warehouse = p.Warehouses[0]
		p.Warehouse.ID = ""
		p.Warehouses = nil
		p.PrimaryWarehouseID = ""
	}

	// Seed default prompts from domain pack.
	//
	// After the warehouse checks above, not before them, because the pack has
	// to agree with the datasource it will be run against and neither the
	// split-brain body nor an unanchored project has a settled answer to
	// "which datasource is that". Nothing between here and the decode reads
	// prompts, so the move costs nothing.
	if h.domainPackRepo != nil {
		pack, err := h.domainPackRepo.GetBySlug(r.Context(), p.Domain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load domain pack: "+err.Error())
			return
		}
		// Unknown domain is refused only when the request needed the pack to
		// seed from, which is the behaviour this route has always had.
		// Rejecting it for a client that supplied its own prompts would be a
		// second change riding along with this one.
		if pack == nil {
			if p.Prompts == nil {
				writeError(w, http.StatusBadRequest, "domain pack not found: "+p.Domain)
				return
			}
		} else {
			// The pairing is checked whether or not the prompts came from the
			// pack. Supplying custom prompts does not make the project's domain
			// compatible with its data source — it only stops the pack being
			// copied — and gating this on the seeding branch left the whole
			// refusal skippable by sending a `prompts` field.
			if msg := packShapeMismatch(pack, p.PrimaryWarehouse()); msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			if p.Prompts == nil {
				SeedProjectPrompts(&p, pack)
			}
		}
	}

	// Plan-gate: provider allow-list. Self-hosted Noop permits everything.
	ck := policy.GetChecker()
	if err := ck.CheckLLMProviderAllowed(r.Context(), "", p.LLM.Provider); err != nil {
		if writePolicyError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return
	}

	// Plan-gate: projects-per-deployment. Reservation is consumed on a
	// successful repo insert and released on failure.
	res, err := ck.CheckCreateProject(r.Context(), "", policy.ProjectIntent{
		ProjectID:   p.ID,
		Name:        p.Name,
		LLMProvider: p.LLM.Provider,
	})
	if err != nil {
		if writePolicyError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
		return
	}

	// Plan-gate: data-sources-per-deployment. The create path allows at most one
	// warehouse (guarded above), so a project that configures a warehouse — via
	// the legacy field or a one-entry `warehouses` — is adding exactly one data
	// source. Self-hosted Noop is a no-op.
	var dsRes *policy.Reservation
	if len(p.EffectiveWarehouses()) > 0 {
		dsRes, err = ck.CheckAddDataSource(r.Context(), "")
		if err != nil {
			if res != nil {
				if relErr := ck.Release(r.Context(), res.ID); relErr != nil {
					apilog.WithError(relErr).Warn("failed to release project-create reservation after data-source denial")
				}
			}
			if writePolicyError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
			return
		}
	}

	if err := h.repo.Create(r.Context(), &p); err != nil {
		apilog.WithError(err).Error("Failed to create project")
		if res != nil {
			if relErr := ck.Release(r.Context(), res.ID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release project-create reservation after insert failure")
			}
		}
		if dsRes != nil {
			if relErr := ck.Release(r.Context(), dsRes.ID); relErr != nil {
				apilog.WithError(relErr).Warn("failed to release data-source reservation after insert failure")
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}

	// The provider used for logging/telemetry comes from the primary warehouse
	// so a one-entry `warehouses` create (empty legacy field) is attributed
	// correctly, not blank.
	primaryProvider := p.PrimaryWarehouse().Provider
	apilog.WithFields(apilog.Fields{
		"project_id": p.ID,
		"name":       p.Name,
		"domain":     p.Domain,
		"category":   p.Category,
		"llm":        p.LLM.Provider,
		"warehouse":  primaryProvider,
	}).Info("Project created")

	telemetry.TrackProjectCreated(primaryProvider, p.LLM.Provider, p.Domain)

	// Enqueue the new project for schema indexing. A project without a
	// warehouse (blank-state) cannot be indexed; it will transition to
	// pending_indexing on its first PUT that adds one. We set this
	// explicitly rather than defaulting in the repo so reads without a
	// warehouse still see SchemaIndexStatus == "" (→ "not yet
	// configured" in the dashboard).
	if len(p.EffectiveWarehouses()) > 0 {
		if err := h.repo.SetSchemaIndexStatus(r.Context(), p.ID, models.SchemaIndexStatusPendingIndexing, ""); err != nil {
			apilog.WithError(err).Warn("schema-index: failed to enqueue new project; user must click Re-index manually")
		} else {
			p.SchemaIndexStatus = models.SchemaIndexStatusPendingIndexing
		}
	}

	writeJSON(w, http.StatusCreated, p)
}

// List returns all projects.
// GET /api/v1/projects
func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	projects, err := h.repo.List(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects: "+err.Error())
		return
	}

	if projects == nil {
		projects = make([]*models.Project, 0)
	}

	h.enrichLastRun(r.Context(), projects)

	writeJSON(w, http.StatusOK, projects)
}

// Get returns a project by ID.
// GET /api/v1/projects/{id}
func (h *ProjectsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	h.enrichLastRun(r.Context(), []*models.Project{p})

	writeJSON(w, http.StatusOK, p)
}

// Update updates a project.
// PUT /api/v1/projects/{id}
// Merges incoming fields with existing project — preserves fields not in the request
// (e.g., settings page doesn't send prompts, prompts page doesn't send warehouse).
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	var incoming models.Project
	if err := json.Unmarshal(bodyBytes, &incoming); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	// Which top-level fields the client actually sent — lets the datasource guard
	// distinguish an explicit "warehouses":[] (a removal attempt) from an omitted
	// field, since both decode to a zero-length slice.
	var sentFields map[string]json.RawMessage
	_ = json.Unmarshal(bodyBytes, &sentFields)
	_, warehousesSent := sentFields["warehouses"]

	// Managed-inference override (no-op unless AI_GATEWAY_URL is set):
	// canonicalize the incoming AI config to the gateway preset before
	// the checks and the merge below, so a crafted PUT that sets a
	// different provider/model/base_url is discarded — the merged
	// `existing` persists the preset, not the request body.
	managedai.Apply(&incoming)

	// Plan-gate: if the request changes the LLM provider, validate the
	// new provider against the plan's allow-list before persisting.
	if incoming.LLM.Provider != "" && incoming.LLM.Provider != existing.LLM.Provider {
		if err := policy.GetChecker().CheckLLMProviderAllowed(r.Context(), "", incoming.LLM.Provider); err != nil {
			if writePolicyError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "policy check failed: "+err.Error())
			return
		}
	}

	// Validate provider-specific LLM config (e.g., wire_override syntax).
	if incoming.LLM.Provider != "" {
		if msg := validateLLMConfig(incoming.LLM.Provider, incoming.LLM.Config); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}

	// Merge: update only fields that are present in the request
	if incoming.Name != "" {
		existing.Name = incoming.Name
	}
	if incoming.Description != "" || incoming.Name != "" {
		existing.Description = incoming.Description
	}
	// Datasources are NOT managed through this settings route (it neither gates
	// the multi_warehouse feature nor reserves per-warehouse quota nor enqueues
	// indexing). Reject a request that would CHANGE datasources while letting an
	// unchanged echo through — a full-object round-trip (GET → tweak one setting
	// → PUT) re-sends the current datasources verbatim, and the merge below
	// ignores the datasource fields regardless. Two forbidden shapes:
	//   - `warehouses` present and different from what's stored (add/edit/remove,
	//     or turning a single/legacy project multi-warehouse via this route); and
	//   - a legacy `warehouse` edit on a project that is ALREADY multi-warehouse,
	//     where EffectiveWarehouses() ignores the legacy field so the edit would
	//     silently no-op.
	// Datasource edits go through the dedicated, gated warehouse-management flow.
	// A `warehouses` field is an edit when it was actually sent and differs from
	// what's stored — including an explicit "warehouses":[] on a multi-warehouse
	// project (a removal). Treat nil and empty as equal so a client that echoes
	// an empty list on a single-warehouse project isn't spuriously rejected.
	warehousesEdited := warehousesSent &&
		(len(incoming.Warehouses) > 0 || len(existing.Warehouses) > 0) &&
		!reflect.DeepEqual(incoming.Warehouses, existing.Warehouses)
	legacyEditedOnMulti := len(existing.Warehouses) > 0 && incoming.Warehouse.Provider != "" &&
		!reflect.DeepEqual(incoming.Warehouse, existing.Warehouse)
	if warehousesEdited || legacyEditedOnMulti {
		writeError(w, http.StatusBadRequest,
			"data sources cannot be edited through this settings route; use warehouse management")
		return
	}
	// A legacy `warehouse` edit remains the way to change a single-warehouse
	// project's datasource. Never apply it to a multi-warehouse project — there
	// the value above was an unchanged echo, and EffectiveWarehouses() ignores
	// the legacy field anyway.
	if incoming.Warehouse.Provider != "" && len(existing.Warehouses) == 0 {
		if !rejectAnchoringPromotion(w, []models.WarehouseConfig{incoming.Warehouse}) {
			return
		}
		// A single-warehouse project's only datasource IS the project's
		// anchor, so swapping it for a non-anchoring source leaves nothing to
		// carry the project — the same end state the create path refuses, one
		// edit later.
		if !rejectUnanchoredProject(w, []models.WarehouseConfig{incoming.Warehouse}, telemetry.AnchoringAtSettingsEdit, existing.ID) {
			return
		}
		// The pairing is checked wherever it changes, and this is the other
		// place it does. A project created before it had a datasource was
		// seeded from its pack with nothing to disagree with; the shape only
		// becomes checkable here, one edit later — which is also the flow a
		// customer takes when they set the project up before connecting
		// anything.
		if !h.rejectPackShapeMismatch(r.Context(), w, existing.Domain, incoming.Warehouse) {
			return
		}
		existing.Warehouse = incoming.Warehouse
	}
	if incoming.LLM.Provider != "" {
		existing.LLM = incoming.LLM
	}
	if incoming.Profile != nil {
		existing.Profile = incoming.Profile
	}
	if incoming.Prompts != nil {
		existing.Prompts = incoming.Prompts
	}
	if incoming.Embedding.Provider != "" {
		existing.Embedding = incoming.Embedding
	}
	// Language: empty means "field not present" (preserve existing) so a
	// PUT that does not touch language never overwrites a configured
	// value. To clear, send the literal "English" (which is the default).
	// Sanitization rejects control characters and oversize values to close
	// the prompt-injection vector — Language is rendered into LLM system
	// prompts verbatim via {{LANGUAGE}}.
	if incoming.Language != "" {
		clean, err := sanitizeLanguage(incoming.Language)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing.Language = clean
	}
	// BlurbLLM is a pointer — nil means "field not present in this
	// request" (preserve existing), an empty-provider value means "user
	// cleared the override" (so we clear it too).
	if incoming.BlurbLLM != nil {
		if incoming.BlurbLLM.Provider == "" {
			existing.BlurbLLM = nil
		} else {
			existing.BlurbLLM = incoming.BlurbLLM
		}
	}

	// State transitions go through dedicated lifecycle endpoints
	// (plugins own their own state transition logic). The Update
	// handler does NOT accept arbitrary state writes from the
	// request body — that would let any caller move a project out
	// of a plugin-managed state mid-flight. Plugins that need a
	// state-changing PUT mount their own route via the route-group
	// extension point.

	// ValidationEnabled is a pointer field — nil in the request means
	// "do not touch", non-nil means "set to this value" (including
	// false, which is the whole point of the toggle).
	if incoming.ValidationEnabled != nil {
		existing.ValidationEnabled = incoming.ValidationEnabled
	}

	// SmartOverflowEnabled: same nil-means-untouched pointer semantics.
	if incoming.SmartOverflowEnabled != nil {
		existing.SmartOverflowEnabled = incoming.SmartOverflowEnabled
	}

	// ReasoningEnabled: same nil-means-untouched pointer semantics.
	if incoming.ReasoningEnabled != nil {
		existing.ReasoningEnabled = incoming.ReasoningEnabled
	}

	// RecommendationVerdicts is a slice — nil means "do not touch", non-nil
	// (including an explicit empty array, which EffectiveRecommendationVerdicts
	// resolves back to the default) means "replace with this set". The
	// dashboard sends the field only when the operator changes it, always as
	// the full desired selection, so replace-on-present is correct.
	if incoming.RecommendationVerdicts != nil {
		existing.RecommendationVerdicts = incoming.RecommendationVerdicts
	}

	if err := h.repo.Update(r.Context(), id, existing); err != nil {
		apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Error("Failed to update project")
		writeError(w, http.StatusInternalServerError, "failed to update project: "+err.Error())
		return
	}

	apilog.WithField("project_id", id).Info("Project updated")
	writeJSON(w, http.StatusOK, existing)
}

// Delete fully removes a project: every Mongo collection, the Qdrant
// per-project schema-index collection, and (when the configured secret
// provider supports it) every secret stored under the project's
// namespace.
//
// External secret managers (GCP/AWS/Azure) intentionally do NOT have
// a server-driven delete path — those credentials must be cleaned
// up via the cloud console / IAM-audited tooling. The handler reports
// `secrets_skipped: true` in that case so the UI can surface the
// follow-up to the user.
//
// Pre-flight checks:
//   - 404 if the project doesn't exist
//   - 409 if a schema-indexing run is in flight (caller must cancel
//     first via /schema-index/cancel — auto-cancelling during a
//     destructive operation is too easy to misread)
//
// On success returns 200. The cascade is best-effort within the
// project: a Qdrant or secret failure logs but doesn't abort the
// Mongo cascade — a re-issued Delete is idempotent and finishes any
// step that didn't land.
//
// DELETE /api/v1/projects/{id}
func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// 409 if indexing — the user must cancel through the dedicated
	// endpoint first. Auto-cancelling during a destructive op would
	// silently abort someone else's hour-long run.
	if p.SchemaIndexStatus == models.SchemaIndexStatusIndexing {
		writeError(w, http.StatusConflict, "schema indexing is in flight; cancel first via POST /api/v1/projects/"+id+"/schema-index/cancel before deleting the project")
		return
	}

	// Drop Qdrant collection (best-effort). On failure we log and
	// continue: BuildIndex drops on entry next time, so a leftover
	// collection isn't catastrophic — but the user gets the audit
	// log line so they know cleanup is partial.
	if h.dropper != nil {
		if err := h.dropper.DropCollection(r.Context(), id); err != nil {
			apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Warn("Project delete: Qdrant drop failed; cascade continues")
		}
	}

	// Sweep secrets ONLY when the configured provider implements
	// secrets.ProjectDeleter (mongodb-backed). External secret
	// managers (gcp/aws/azure) deliberately route deletion through
	// IAM-audited tooling — the API never reaches into them.
	secretsSkipped := true
	if h.secretProvider != nil {
		if del, ok := h.secretProvider.(secrets.ProjectDeleter); ok {
			if err := del.DeleteAllForProject(r.Context(), id); err != nil {
				apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Warn("Project delete: secret sweep failed; cascade continues")
			} else {
				secretsSkipped = false
			}
		}
	}

	if err := h.repo.DeleteCascade(r.Context(), id); err != nil {
		apilog.WithFields(apilog.Fields{"project_id": id, "error": err.Error()}).Error("Project delete: Mongo cascade failed")
		writeError(w, http.StatusInternalServerError, "delete cascade: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{"project_id": id, "secrets_skipped": secretsSkipped}).Info("Project deleted")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":         id,
		"secrets_skipped": secretsSkipped,
	})
}

// packShapeMismatch reports why a domain pack cannot seed this project's
// prompts, or "" when it can.
//
// A pack's prompts are written for one shape of source and are not portable
// across shapes. An entities pack tells the model to select from tables and
// names them through {{DATASET}}; a cube pack tells it to choose metrics and
// dimensions and has no dataset to name. Seeding either into a project whose
// primary datasource is the other shape yields a discovery run that reads
// correctly and asks for queries the source cannot answer — a failure with no
// error attached, which is the expensive kind.
//
// This could not happen while every pack was table-shaped. It became
// reachable the moment a cube pack could be saved, so it is checked at the
// one place a pack and a datasource are first paired. It is not a validator
// rule: a pack is not invalid for being cube-shaped, it is only wrong HERE.
//
// A project with no datasource yet is still checked, against entities. That is
// not a guess: the pack has to agree with the datasource the project will end
// up with, that datasource must be one that can carry an analysis on its own,
// and no cube-shaped source can be. Returning "no mismatch" here instead —
// which this did at first, on the reasoning that nothing had been chosen to
// disagree with — creates a project holding a cube pack that the settings-edit
// guard then refuses to give any anchoring datasource to. Unusable, and
// unrepairable through the API, built out of two guards disagreeing about the
// empty case. It is also exactly the rule the domain picker applies.
//
// An unregistered provider is read as table-shaped, the same default the rest
// of the system applies, so an unknown spelling keeps the check rather than
// waving a pack through.
func packShapeMismatch(pack *models.DomainPack, primary models.WarehouseConfig) string {
	if pack == nil {
		return ""
	}
	want := gowarehouse.ShapeEntities
	if primary.Provider != "" {
		if meta, ok := gowarehouse.GetProviderMeta(primary.Provider); ok {
			want = meta.EffectiveShape()
		}
	}
	got := pack.EffectiveShape()
	if got == want {
		return ""
	}
	if primary.Provider == "" {
		return fmt.Sprintf(
			"domain pack %q is written for a %s data source, and a project's domain pack has to match the data source that carries it, which is always %s; choose a pack written for %s",
			pack.Slug, got, want, want)
	}
	return fmt.Sprintf(
		"domain pack %q is written for a %s data source, but this project's data source is %s; choose a pack written for %s",
		pack.Slug, got, want, want)
}

// rejectUnanchoredProject refuses a datasource set in which nothing can carry
// the project, writing a 400 and returning false.
//
// An EMPTY set passes. A project with no datasources yet is how every project
// starts, and refusing it here would make the product unusable to say
// something true; the run paths refuse an empty set on their own terms.
//
// What is refused is a set that HAS datasources, none of which is a system of
// record. Such a project reaches the agent looking perfectly healthy and
// produces analysis that restates what the source's own reporting already
// shows — confidently, and with no error anyone would connect to the cause.
// Refusing at configuration time is the only point where the message can name
// the fix.
// rejectPackShapeMismatch is packShapeMismatch over a project's saved pack
// slug, writing a 400 and returning false on a mismatch.
//
// A pack that cannot be loaded is not a refusal. The pack may have been
// deleted or renamed since the project was created, and blocking an unrelated
// settings edit on that would be a worse outcome than the mismatch it is
// guarding against — which the create path already catches for every project
// that had a datasource to check.
func (h *ProjectsHandler) rejectPackShapeMismatch(ctx context.Context, w http.ResponseWriter, domain string, wh models.WarehouseConfig) bool {
	if h.domainPackRepo == nil || domain == "" {
		return true
	}
	pack, err := h.domainPackRepo.GetBySlug(ctx, domain)
	if err != nil || pack == nil {
		return true
	}
	if msg := packShapeMismatch(pack, wh); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return false
	}
	return true
}

func rejectUnanchoredProject(w http.ResponseWriter, whs []models.WarehouseConfig, at, projectID string) bool {
	if len(whs) == 0 || models.AnyAnchors(whs) {
		return true
	}
	recordAnchoringRefusal(at, projectID, whs)
	writeError(w, http.StatusBadRequest,
		"this project has no data source that can carry an analysis on its own; "+
			"add one that is a system of record, or turn `anchoring` back on for a source you demoted")
	return false
}

// recordAnchoringRefusal makes a refusal visible after the fact.
//
// Two sinks because they answer different questions and neither answers both.
// The counter says how often the rule fires and at which site, which is the
// only measure of whether a feature whose job is to say no is calibrated —
// and it carries no identifiers, like every other event. The log line names
// the project and the providers involved, which is what an operator needs when
// a customer asks why their setup was refused.
//
// A package-level var so a test can substitute it and assert which SITE
// refused. That is the part worth pinning and the part a type checker cannot
// help with: every call passes a string, and a copy-pasted one is wrong in a
// way nothing surfaces — the refusal still works, and the counts quietly
// attribute it to the wrong place.
var recordAnchoringRefusal = func(at, projectID string, whs []models.WarehouseConfig) {
	telemetry.TrackAnchoringRefused(at, strings.Join(anchoringProviders(whs), ","))
	apilog.WithFields(apilog.Fields{
		"at":         at,
		"project_id": projectID,
		"providers":  anchoringProviders(whs),
	}).Warn("anchoring refused: no data source can carry this project")
}

// anchoringProviders lists the provider slugs of the datasources that were
// refused, skipping blank placeholder rows — a row with no provider is not a
// datasource anyone chose, and naming it in the count would attribute the
// refusal to a source that does not exist.
func anchoringProviders(whs []models.WarehouseConfig) []string {
	out := make([]string, 0, len(whs))
	for _, wh := range whs {
		if wh.Provider != "" {
			out = append(out, wh.Provider)
		}
	}
	return out
}

// rejectAnchoringPromotion refuses a request that tries to promote a datasource
// whose provider declares it cannot anchor, writing a 400 and returning false.
//
// The override may only DEMOTE. Storing a promotion instead of refusing it
// would be the worse failure: EffectiveAnchoring applies the provider as a
// ceiling and would ignore the stored value, so the setting would read back as
// applied while changing nothing — and the user would believe their
// enrichment-only source had been made able to carry the project.
func rejectAnchoringPromotion(w http.ResponseWriter, whs []models.WarehouseConfig) bool {
	for _, wh := range whs {
		if wh.Anchoring == nil || wh.Provider == "" {
			continue
		}
		if !gowarehouse.AnchoringOverrideAllowed(wh.Provider, *wh.Anchoring) {
			// A promotion is refused for a different reason from an
			// unanchored set — the operator asked for something the provider
			// cannot do, rather than arriving at a state nothing can carry —
			// and it is worth telling apart in the counts.
			telemetry.TrackAnchoringRefused(telemetry.AnchoringAtPromotion, wh.Provider)
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"data source %q uses provider %q, which cannot anchor a project; `anchoring` may be turned off for a source but never on",
				wh.ID, wh.Provider))
			return false
		}
	}
	return true
}
