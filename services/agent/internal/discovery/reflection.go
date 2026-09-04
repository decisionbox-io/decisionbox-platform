package discovery

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

//go:embed prompts/reflection.md
var reflectionPromptTemplate string

// Env knobs for the reflection / consolidation phase (Rule 2 — all parametric).
const (
	// discoveryReflectionEnabledEnv is the deployment-availability gate (Layer
	// A). Default off: the ledger loop only earns its keep where the enterprise
	// RAG + evolution workflow can consume it, so the enterprise agent Helm
	// overlay turns it on. Independent of the per-project Settings toggle
	// (Layer B, default on).
	discoveryReflectionEnabledEnv    = "DISCOVERY_REFLECTION_ENABLED"
	discoveryReflectionTimeoutEnv    = "DISCOVERY_REFLECTION_TIMEOUT"
	discoveryReflectionMaxOutputEnv  = "DISCOVERY_REFLECTION_MAX_OUTPUT"
	discoveryReflectionParseRetryEnv = "DISCOVERY_REFLECTION_PARSE_MAX_RETRIES"
	discoveryLedgerMaxFindingsEnv    = "DISCOVERY_LEDGER_MAX_FINDINGS"
	discoveryLedgerDedupMinScoreEnv  = "DISCOVERY_LEDGER_DEDUP_MINSCORE"
	discoveryLedgerTrendDeltaEnv     = "DISCOVERY_LEDGER_TREND_DELTA"
)

const (
	defaultDiscoveryReflectionTimeout    = 3 * time.Minute
	defaultDiscoveryReflectionMaxOutput  = 3000
	defaultDiscoveryReflectionParseRetry = 1
	defaultDiscoveryLedgerMaxFindings    = 500
	defaultDiscoveryLedgerDedupMinScore  = 0.85
	defaultDiscoveryLedgerTrendDelta     = 0.2
	maxConvergenceHistory                = 50
	maxPriorFindingsInPrompt             = 60
	maxLedgerTasksInPrompt               = 40
)

// ledgerStore / findingStore / taskStore / proposalStore are the persistence
// surfaces the reflection phase needs, held as interfaces so unit tests can
// inject fakes without MongoDB. The concrete writers are the *database.Ledger*
// repositories, wired by agentserver.go.
type ledgerStore interface {
	Get(ctx context.Context, projectID string) (*commonmodels.DiscoveryLedger, error)
	Save(ctx context.Context, l *commonmodels.DiscoveryLedger) error
}

type findingStore interface {
	List(ctx context.Context, projectID string) ([]commonmodels.LedgerFinding, error)
	Upsert(ctx context.Context, f *commonmodels.LedgerFinding) error
	Prune(ctx context.Context, projectID string, max int) error
}

type taskStore interface {
	List(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.LedgerTask, error)
	Insert(ctx context.Context, tasks []commonmodels.LedgerTask) error
}

type proposalStore interface {
	Insert(ctx context.Context, proposals []commonmodels.PackProposal) error
	ListForProject(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.PackProposal, error)
}

// reflectionContext derives the dedicated write/LLM ctx for the reflection phase.
// Like questionsContext it uses WithoutCancel so the phase — which runs after the
// discovery is already finalized — gets its own budget independent of the
// (possibly already-expired) compute-phase deadline.
func reflectionContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := goconfig.GetEnvAsDuration(discoveryReflectionTimeoutEnv, defaultDiscoveryReflectionTimeout)
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

// RunPhaseReflection is the best-effort end-of-run reflection / consolidation
// hop. Invoked by agentserver AFTER RunDiscovery has returned and the completion
// event + telemetry have fired, so a slow (or timed-out) call can neither delay
// the user-facing completion notification nor consume the exploration step
// budget. It reads findings from the persisted result and writes the Discovery
// Ledger so the next run builds on this one. Any failure is logged and swallowed
// — the run has already succeeded. Gated by the deployment flag (Layer A), the
// per-project toggle (Layer B), and the sources entitlement.
func (o *Orchestrator) RunPhaseReflection(ctx context.Context, result *models.DiscoveryResult) {
	defer func() {
		if r := recover(); r != nil {
			applog.WithField("panic", r).Error("Reflection phase panicked; discovery run is unaffected")
		}
	}()

	if !o.reflectionEnabled {
		return // Layer B: per-project Settings toggle is off.
	}
	if !goconfig.GetEnvAsBool(discoveryReflectionEnabledEnv, false) {
		return // Layer A: feature not available on this deployment.
	}
	if o.aiClient == nil || o.ledgerRepo == nil || o.findingRepo == nil || result == nil {
		return // Missing deps (unit / single-binary builds) — nothing to do.
	}

	rctx, cancel := reflectionContext(ctx)
	defer cancel()

	// License entitlement: the ledger's value is the enterprise RAG + evolution
	// workflow, gated on the same sources entitlement the answer API uses. On
	// community / self-hosted the Noop checker returns true, so the env flag +
	// per-project toggle remain the effective gates.
	if enabled, _ := policy.GetChecker().FeatureEnabled(rctx, "", policy.FeatureSources); !enabled {
		return
	}

	// Resolve the per-project evolution policy (best-effort; default off).
	pol, perr := agentplugin.ResolveDiscoveryPolicy(rctx, o.projectID)
	if perr != nil {
		applog.WithError(perr).Warn("Failed to resolve discovery policy; using default (off)")
	}

	// 1. Capture this run's insights as durable ledger findings WITH substance,
	//    deduped/trended against prior findings. Always runs (low-risk).
	newCount, totalCount, err := o.consolidateFindings(rctx, result)
	if err != nil {
		applog.WithError(err).Warn("Reflection: finding consolidation failed")
	}

	// 2. The bounded LLM call produces the judgment-heavy outputs: coverage
	//    summary, prior-finding status re-judgement, durable learnings,
	//    next-tasks, and domain-pack deltas. Skipped cleanly on any failure.
	ref, err := o.generateReflection(rctx, result, pol)
	if err != nil {
		applog.WithError(err).Warn("Reflection: consolidation LLM call failed; ledger findings still captured")
		ref = nil
	}

	// 3. Apply the LLM outputs, each independently guarded.
	o.applyReflection(rctx, result, pol, ref)

	// 4. Update coverage + convergence on the ledger and prune findings.
	o.updateLedgerMeta(rctx, result, ref, newCount, totalCount)
	if err := o.findingRepo.Prune(rctx, o.projectID,
		goconfig.GetEnvAsInt(discoveryLedgerMaxFindingsEnv, defaultDiscoveryLedgerMaxFindings)); err != nil {
		applog.WithError(err).Warn("Reflection: ledger prune failed")
	}

	applog.WithFields(applog.Fields{
		"project_id":     o.projectID,
		"run_id":         o.runID,
		"new_findings":   newCount,
		"total_findings": totalCount,
		"evolution_mode": string(pol.EvolutionMode),
	}).Info("Reflection phase complete")
}

// consolidateFindings turns this run's insights into durable ledger findings,
// deduping (exact + semantic) and trend-marking against prior findings, and
// indexes new/updated findings into Qdrant when the embedder is wired. Returns
// the count of genuinely-new findings and the project's total afterward.
func (o *Orchestrator) consolidateFindings(ctx context.Context, result *models.DiscoveryResult) (newCount, totalCount int, err error) {
	candidates := buildFindingCandidates(result)
	prior, lerr := o.findingRepo.List(ctx, o.projectID)
	if lerr != nil {
		return 0, 0, fmt.Errorf("list prior findings: %w", lerr)
	}

	// Embed candidates once (used for both semantic dedup and indexing). Best
	// effort: on any embedding failure we fall back to exact-key dedup only.
	var vectors [][]float64
	if o.embeddingProvider != nil && o.vectorStore != nil && len(candidates) > 0 {
		texts := make([]string, len(candidates))
		for i := range candidates {
			texts[i] = ledgerFindingEmbedText(candidates[i])
		}
		if vs, embErr := o.embeddingProvider.Embed(ctx, texts); embErr != nil {
			applog.WithError(embErr).Warn("Reflection: finding embedding failed; falling back to exact-key dedup")
		} else if len(vs) == len(candidates) {
			vectors = vs
		}
	}

	trendDelta := getEnvAsFloat(discoveryLedgerTrendDeltaEnv, defaultDiscoveryLedgerTrendDelta)
	dedupMinScore := getEnvAsFloat(discoveryLedgerDedupMinScoreEnv, defaultDiscoveryLedgerDedupMinScore)
	now := time.Now()

	priorByKey := make(map[string]*commonmodels.LedgerFinding, len(prior))
	priorByID := make(map[string]*commonmodels.LedgerFinding, len(prior))
	for i := range prior {
		priorByKey[prior[i].NormalizedKey] = &prior[i]
		priorByID[prior[i].ID] = &prior[i]
	}

	// Findings to (re)index into Qdrant, paired with their vector.
	type indexItem struct {
		finding commonmodels.LedgerFinding
		vector  []float64
	}
	var toIndex []indexItem

	for i := range candidates {
		cand := candidates[i]
		var vec []float64
		if vectors != nil {
			vec = vectors[i]
		}

		match := priorByKey[cand.NormalizedKey]
		if match == nil && vec != nil {
			if id := o.searchLedgerNeighbour(ctx, vec, dedupMinScore); id != "" {
				match = priorByID[id]
			}
		}

		if match != nil {
			// Merge / trend-mark the existing finding in place.
			changed := findingMagnitudeChanged(match, &cand, trendDelta)
			match.Description = cand.Description
			match.KeyMetric = cand.KeyMetric
			match.Evidence = cand.Evidence
			match.Severity = cand.Severity
			match.AffectedCount = cand.AffectedCount
			match.Liked = match.Liked || cand.Liked
			match.SeenCount++
			match.LastSeen = now
			match.SourceDiscoveryID = result.ID
			match.UpdatedAt = now
			if changed {
				match.Status = commonmodels.LedgerFindingStatusChanged
			} else if match.Status == "" {
				match.Status = commonmodels.LedgerFindingStatusConfirmed
			}
			if err := o.findingRepo.Upsert(ctx, match); err != nil {
				applog.WithError(err).Warn("Reflection: upsert merged finding failed")
				continue
			}
			if vec != nil {
				toIndex = append(toIndex, indexItem{finding: *match, vector: vec})
			}
			continue
		}

		// A genuinely new finding.
		cand.ID = uuid.New().String()
		cand.Status = commonmodels.LedgerFindingStatusConfirmed
		cand.SeenCount = 1
		cand.FirstSeen = now
		cand.LastSeen = now
		cand.SourceDiscoveryID = result.ID
		cand.CreatedAt = now
		cand.UpdatedAt = now
		if err := o.findingRepo.Upsert(ctx, &cand); err != nil {
			applog.WithError(err).Warn("Reflection: upsert new finding failed")
			continue
		}
		newCount++
		// Register the just-inserted finding so a LATER candidate in the same run
		// with the same key merges into it (bumping seen_count) instead of writing
		// a duplicate doc and inflating newCount / the convergence signal. cand is
		// a per-iteration variable, so &cand is a distinct, heap-escaped pointer.
		inserted := cand
		priorByKey[inserted.NormalizedKey] = &inserted
		priorByID[inserted.ID] = &inserted
		if vec != nil {
			toIndex = append(toIndex, indexItem{finding: cand, vector: vec})
		}
	}

	// Index new/updated findings into the shared Qdrant collection (best-effort).
	if len(toIndex) > 0 && o.embeddingProvider != nil && o.vectorStore != nil {
		dims, dErr := resolveEmbeddingDimensions(ctx, o.embeddingProvider)
		if dErr != nil {
			applog.WithError(dErr).Warn("Reflection: resolve embedding dims failed; skipping ledger indexing")
		} else if err := o.vectorStore.EnsureCollection(ctx, dims); err != nil {
			applog.WithError(err).Warn("Reflection: ensure Qdrant collection failed; skipping ledger indexing")
		} else {
			model := o.embeddingProvider.ModelName()
			points := make([]vectorstore.Point, 0, len(toIndex))
			for _, it := range toIndex {
				points = append(points, vectorstore.Point{
					ID:     it.finding.ID,
					Vector: it.vector,
					Payload: map[string]interface{}{
						"type":            commonmodels.LedgerFindingVectorType,
						"project_id":      o.projectID,
						"analysis_area":   it.finding.Area,
						"severity":        it.finding.Severity,
						"status":          it.finding.Status,
						"embedding_model": model,
						"name":            it.finding.Name,
						"created_at":      time.Now().Format(time.RFC3339),
					},
				})
			}
			if err := o.vectorStore.Upsert(ctx, points); err != nil {
				applog.WithError(err).Warn("Reflection: Qdrant upsert of ledger findings failed")
			}
		}
	}

	totalCount = len(prior) + newCount
	return newCount, totalCount, nil
}

// searchLedgerNeighbour returns the id of the nearest prior ledger finding above
// minScore, or "" when there is none. Best-effort — a search error yields "".
func (o *Orchestrator) searchLedgerNeighbour(ctx context.Context, vec []float64, minScore float64) string {
	hits, err := o.vectorStore.Search(ctx, vec, vectorstore.SearchOpts{
		ProjectIDs: []string{o.projectID},
		Types:      []string{commonmodels.LedgerFindingVectorType},
		Limit:      1,
		MinScore:   minScore,
	})
	if err != nil || len(hits) == 0 {
		return ""
	}
	return hits[0].ID
}

// buildFindingCandidates extracts durable, substance-carrying findings from the
// run's insights.
func buildFindingCandidates(result *models.DiscoveryResult) []commonmodels.LedgerFinding {
	out := make([]commonmodels.LedgerFinding, 0, len(result.Insights))
	for i := range result.Insights {
		ins := result.Insights[i]
		name := strings.TrimSpace(ins.Name)
		if name == "" {
			continue
		}
		out = append(out, commonmodels.LedgerFinding{
			ProjectID:     result.ProjectID,
			Area:          ins.AnalysisArea,
			Name:          name,
			Description:   truncate(ins.Description, 800),
			KeyMetric:     buildKeyMetric(ins),
			Evidence:      truncate(strings.Join(ins.Indicators, "; "), 400),
			Severity:      ins.Severity,
			AffectedCount: ins.AffectedCount,
			NormalizedKey: commonmodels.NormalizedFindingKey(ins.AnalysisArea, name),
		})
	}
	return out
}

// buildKeyMetric renders a short, comparable metric string for a finding. It
// prefers the insight's domain metrics, falling back to the affected count.
func buildKeyMetric(ins models.Insight) string {
	if len(ins.Metrics) > 0 {
		keys := make([]string, 0, len(ins.Metrics))
		for k := range ins.Metrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, ins.Metrics[k]))
			if len(parts) >= 4 {
				break
			}
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprintf("affected=%d", ins.AffectedCount)
}

// findingMagnitudeChanged reports whether the new sighting of a finding is a
// trend (a changed magnitude), by comparing severity and affected-count delta.
func findingMagnitudeChanged(prior *commonmodels.LedgerFinding, cur *commonmodels.LedgerFinding, delta float64) bool {
	if prior.Severity != cur.Severity {
		return true
	}
	base := prior.AffectedCount
	if base <= 0 {
		base = 1
	}
	rel := float64(cur.AffectedCount-prior.AffectedCount) / float64(base)
	if rel < 0 {
		rel = -rel
	}
	return rel > delta
}

// ledgerFindingEmbedText builds the text embedded for semantic dedup + retrieval.
func ledgerFindingEmbedText(f commonmodels.LedgerFinding) string {
	var b strings.Builder
	if f.Area != "" {
		b.WriteString("Area: ")
		b.WriteString(f.Area)
		b.WriteString("\n")
	}
	b.WriteString(f.Name)
	if f.Description != "" {
		b.WriteString("\n")
		b.WriteString(f.Description)
	}
	if f.KeyMetric != "" {
		b.WriteString("\nMetric: ")
		b.WriteString(f.KeyMetric)
	}
	return b.String()
}

// updateLedgerMeta writes the coverage map + a convergence point onto the ledger.
func (o *Orchestrator) updateLedgerMeta(ctx context.Context, result *models.DiscoveryResult, ref *parsedReflection, newCount, totalCount int) {
	ledger, err := o.ledgerRepo.Get(ctx, o.projectID)
	if err != nil {
		applog.WithError(err).Warn("Reflection: load ledger failed; skipping coverage/convergence update")
		return
	}

	// Coverage: union the LLM-reported covered tables into the explored set, and
	// record the catalog size so a frontier count can be shown.
	if ref != nil {
		explored := map[string]struct{}{}
		for _, t := range ledger.Coverage.ExploredTables {
			explored[t] = struct{}{}
		}
		for _, t := range ref.CoveredTables {
			t = strings.TrimSpace(t)
			if t != "" {
				explored[t] = struct{}{}
			}
		}
		merged := make([]string, 0, len(explored))
		for t := range explored {
			merged = append(merged, t)
		}
		sort.Strings(merged)
		ledger.Coverage.ExploredTables = merged
		if strings.TrimSpace(ref.CoverageSummary) != "" {
			ledger.Coverage.Summary = truncate(ref.CoverageSummary, 1000)
		}
		if ledger.Coverage.AreaDepth == nil {
			ledger.Coverage.AreaDepth = map[string]int{}
		}
		for _, a := range ref.CoveredAreas {
			a = strings.TrimSpace(a)
			if a != "" {
				ledger.Coverage.AreaDepth[a]++
			}
		}
	}
	ledger.Coverage.TotalTables = len(result.Schemas)

	// Convergence: marginal-new ratio for this run.
	ratio := 0.0
	if totalCount > 0 {
		ratio = float64(newCount) / float64(totalCount)
	}
	ledger.Convergence = append(ledger.Convergence, commonmodels.ConvergencePoint{
		RunID:         o.runID,
		NewFindings:   newCount,
		TotalFindings: totalCount,
		MarginalRatio: ratio,
		Date:          time.Now(),
	})
	if len(ledger.Convergence) > maxConvergenceHistory {
		ledger.Convergence = ledger.Convergence[len(ledger.Convergence)-maxConvergenceHistory:]
	}

	if err := o.ledgerRepo.Save(ctx, ledger); err != nil {
		applog.WithError(err).Warn("Reflection: save ledger failed")
	}
}

// applyReflection persists the LLM outputs: prior-finding status updates,
// durable learnings (fixing the dead AddNote channel), next-tasks, and — when
// the evolution mode allows — proposed domain-pack deltas.
func (o *Orchestrator) applyReflection(ctx context.Context, result *models.DiscoveryResult, pol agentplugin.DiscoveryPolicy, ref *parsedReflection) {
	if ref == nil {
		return
	}

	// Prior-finding status re-judgement (confirmed/monitoring/resolved/refuted).
	// The consolidation pass already set "changed" for trends it detected; here
	// we honor the model's grounded verdicts on findings it reasoned about.
	if len(ref.StatusUpdates) > 0 {
		prior, err := o.findingRepo.List(ctx, o.projectID)
		if err == nil {
			byID := make(map[string]*commonmodels.LedgerFinding, len(prior))
			for i := range prior {
				byID[prior[i].ID] = &prior[i]
			}
			for _, su := range ref.StatusUpdates {
				f := byID[strings.TrimSpace(su.FindingID)]
				if f == nil || !commonmodels.ValidLedgerFindingStatus(su.Status) {
					continue
				}
				f.Status = su.Status
				f.UpdatedAt = time.Now()
				if err := o.findingRepo.Upsert(ctx, f); err != nil {
					applog.WithError(err).Warn("Reflection: apply finding status update failed")
				}
			}
		}
	}

	// Durable learnings → agent-observation notes (fixes the dead AddNote channel).
	if len(ref.Learnings) > 0 && o.contextRepo != nil {
		if pctx, err := o.contextRepo.GetByProjectID(ctx, o.projectID); err == nil && pctx != nil {
			added := false
			for _, l := range ref.Learnings {
				note := strings.TrimSpace(l.Note)
				if note == "" {
					continue
				}
				rel := l.Relevance
				if rel <= 0 || rel > 1 {
					rel = 0.6
				}
				pctx.AddNote(strings.TrimSpace(l.Category), note, rel)
				added = true
			}
			if added {
				if err := o.contextRepo.Save(ctx, pctx); err != nil {
					applog.WithError(err).Warn("Reflection: save learnings notes failed")
				}
			}
		}
	}

	// The rest (next-tasks + domain-pack deltas) is the self-directing part the
	// evolution mode governs. Off records coverage/findings/learnings only.
	if pol.EvolutionMode == agentplugin.EvolutionModeOff {
		return
	}

	o.applyNextTasks(ctx, ref)
	o.applyPackProposals(ctx, result, ref)
}

// applyNextTasks inserts fresh next-tasks/hypotheses, deduped against the open
// queue by normalized text key.
func (o *Orchestrator) applyNextTasks(ctx context.Context, ref *parsedReflection) {
	if len(ref.NextTasks) == 0 || o.taskRepo == nil {
		return
	}
	existing, err := o.taskRepo.List(ctx, o.projectID,
		commonmodels.LedgerTaskStatusOpen, commonmodels.LedgerTaskStatusInProgress)
	if err != nil {
		applog.WithError(err).Warn("Reflection: list tasks failed; proceeding without dedup")
	}
	seen := make(map[string]bool, len(existing))
	for _, t := range existing {
		seen[t.NormalizedKey] = true
	}
	now := time.Now()
	var fresh []commonmodels.LedgerTask
	for _, nt := range ref.NextTasks {
		text := strings.TrimSpace(nt.Text)
		if text == "" {
			continue
		}
		key := commonmodels.NormalizedQuestionKey(text)
		if seen[key] {
			continue
		}
		seen[key] = true
		kind := nt.Kind
		if kind != commonmodels.LedgerTaskKindHypothesis {
			kind = commonmodels.LedgerTaskKindNextTask
		}
		task := commonmodels.LedgerTask{
			ID:            uuid.New().String(),
			ProjectID:     o.projectID,
			Text:          text,
			Kind:          kind,
			Status:        commonmodels.LedgerTaskStatusOpen,
			CreatedRunID:  o.runID,
			NormalizedKey: key,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if commonmodels.ValidQuestionTargetType(nt.TargetType) && strings.TrimSpace(nt.TargetID) != "" {
			task.LinkedTarget = commonmodels.QuestionTarget{Type: nt.TargetType, ID: strings.TrimSpace(nt.TargetID)}
		}
		fresh = append(fresh, task)
	}
	if len(fresh) > 0 {
		if err := o.taskRepo.Insert(ctx, fresh); err != nil {
			applog.WithError(err).Warn("Reflection: insert next-tasks failed")
		}
	}
}

// applyPackProposals writes proposed domain-pack deltas (status "proposed") for
// the enterprise evolution workflow to govern. Deduped against still-open
// proposals for the same area+action.
func (o *Orchestrator) applyPackProposals(ctx context.Context, result *models.DiscoveryResult, ref *parsedReflection) {
	if len(ref.PackDeltas) == 0 || o.proposalRepo == nil {
		return
	}
	open, err := o.proposalRepo.ListForProject(ctx, o.projectID,
		commonmodels.PackProposalStatusProposed, commonmodels.PackProposalStatusApproved)
	if err != nil {
		applog.WithError(err).Warn("Reflection: list pack proposals failed; proceeding without dedup")
	}
	seen := make(map[string]bool, len(open))
	for _, p := range open {
		seen[p.Action+"|"+p.AreaID] = true
	}
	now := time.Now()
	var fresh []commonmodels.PackProposal
	for _, d := range ref.PackDeltas {
		if !commonmodels.ValidPackDeltaAction(d.Action) {
			continue
		}
		areaID := strings.TrimSpace(d.AreaID)
		if areaID == "" {
			continue
		}
		if strings.TrimSpace(d.Rationale) == "" {
			continue // grounding: a delta with no reason is dropped
		}
		dedupKey := d.Action + "|" + areaID
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		fresh = append(fresh, commonmodels.PackProposal{
			ID:                uuid.New().String(),
			ProjectID:         o.projectID,
			Action:            d.Action,
			AreaID:            areaID,
			AreaName:          strings.TrimSpace(d.AreaName),
			Prompt:            d.Prompt,
			Keywords:          d.Keywords,
			Rationale:         strings.TrimSpace(d.Rationale),
			Status:            commonmodels.PackProposalStatusProposed,
			CreatedRunID:      o.runID,
			SourceDiscoveryID: result.ID,
			CreatedAt:         now,
			UpdatedAt:         now,
		})
	}
	if len(fresh) > 0 {
		if err := o.proposalRepo.Insert(ctx, fresh); err != nil {
			applog.WithError(err).Warn("Reflection: insert pack proposals failed")
		}
	}
}

// getEnvAsFloat reads a float env var with a default (go-common has no float
// helper). Rule 2 — all thresholds parametric.
func getEnvAsFloat(key string, def float64) float64 {
	v := goconfig.GetEnv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}
