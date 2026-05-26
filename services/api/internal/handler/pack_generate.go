package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/packgen"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// finalizeBudget bounds the safety-net `FinalizePackGenIfStuck` call the
// goroutine runs in its defer. Kept generous because the orchestrator's
// own revert path may already have updated the document under us; the
// detached context here just guarantees the cleanup attempt completes
// even when the inbound request's context is gone.
const finalizeBudget = 5 * time.Second

// finalizeInterruptedErr is the message the safety-net defer writes to
// `pack_gen_last_error` when it has to clean up after a goroutine that
// did not reach a terminal write. The wizard surfaces this string verbatim,
// so it must be self-explanatory to a non-technical operator.
const finalizeInterruptedErr = "generation interrupted; please retry"

// PackGenerateHandler exposes pack-generation endpoints.
//
// Generate is now asynchronous: the handler validates inputs, atomically
// writes a `pack_gen_request` launch marker to the project document via
// EnqueuePackGen, spawns a goroutine that runs the orchestrator on a
// server-lifetime context (decoupled from the inbound HTTP request),
// and returns 202 immediately. The dashboard polls project state to
// observe transitions.
//
// RegenerateSection remains synchronous — it is a single LLM call that
// fits comfortably inside the dashboard proxy's request budget.
type PackGenerateHandler struct {
	repo database.ProjectRepo

	// serverCtx is canceled by apiserver shutdown BEFORE the HTTP drain
	// completes, giving each in-flight goroutine its chance to write a
	// terminal state via the orchestrator's revertOnError closure or
	// (failing that) via the handler-side defer below.
	serverCtx context.Context

	// wg is the apiserver's pack-gen goroutine WaitGroup. The shutdown
	// path waits on this (bounded) after canceling serverCtx so Mongo
	// is still connected while terminal writes execute.
	wg *sync.WaitGroup
}

// NewPackGenerateHandler constructs a PackGenerateHandler.
//
// serverCtx and wg are required (apiserver.Run passes them). Both nil
// is acceptable only in tests that exercise only the validation path
// (where no goroutine is ever spawned).
func NewPackGenerateHandler(repo database.ProjectRepo, serverCtx context.Context, wg *sync.WaitGroup) *PackGenerateHandler {
	return &PackGenerateHandler{repo: repo, serverCtx: serverCtx, wg: wg}
}

// generateResponse is the JSON body the async Generate handler returns.
// Field shape preserves the public `GenerateResult` contract (run_id +
// async flag) so downstream consumers do not break.
type generateResponse struct {
	RunID string `json:"run_id"`
	Async bool   `json:"async"`
}

// Generate kicks off an async pack generation. POST
// /api/v1/projects/{id}/pack-generate.
//
// Response codes:
//   - 202 Accepted on a fresh enqueue OR an idempotent duplicate (marker
//     already set for an in-flight run); body carries the run_id.
//   - 404 when no pack-gen provider is registered, or the project does
//     not exist.
//   - 409 when the project state is not pack_generation_pending AND the
//     marker is absent — typically the project already completed
//     generation, or has been hand-edited into an inconsistent state
//     (operator runbook covers recovery).
//   - 400 when the project's GeneratePack payload is missing or disabled.
//   - 500 on a transient state-classification failure (race between our
//     enqueue and another writer that subsequent retries should clear).
func (h *PackGenerateHandler) Generate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	if !packgen.IsAvailable() {
		writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
		return
	}

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Marker-first idempotency: if a run is already in flight for this
	// project, return 202 with its run_id regardless of the project's
	// current state. The dashboard polls and observes the eventual
	// transition.
	if p.PackGenRequest != nil {
		if p.EffectiveState() == models.ProjectStatePackGenerationDone {
			// Stale-marker case — the orchestrator's success write should
			// have unset the field. Returning 202 here would mislead the
			// dashboard into expecting more transitions.
			writeError(w, http.StatusConflict, "pack generation already complete")
			return
		}
		writeJSON(w, http.StatusAccepted, generateResponse{
			RunID: p.PackGenRequest.RunID,
			Async: true,
		})
		return
	}

	// No marker — classify from state before attempting the enqueue so
	// the user gets a precise 400/409 instead of a generic 500.
	switch p.EffectiveState() {
	case models.ProjectStatePackGenerationPending:
		// Proceed to enqueue.
	case models.ProjectStatePackGenerationDone:
		writeError(w, http.StatusConflict, "pack generation already complete")
		return
	case models.ProjectStatePackGeneration:
		// Marker is absent but state says a run is in progress — the
		// previous run died WITHOUT writing a terminal state. Operator
		// recovery is documented in the enterprise runbook.
		writeError(w, http.StatusInternalServerError, "project is in pack_generation without an active run marker; operator intervention required")
		return
	default:
		writeError(w, http.StatusConflict, "project is not in pack_generation_pending state (current: "+p.EffectiveState()+")")
		return
	}
	if p.GeneratePack == nil || !p.GeneratePack.Enabled {
		writeError(w, http.StatusBadRequest, "project has no pending pack-generation request")
		return
	}

	// Run-ID format mirrors the schema-indexer's so log scraping and
	// dashboard correlation work consistently across run modes.
	runID := time.Now().UTC().Format("20060102T150405.000Z")

	existingRunID, alreadyQueued, err := h.repo.EnqueuePackGen(r.Context(), id, runID)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrPackGenNotEnabled):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, database.ErrPackGenStateMismatch):
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enqueue pack-gen: "+err.Error())
		return
	}
	if alreadyQueued {
		// Race: another POST won between our GetByID + EnqueuePackGen.
		// The winner's run_id is what we report; no goroutine spawn.
		writeJSON(w, http.StatusAccepted, generateResponse{
			RunID: existingRunID,
			Async: true,
		})
		return
	}

	req := packgen.GenerateRequest{
		ProjectID:   p.ID,
		RunID:       runID,
		PackName:    p.GeneratePack.PackName,
		PackSlug:    p.GeneratePack.PackSlug,
		Description: p.GeneratePack.Description,
	}

	// Spawn the goroutine on the server-lifetime context so a client
	// disconnect (proxy timeout, browser close) does not abort the run.
	// The defer is the safety net for the two cases the orchestrator
	// itself can't cover: a panic, or a returned error where the
	// orchestrator's own revert-on-error closure also failed (e.g.
	// the detached-context Mongo write timed out). It is NOT fired on
	// a clean provider return — that contract is the provider's job:
	//   - Async:false providers write terminal state synchronously
	//     before returning (the current in-process enterprise
	//     orchestrator does this) — running the safety net would at
	//     best be a no-op (predicate misses), at worst race the
	//     provider's own success write.
	//   - Async:true providers (hypothetical future contract) return
	//     to acknowledge handoff while their own worker continues with
	//     the marker still legitimately set — running the safety net
	//     would falsely clear that marker mid-run.
	h.wg.Add(1)
	go func(projectID string) {
		defer h.wg.Done()
		var provErr error
		defer func() {
			recovered := recover()
			// Clean return — trust the provider's terminal-state contract.
			if recovered == nil && provErr == nil {
				return
			}
			if recovered != nil {
				apilog.WithFields(apilog.Fields{
					"project_id": projectID,
					"recover":    recovered,
				}).Error("pack-generate: goroutine panic")
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), finalizeBudget)
			defer cancel()
			if err := h.repo.FinalizePackGenIfStuck(cleanupCtx, projectID, finalizeInterruptedErr); err != nil {
				apilog.WithFields(apilog.Fields{
					"project_id": projectID,
					"error":      err.Error(),
				}).Warn("pack-generate: safety-net finalize failed")
			}
		}()

		var res *packgen.GenerateResult
		res, provErr = packgen.GetProvider().Generate(h.serverCtx, req)
		if provErr != nil {
			apilog.WithFields(apilog.Fields{
				"project_id": projectID,
				"run_id":     runID,
				"error":      provErr.Error(),
			}).Info("pack-generate: in-process generation returned error (orchestrator owns state writes; safety net may follow)")
			return
		}
		async := res != nil && res.Async
		apilog.WithFields(apilog.Fields{
			"project_id": projectID,
			"run_id":     runID,
			"async":      async,
		}).Info("pack-generate: in-process generation completed")
	}(p.ID)

	apilog.WithFields(apilog.Fields{
		"project_id": id,
		"pack_slug":  req.PackSlug,
		"run_id":     runID,
	}).Info("pack-generate: dispatched async")

	writeJSON(w, http.StatusAccepted, generateResponse{
		RunID: runID,
		Async: true,
	})
}

// regenerateSectionRequest is the JSON body shape of POST
// /api/v1/projects/{id}/pack-generate/regenerate. It maps onto
// packgen.RegenerateSectionRequest with the project ID coming from the
// URL and the pack slug from the project document.
type regenerateSectionRequest struct {
	Section  string `json:"section"`
	Feedback string `json:"feedback"`
}

// RegenerateSection synchronously re-emits a single section of the
// project's draft pack using user-supplied feedback. POST
// /api/v1/projects/{id}/pack-generate/regenerate.
//
// Pre-conditions:
//   - Project must exist (404 if not).
//   - A non-no-op packgen Provider must be configured (404 otherwise).
//   - Project state must be pack_generation_done (409 otherwise — section
//     regeneration mutates a draft pack the user is reviewing).
//   - Project.Domain (filled at the end of generation) must be set.
func (h *PackGenerateHandler) RegenerateSection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}

	if !packgen.IsAvailable() {
		writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
		return
	}

	var body regenerateSectionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Section == "" {
		writeError(w, http.StatusBadRequest, "section is required")
		return
	}
	if body.Feedback == "" {
		writeError(w, http.StatusBadRequest, "feedback is required")
		return
	}

	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project: "+err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if p.EffectiveState() != models.ProjectStatePackGenerationDone {
		writeError(w, http.StatusConflict, "project is not in pack_generation_done state (current: "+p.EffectiveState()+")")
		return
	}
	if p.Domain == "" {
		writeError(w, http.StatusBadRequest, "project has no associated pack to regenerate")
		return
	}

	req := packgen.RegenerateSectionRequest{
		ProjectID: p.ID,
		PackSlug:  p.Domain,
		Section:   body.Section,
		Feedback:  body.Feedback,
	}
	res, err := packgen.GetProvider().RegenerateSection(r.Context(), req)
	if err != nil {
		if errors.Is(err, packgen.ErrNotConfigured) {
			writeError(w, http.StatusNotFound, "pack generation is not available on this deployment")
			return
		}
		apilog.WithFields(apilog.Fields{"project_id": id, "section": body.Section, "error": err.Error()}).Error("pack-generate: regenerate section failed")
		writeError(w, http.StatusInternalServerError, "regenerate failed: "+err.Error())
		return
	}

	apilog.WithFields(apilog.Fields{
		"project_id": id,
		"section":    body.Section,
		"attempts":   res.Attempts,
	}).Info("pack-generate: section regenerated")
	writeJSON(w, http.StatusOK, res)
}
