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
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

//go:embed prompts/reflection.md
var reflectionPromptTemplate string

// Env knobs for the reflection / consolidation phase (Rule 2 — all parametric).
const (
	// discoveryReflectionEnabledEnv is the deployment-availability gate (Layer
	// A). Default ON: the phase is already fully protected by the gates that
	// actually matter — the ledger/finding repos are only wired by the
	// enterprise agent plugins (nil elsewhere, so the phase returns early) and
	// the sources license entitlement is checked below. Layer A therefore added
	// no real protection while silently costing every deployment that forgot to
	// set it an empty Discovery Ledger, with no error to point at. Set it to
	// "false" to opt out explicitly. Independent of the per-project Settings
	// toggle (Layer B, default on).
	discoveryReflectionEnabledEnv    = "DISCOVERY_REFLECTION_ENABLED"
	discoveryReflectionTimeoutEnv    = "DISCOVERY_REFLECTION_TIMEOUT"
	discoveryReflectionMaxOutputEnv  = "DISCOVERY_REFLECTION_MAX_OUTPUT"
	discoveryReflectionParseRetryEnv = "DISCOVERY_REFLECTION_PARSE_MAX_RETRIES"
	discoveryLedgerMaxFindingsEnv    = "DISCOVERY_LEDGER_MAX_FINDINGS"
	discoveryLedgerDedupMinScoreEnv  = "DISCOVERY_LEDGER_DEDUP_MINSCORE"
	discoveryLedgerTrendDeltaEnv     = "DISCOVERY_LEDGER_TREND_DELTA"
)

const (
	// defaultDiscoveryReflectionTimeout bounds the whole reflection phase.
	//
	// Three HOURS, not the three minutes this started as. The phase's cost grows
	// with the ledger it reflects on — the prompt re-judges every prior finding —
	// so the call that returns in seconds on a project's first run takes minutes
	// once the ledger holds a few hundred. On a production project carrying 69
	// findings the reflection call sent a 32k-token prompt and took 2m13s; the
	// next run's took longer than the budget and was cancelled mid-call. Step 1
	// had already written its findings, so the ledger kept growing while
	// everything from step 2 on — coverage, convergence, learnings, next-tasks,
	// provenance — stopped being written after the first run. The budget was
	// tightest exactly on the runs with the most history to reason about.
	//
	// So it is deliberately generous enough never to be the binding constraint:
	// a hung provider is bounded by the per-call LLM_TIMEOUT and the pod by the
	// agent Job's ActiveDeadlineSeconds (AGENT_JOB_TIMEOUT_HOURS). This bounds
	// only how long we are willing to keep trying, and the phase runs after the
	// run-completion event, so a long one delays nothing a user is waiting on.
	defaultDiscoveryReflectionTimeout = 3 * time.Hour
	// reflectionCleanupTimeout bounds taking the in-flight marker back down.
	// Short: it is one small update, and it runs after the phase's own
	// deadline has usually already passed.
	reflectionCleanupTimeout = 10 * time.Second
	// ledgerDedupSearchTimeout bounds ONE semantic-dedup lookup. Short: it is
	// a single vector search, and it is optional — the exact-key match has
	// already been tried.
	ledgerDedupSearchTimeout = 15 * time.Second
	// ledgerDedupSearchBudget is the ceiling on all of them together.
	ledgerDedupSearchBudget = 45 * time.Second
	defaultDiscoveryReflectionMaxOutput  = 3000
	defaultDiscoveryReflectionParseRetry = 1
	defaultDiscoveryLedgerMaxFindings    = 500
	defaultDiscoveryLedgerDedupMinScore  = 0.85
	defaultDiscoveryLedgerTrendDelta     = 0.2
	maxConvergenceHistory                = 50
	maxPriorFindingsInPrompt             = 60
	maxLedgerTasksInPrompt               = 40
	// defaultLearningRelevance is the importance a learning is stored with when
	// the model gave none or gave one outside 0..1. Shared by the
	// project-context note and the ledger's own copy so the two cannot drift.
	defaultLearningRelevance = 0.6
	// maxReasonLength bounds the free-text reasons the model supplies for a
	// status move or a task close, like every other model string on this path.
	maxReasonLength = 400
)

// ledgerStore / findingStore / taskStore / proposalStore are the persistence
// surfaces the reflection phase needs, held as interfaces so unit tests can
// inject fakes without MongoDB. The concrete writers are the *database.Ledger*
// repositories, wired by agentserver.go.
type ledgerStore interface {
	Get(ctx context.Context, projectID string) (*commonmodels.DiscoveryLedger, error)
	Save(ctx context.Context, l *commonmodels.DiscoveryLedger) error
	// BeginReflection / EndReflection bracket the phase, so the one-shot
	// backfill can tell that finding documents are still being rewritten.
	// Run status cannot: a run is `completed` before this phase starts.
	BeginReflection(ctx context.Context, projectID, runID string) error
	EndReflection(ctx context.Context, projectID, runID string) error
}

type findingStore interface {
	// List is the ACTIVE set — archived findings excluded. Both prompt paths
	// read it, which is what keeps prompt size and content unchanged by the
	// retention cap having become a soft archive.
	List(ctx context.Context, projectID string) ([]commonmodels.LedgerFinding, error)
	// ListMatching is for the dedup pass: a finding the cap archived and this
	// run saw again must merge into the existing document (and be
	// un-archived), not be written a second time under the same key. Only the
	// rows that could be that finding are read — the collection grows with
	// the project's lifetime, while the active set stays capped.
	ListMatching(ctx context.Context, projectID string, keys, ids []string) ([]commonmodels.LedgerFinding, error)
	// Upsert writes a finding whole. For a NEW finding only: a merge into one
	// that already exists goes through Merge, which adds rather than replaces
	// so a reflection running beside this one does not lose its work.
	Upsert(ctx context.Context, f *commonmodels.LedgerFinding) error
	Merge(ctx context.Context, projectID, id string, m database.FindingMerge) error
	ApplyStatus(ctx context.Context, projectID, id, status string, c commonmodels.FindingStatusChange) error
	// MarkAnsweredTask appends the reverse of a closed thread's
	// resolved_by_finding_ids, atomically — never by replacing the document.
	MarkAnsweredTask(ctx context.Context, projectID string, findingIDs []string, taskID string) error
	Prune(ctx context.Context, projectID string, max int) error
}

type taskStore interface {
	List(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.LedgerTask, error)
	Insert(ctx context.Context, tasks []commonmodels.LedgerTask) error
	// Close reports whether this call is the one that closed the task.
	Close(ctx context.Context, projectID, id string, u database.TaskClose) (bool, error)
	MarkPursued(ctx context.Context, projectID string, ids []string, runID string) error
}

type learningStore interface {
	Upsert(ctx context.Context, l *commonmodels.LedgerLearning) error
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
	if !goconfig.GetEnvAsBool(discoveryReflectionEnabledEnv, true) {
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

	// From here on this phase rewrites finding documents. Say so for as long
	// as it takes, so the backfill leaves the project alone meanwhile. The
	// clear is deferred rather than placed at the end: every return below is
	// a best-effort bail-out, and a marker left set would block the backfill
	// on a project nothing is writing.
	if err := o.ledgerRepo.BeginReflection(rctx, o.projectID, o.runID); err != nil {
		applog.WithError(err).Warn("Reflection: could not mark the phase in flight")
	}
	defer func() {
		// Its own context, not rctx: the commonest reason to reach this defer
		// is that rctx's deadline expired, and clearing the marker with an
		// expired context fails — leaving an idle project marked as writing.
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), reflectionCleanupTimeout)
		defer ccancel()
		if err := o.ledgerRepo.EndReflection(cctx, o.projectID, o.runID); err != nil {
			applog.WithError(err).Warn("Reflection: could not clear the in-flight marker")
		}
	}()

	// Resolve the per-project evolution policy (best-effort; default off).
	pol, perr := agentplugin.ResolveDiscoveryPolicy(rctx, o.projectID)
	if perr != nil {
		applog.WithError(perr).Warn("Failed to resolve discovery policy; using default (off)")
	}

	// 1. Capture this run's insights as durable ledger findings WITH substance,
	//    deduped/trended against prior findings. Always runs (low-risk).
	cons, err := o.consolidateFindings(rctx, result)
	if err != nil {
		applog.WithError(err).Error("Reflection: finding consolidation failed")
	}
	if cons.Unrecorded > 0 {
		// Loud, and counted. A run that found things and could not record
		// them is not a quiet degradation: the next run builds on the ledger,
		// so what is missing here is missing from every run after it.
		applog.WithFields(applog.Fields{
			"project_id": o.projectID,
			"run_id":     o.runID,
			"unrecorded": cons.Unrecorded,
			"found":      len(result.Insights),
		}).Error("Reflection: findings from this run did not reach the ledger")
	}

	// 2. The bounded LLM call produces the judgment-heavy outputs: coverage
	//    summary, prior-finding status re-judgement, durable learnings,
	//    next-tasks, and domain-pack deltas. Skipped cleanly on any failure.
	// reSeen is what licenses asking the model to re-judge a prior finding:
	// the findings the ledger carried IN that this run surfaced again, which
	// is grounded evidence about them and the only kind this phase has. The
	// finding list generateReflection reads cannot supply it — consolidation
	// has already merged this run's own findings into that list, so a first
	// run that found anything looks like it has a history. A consolidation
	// that failed reports none, which reads as "no evidence": the safe
	// direction, since the alternative is a fabricated verdict on a real
	// finding, and a `resolved` moves it to the front of the prune queue.
	// 2a. Record which threads this run had in front of it. Deliberately
	//     outside the LLM-output path, and BEFORE the call rather than after:
	//     the record is true whether or not the model closed anything, and a
	//     call that exhausts DISCOVERY_REFLECTION_TIMEOUT leaves rctx expired,
	//     so a write placed after it would fail on a deadline every time the
	//     reflection was slow — losing the record exactly on the runs that
	//     worked hardest.
	o.markThreadsPursued(rctx)

	ref, err := o.generateReflection(rctx, result, pol, cons)
	if err != nil {
		applog.WithError(err).Warn("Reflection: consolidation LLM call failed; ledger findings still captured")
		ref = nil
	}

	// 3. Apply the LLM outputs, each independently guarded.
	o.applyReflection(rctx, result, pol, ref, cons)

	// 4. Update coverage + convergence on the ledger and prune findings.
	o.updateLedgerMeta(rctx, result, ref, cons)
	if err := o.findingRepo.Prune(rctx, o.projectID,
		goconfig.GetEnvAsInt(discoveryLedgerMaxFindingsEnv, defaultDiscoveryLedgerMaxFindings)); err != nil {
		applog.WithError(err).Warn("Reflection: ledger prune failed")
	}

	applog.WithFields(applog.Fields{
		"project_id":     o.projectID,
		"run_id":         o.runID,
		"new_findings":   cons.NewCount,
		"total_findings": cons.TotalCount,
		"evolution_mode": string(pol.EvolutionMode),
	}).Info("Reflection phase complete")
}

// consolidationResult is what the consolidation pass knows and the rest of the
// reflection phase needs. It is a struct rather than a growing return list
// because three of its four members only make sense together: they are all
// statements about which ledger finding this run's insights landed on.
type consolidationResult struct {
	// NewCount is the number of genuinely-new findings; TotalCount the
	// project's active total afterward. They drive the convergence point.
	NewCount   int
	TotalCount int

	// ReSeen are the findings the ledger CARRIED IN that this run surfaced
	// again — the reflection phase's only grounded evidence about the
	// project's history. A carried finding this run saw again can honestly be
	// re-judged; one it did not see cannot, because absence is not proof.
	//
	// It is collected during consolidation because only that pass can tell the
	// two apart afterwards: a merge stamps SourceDiscoveryID with this run
	// either way, so a carried finding re-seen now and one created a moment
	// ago by an earlier candidate in the same loop are indistinguishable in
	// the stored document.
	ReSeen map[string]struct{}

	// Unrecorded is how many of this run's findings did not reach the ledger.
	//
	// It exists because the alternative is a lie. Consolidation is
	// best-effort — it must never fail a run that has already succeeded — but
	// the convergence point it feeds says "this run found N", and a pass that
	// wrote nothing would report N=0: not silence, a false record of a run
	// that actually found things. Carried through to the ledger so the page
	// can say the run's findings are missing rather than that there were
	// none.
	Unrecorded int

	// FindingIDByInsightID maps each of this run's insight ids to the ledger
	// finding it landed on — a new one, or a prior one it merged into.
	//
	// This is what lets the model point at a finding from THIS run. The
	// reflection prompt lists prior findings by their ledger id and this run's
	// findings by their insight id (a run's findings have no ledger id the
	// model could have seen), so an answer naming either is resolved through
	// here to one real ledger finding, and anything else is dropped.
	FindingIDByInsightID map[string]string
}

// resolveFindingIDs maps model-supplied finding ids onto real ledger finding
// ids, dropping anything the prompt did not carry. An id is accepted when it is
// a ledger finding id in known (the prior-findings page) or one of this run's
// insight ids (resolved to the finding it landed on). The result is deduped and
// order-stable.
//
// Dropping silently is the established behaviour for an id a reflection
// response invented — applyTaskStatusUpdates already does it for task ids — and
// it is the right one: a fabricated provenance edge is worse than a missing one.
func (c consolidationResult) resolveFindingIDs(raw []string, known map[string]struct{}) []string {
	var out []string
	seen := make(map[string]struct{}, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		resolved := ""
		if _, ok := known[id]; ok {
			resolved = id
		} else if mapped, ok := c.FindingIDByInsightID[id]; ok {
			resolved = mapped
		}
		if resolved == "" {
			continue
		}
		if _, dup := seen[resolved]; dup {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	return out
}

// appendFindingSighting records one run's observation of a finding. Deduped on
// (run, insight) so re-running the phase cannot double-count, while a run whose
// two insights both landed on this finding legitimately records both.
// sightingIf passes the sighting on only when it was actually added, so a
// replay writes nothing rather than a duplicate.
func sightingIf(added bool, s commonmodels.FindingSighting) *commonmodels.FindingSighting {
	if !added {
		return nil
	}
	return &s
}

// appendFindingSighting reports whether the sighting was added, so a caller
// writing the trail as a delta knows whether to write it. A replay of the same
// discovery must not push a second copy: duplicates would spend the trail's
// fifty entries on repeats and evict real history to do it.
func appendFindingSighting(f *commonmodels.LedgerFinding, s commonmodels.FindingSighting) bool {
	for i := range f.Sightings {
		if f.Sightings[i].RunID == s.RunID && f.Sightings[i].InsightID == s.InsightID {
			return false
		}
	}
	f.Sightings = commonmodels.TrimFindingTrail(append(f.Sightings, s))
	return true
}

// appendFindingStatusChange records why a status moved. A repeat of the same
// verdict from the same run and the same source is skipped, so a re-judgement
// that changes nothing does not grow the document.
// appendFindingStatusChange reports whether the entry was added, so a caller
// writing the trail as a delta knows what to write.
func appendFindingStatusChange(f *commonmodels.LedgerFinding, c commonmodels.FindingStatusChange) bool {
	// Only the LAST entry suppresses a repeat. One run can legitimately move a
	// status away and back — consolidation marks a trend `changed` and the
	// model then confirms the finding — and matching any earlier entry would
	// swallow that final verdict, leaving the trail ending on a status the
	// finding no longer has and losing the model's reason with it.
	if n := len(f.StatusHistory); n > 0 {
		last := f.StatusHistory[n-1]
		if last.Status == c.Status && last.RunID == c.RunID && last.Source == c.Source {
			return false
		}
	}
	f.StatusHistory = commonmodels.TrimFindingTrail(append(f.StatusHistory, c))
	return true
}

// appendUnique appends the ids not already present, order-stable.
func appendUnique(dst, add []string) []string {
	have := make(map[string]struct{}, len(dst))
	for _, id := range dst {
		have[id] = struct{}{}
	}
	for _, id := range add {
		if id == "" {
			continue
		}
		if _, dup := have[id]; dup {
			continue
		}
		have[id] = struct{}{}
		dst = append(dst, id)
	}
	return dst
}

// consolidateFindings turns this run's insights into durable ledger findings,
// deduping (exact + semantic) and trend-marking against prior findings, and
// indexes new/updated findings into Qdrant when the embedder is wired.
func (o *Orchestrator) consolidateFindings(ctx context.Context, result *models.DiscoveryResult) (consolidationResult, error) {
	candidates := buildFindingCandidates(result)
	out := consolidationResult{
		ReSeen:               make(map[string]struct{}),
		FindingIDByInsightID: make(map[string]string, len(candidates)),
	}
	// The ACTIVE set, which the retention cap bounds. The archived rows this
	// run might match are fetched separately, below, once there is something
	// to match them against — the archive grows with the project's lifetime,
	// so reading all of it here would make this path cost more every month.
	prior, lerr := o.findingRepo.List(ctx, o.projectID)
	if lerr != nil {
		out.Unrecorded = len(candidates)
		return out, fmt.Errorf("list prior findings: %w", lerr)
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

	// A finding the retention cap archived and this run has seen again must
	// merge into the existing document — which un-archives it, with its
	// sightings and edges intact — rather than being written a second time
	// under the same normalized_key. Only the rows that could be one of this
	// run's candidates are read, by key; the archive grows with the project's
	// lifetime while the active set stays capped.
	//
	// By KEY only, and before any network call. Everything this run can match
	// exactly is then resolvable from memory, which is what lets the writes
	// below happen before the optional semantic fallback is ever asked
	// anything.
	keys := make([]string, 0, len(candidates))
	for i := range candidates {
		keys = append(keys, candidates[i].NormalizedKey)
	}
	matching, aerr := o.findingRepo.ListMatching(ctx, o.projectID, keys, nil)
	if aerr != nil {
		out.Unrecorded = len(candidates)
		return out, fmt.Errorf("list findings this run may have seen again: %w", aerr)
	}
	// Reconciled by id: this read is not filtered to archived rows, so it can
	// return one the active read above already had.
	seen := make(map[string]struct{}, len(prior))
	for i := range prior {
		seen[prior[i].ID] = struct{}{}
	}
	for i := range matching {
		if _, dup := seen[matching[i].ID]; dup {
			continue
		}
		prior = append(prior, matching[i])
	}

	// insertedThisRun holds the ids this loop creates. They join priorByKey /
	// priorByID so a later candidate merges into them instead of duplicating,
	// which means a "merge" below is not by itself evidence about the past.
	insertedThisRun := make(map[string]struct{})

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

	// Candidates this run can already place by key come FIRST, and they are
	// written before the semantic fallback is asked anything at all.
	//
	// The order is the whole point. Recording this run's findings is work the
	// ledger owes; the semantic lookup is an optional nicety for candidates
	// no key matched. Running every lookup before the first write made the
	// optional step able to destroy the mandatory one: a slow vector store
	// burned the phase's deadline and NOTHING was written, including the
	// findings that matched exactly and never needed a lookup. Obligation
	// first, enrichment second, so the worst a stalled lookup can now cost is
	// the candidates that actually depend on it.
	order := make([]int, 0, len(candidates))
	for i := range candidates {
		if _, exact := priorByKey[candidates[i].NormalizedKey]; exact {
			order = append(order, i)
		}
	}
	for i := range candidates {
		if _, exact := priorByKey[candidates[i].NormalizedKey]; !exact {
			order = append(order, i)
		}
	}

	// One budget for every lookup together, as a second line of defence: with
	// the writes already done it can no longer cost the run its findings, but
	// it still must not spend a deadline the rest of the phase needs.
	sctx, cancelSearch := context.WithTimeout(ctx, dedupSearchBudget(ctx))
	defer cancelSearch()
	searchBudgetSpent := false

	// Findings pulled in one at a time by a semantic match, each its own
	// allocation rather than appended to `prior` — growing that slice would
	// move it and stale every pointer already in the two maps. Kept here so
	// the active count at the end can see them: a reworded insight that
	// revived an archived finding has put one back into the active set, and
	// leaving it out would understate the convergence denominator.
	var revived []*commonmodels.LedgerFinding

	for _, i := range order {
		cand := candidates[i]
		var vec []float64
		if vectors != nil {
			vec = vectors[i]
		}

		// The insight this candidate was built from — the id that reaches the
		// narrative, the SQL, the quality caveats and the validation verdict.
		var insightID string
		if len(cand.SourceInsightIDs) > 0 {
			insightID = cand.SourceInsightIDs[0]
		}

		match := priorByKey[cand.NormalizedKey]
		if match == nil && vec != nil && !searchBudgetSpent {
			// The fallback, for this candidate only, now that everything
			// this run could place by key is already stored.
			if sctx.Err() != nil {
				searchBudgetSpent = true
				applog.Warn("Reflection: semantic dedup budget spent; the rest fall back to exact-key matching")
			} else if id := o.searchLedgerNeighbour(sctx, vec, dedupMinScore); id != "" {
				match = priorByID[id]
				if match == nil {
					// A row the key read did not cover — an archived finding
					// this candidate was reworded from. One point lookup.
					if found, ferr := o.findingRepo.ListMatching(ctx, o.projectID, nil, []string{id}); ferr != nil {
						applog.WithError(ferr).Warn("Reflection: load the semantically matched finding failed")
					} else if len(found) > 0 {
						// Its own allocation, never appended to `prior`:
						// growing that slice would move it and every pointer
						// already handed to the two maps would be stale.
						pulled := found[0]
						match = &pulled
						priorByID[match.ID] = match
						priorByKey[match.NormalizedKey] = match
						revived = append(revived, match)
					}
				}
			}
		}

		if match != nil {
			if _, ownWork := insertedThisRun[match.ID]; !ownWork {
				out.ReSeen[match.ID] = struct{}{}
			}
			// Merge / trend-mark the existing finding in place. The magnitude
			// as it stood BEFORE the merge is captured first, because the
			// reason a trend is reported with has to name the values that
			// moved and the merge below overwrites them.
			changed := findingMagnitudeChanged(match, &cand, trendDelta)
			priorSeverity, priorAffected := match.Severity, match.AffectedCount
			// The finding as it is STORED, kept until the write lands. Two of
			// this run's insights can merge into the same finding, and if the
			// first write fails the second must be compared against what is
			// in Mongo — not against the observation that did not persist, or
			// a low-to-high move is measured from the high it never recorded
			// and the trend goes unwritten.
			stored := *match
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
			match.SourceInsightIDs = appendUnique(match.SourceInsightIDs, cand.SourceInsightIDs)
			// What this run is ADDING, kept apart from the merged copy. The
			// merged copy is what gets indexed; this is what gets written,
			// because a write that says "add this" survives another run
			// writing to the same finding and a write that says "the document
			// is now this" does not.
			sighting := commonmodels.FindingSighting{
				RunID:         o.runID,
				DiscoveryID:   result.ID,
				Date:          now,
				KeyMetric:     cand.KeyMetric,
				AffectedCount: cand.AffectedCount,
				Severity:      cand.Severity,
				InsightID:     insightID,
			}
			var statusAdded []commonmodels.FindingStatusChange
			sightingAdded := appendFindingSighting(match, sighting)
			// A run that sees an archived finding again brings it back into the
			// active set: it is current again, and its edges and sightings are
			// still attached. The write un-archives unconditionally; this
			// branch is about the STATUS, which only has to move when the
			// snapshot says the finding had been archived under a terminal
			// one.
			if match.Archived {
				match.Archived = false
				match.ArchivedAt = time.Time{}
				// Un-archiving a resolved or refuted finding is not enough on
				// its own: both statuses are terminal, so the read context
				// would still leave it out of the next prompt and the next
				// prune would archive it again — it would be invisible however
				// often it reappeared. While pruning deleted, the same run
				// simply recreated it as confirmed, and that is the state it
				// is put back into. A reflection verdict later in this run
				// still overrides it.
				if match.Status == commonmodels.LedgerFindingStatusResolved ||
					match.Status == commonmodels.LedgerFindingStatusRefuted {
					match.Status = commonmodels.LedgerFindingStatusConfirmed
					revived := commonmodels.FindingStatusChange{
						Status:      commonmodels.LedgerFindingStatusConfirmed,
						Reason:      "seen again after being archived",
						RunID:       o.runID,
						DiscoveryID: result.ID,
						At:          now,
						Source:      commonmodels.FindingStatusSourceTrend,
					}
					if appendFindingStatusChange(match, revived) {
						statusAdded = append(statusAdded, revived)
					}
				}
			}
			if changed {
				match.Status = commonmodels.LedgerFindingStatusChanged
				// The trend is detected here, not judged by the model, so it
				// carries its own reason — otherwise the page shows a status
				// that moved with nothing to explain it.
				trend := commonmodels.FindingStatusChange{
					Status:      commonmodels.LedgerFindingStatusChanged,
					Reason:      trendReason(priorSeverity, priorAffected, cand.Severity, cand.AffectedCount),
					RunID:       o.runID,
					DiscoveryID: result.ID,
					At:          now,
					Source:      commonmodels.FindingStatusSourceTrend,
				}
				if appendFindingStatusChange(match, trend) {
					statusAdded = append(statusAdded, trend)
				}
			} else if match.Status == "" {
				// A legacy row with no status at all. Setting one is a move
				// like any other, and it is recorded for the same reason:
				// status is only ever written together with the entry that
				// explains it, so a concurrent write can never leave a
				// finding showing a status its own history contradicts.
				match.Status = commonmodels.LedgerFindingStatusConfirmed
				first := commonmodels.FindingStatusChange{
					Status:      commonmodels.LedgerFindingStatusConfirmed,
					Reason:      "first recorded status",
					RunID:       o.runID,
					DiscoveryID: result.ID,
					At:          now,
					Source:      commonmodels.FindingStatusSourceTrend,
				}
				if appendFindingStatusChange(match, first) {
					statusAdded = append(statusAdded, first)
				}
			}
			if err := o.findingRepo.Merge(ctx, o.projectID, match.ID, database.FindingMerge{
				Description:       match.Description,
				KeyMetric:         match.KeyMetric,
				Evidence:          match.Evidence,
				Severity:          match.Severity,
				AffectedCount:     match.AffectedCount,
				LastSeen:          match.LastSeen,
				SourceDiscoveryID: match.SourceDiscoveryID,
				Liked:             match.Liked,
				InsightIDs:        cand.SourceInsightIDs,
				Sighting:          sightingIf(sightingAdded, sighting),
				StatusChanges:     statusAdded,
			}); err != nil {
				applog.WithError(err).Warn("Reflection: merge of a re-seen finding failed")
				*match = stored
				out.Unrecorded++
				continue
			}
			if insightID != "" {
				out.FindingIDByInsightID[insightID] = match.ID
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
		appendFindingSighting(&cand, commonmodels.FindingSighting{
			RunID:         o.runID,
			DiscoveryID:   result.ID,
			Date:          now,
			KeyMetric:     cand.KeyMetric,
			AffectedCount: cand.AffectedCount,
			Severity:      cand.Severity,
			InsightID:     insightID,
		})
		appendFindingStatusChange(&cand, commonmodels.FindingStatusChange{
			Status:      commonmodels.LedgerFindingStatusConfirmed,
			Reason:      "first seen in this run",
			RunID:       o.runID,
			DiscoveryID: result.ID,
			At:          now,
			Source:      commonmodels.FindingStatusSourceReflection,
		})
		if err := o.findingRepo.Upsert(ctx, &cand); err != nil {
			applog.WithError(err).Warn("Reflection: insert of a new finding failed")
			out.Unrecorded++
			continue
		}
		out.NewCount++
		if insightID != "" {
			out.FindingIDByInsightID[insightID] = cand.ID
		}
		// Register the just-inserted finding so a LATER candidate in the same run
		// with the same key merges into it (bumping seen_count) instead of writing
		// a duplicate doc and inflating newCount / the convergence signal. cand is
		// a per-iteration variable, so &cand is a distinct, heap-escaped pointer.
		inserted := cand
		priorByKey[inserted.NormalizedKey] = &inserted
		priorByID[inserted.ID] = &inserted
		insertedThisRun[inserted.ID] = struct{}{}
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

	// The total is the ACTIVE set after this run: the prior list read above
	// includes archived rows (the dedup pass needs them), and counting those
	// would inflate the denominator of the convergence ratio with findings the
	// ledger no longer carries.
	active := 0
	for i := range prior {
		if !prior[i].Archived {
			active++
		}
	}
	// And the ones a semantic match pulled in. Their flag tracks what landed:
	// a merge that failed was rolled back, so one still marked archived is
	// one that really is.
	for _, f := range revived {
		if !f.Archived {
			active++
		}
	}
	out.TotalCount = active + out.NewCount
	return out, nil
}

// trendReason states what moved, so a `changed` status the server detected
// arrives with its own evidence rather than as a bare label. Severity is
// reported when it moved; otherwise the affected-count delta is — the same two
// signals findingMagnitudeChanged decides on, in the same order.
func trendReason(priorSeverity string, priorAffected int, curSeverity string, curAffected int) string {
	if priorSeverity != curSeverity {
		return fmt.Sprintf("severity moved from %s to %s", priorSeverity, curSeverity)
	}
	return fmt.Sprintf("affected count moved from %d to %d", priorAffected, curAffected)
}

// dedupSearchBudget is how long ALL of this run's semantic lookups may take
// together: a share of what is left of the phase, never more than the ceiling,
// so the writes that follow always have time to land.
func dedupSearchBudget(ctx context.Context) time.Duration {
	budget := ledgerDedupSearchBudget
	if deadline, ok := ctx.Deadline(); ok {
		if share := time.Until(deadline) / 3; share < budget {
			budget = share
		}
	}
	if budget < 0 {
		return 0
	}
	return budget
}

// searchLedgerNeighbour returns the id of the nearest prior ledger finding above
// minScore, or "" when there is none. Best-effort — a search error yields "".
//
// Bounded on its own, not on the phase's deadline. This is the FALLBACK for a
// candidate no key matched, and a stalled vector store must cost that one
// lookup rather than the whole run's consolidation: on the phase's context, a
// stall here burns the deadline and the write that follows never happens, so
// an optional lookup would take every finding the run exactly matched with it.
func (o *Orchestrator) searchLedgerNeighbour(ctx context.Context, vec []float64, minScore float64) string {
	sctx, cancel := context.WithTimeout(ctx, ledgerDedupSearchTimeout)
	defer cancel()
	hits, err := o.vectorStore.Search(sctx, vec, vectorstore.SearchOpts{
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
		cand := commonmodels.LedgerFinding{
			ProjectID:     result.ProjectID,
			Area:          ins.AnalysisArea,
			Name:          name,
			Description:   truncate(ins.Description, 800),
			KeyMetric:     buildKeyMetric(ins),
			Evidence:      truncate(strings.Join(ins.Indicators, "; "), 400),
			Severity:      ins.Severity,
			AffectedCount: ins.AffectedCount,
			NormalizedKey: commonmodels.NormalizedFindingKey(ins.AnalysisArea, name),
		}
		// The insight's own id, which is also its id in the `insights`
		// collection and its Qdrant point id — so this one edge reaches the
		// narrative, the SQL, the rows, the quality caveats and the validation
		// verdict with no lookup-by-index anywhere. An empty id is not stored:
		// an empty string is not a provenance edge.
		if id := strings.TrimSpace(ins.ID); id != "" {
			cand.SourceInsightIDs = []string{id}
		}
		out = append(out, cand)
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
func (o *Orchestrator) updateLedgerMeta(ctx context.Context, result *models.DiscoveryResult, ref *parsedReflection, cons consolidationResult) {
	newCount, totalCount := cons.NewCount, cons.TotalCount
	ledger, err := o.ledgerRepo.Get(ctx, o.projectID)
	if err != nil {
		applog.WithError(err).Warn("Reflection: load ledger failed; skipping coverage/convergence update")
		return
	}

	// The two catalogs this run could actually have queried. They are the
	// authority the model's self-reported coverage is checked against, and
	// they are kept apart: both namespaces are bare strings, so a cube metric
	// answered into covered_tables is indistinguishable from a table by
	// inspection and only its catalog can tell them apart.
	catalogItems := o.runCatalogItems()
	tableIdx := nameIndex(schemaNames(result.Schemas))
	itemIdx := nameIndex(catalogItems)

	// Coverage: union the LLM-reported covered tables and cube items into the
	// explored sets, and record each catalog's size.
	if ref != nil {
		ledger.Coverage.ExploredTables = mergeExplored(ledger.Coverage.ExploredTables, ref.CoveredTables, tableIdx)
		ledger.Coverage.ExploredCatalogItems = mergeExplored(ledger.Coverage.ExploredCatalogItems, ref.CoveredCatalogItems, itemIdx)
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
	// Written whether or not the LLM call produced anything. It is what the
	// next run reads to know this project HAS a cube, and a run that reflected
	// on nothing still must not leave the ledger claiming an exhausted
	// frontier.
	ledger.Coverage.TotalCatalogItems = len(catalogItems)

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
		// Recorded on the run's own point, because this is the record every
		// reader trusts. Without it a consolidation that wrote nothing
		// reports "found 0" — indistinguishable from a run that genuinely
		// found nothing, and the page would state it as fact.
		UnrecordedFindings: cons.Unrecorded,
	})
	if len(ledger.Convergence) > maxConvergenceHistory {
		ledger.Convergence = ledger.Convergence[len(ledger.Convergence)-maxConvergenceHistory:]
	}

	if err := o.ledgerRepo.Save(ctx, ledger); err != nil {
		applog.WithError(err).Warn("Reflection: save ledger failed")
	}
}

// schemaNames returns the table names in a run's schema map.
func schemaNames(schemas map[string]models.TableSchema) []string {
	out := make([]string, 0, len(schemas))
	for name := range schemas {
		out = append(out, name)
	}
	return out
}

// nameIndex maps each catalog name, lower-cased, to its canonical spelling.
//
// Lower-cased because a model is asked to copy names verbatim and mostly does,
// but an upper-case catalog (Oracle, HANA) is the case where it most often does
// not — and a name that differs only in case is the SAME thing, so matching it
// exactly would drop real coverage while storing the model's spelling alongside
// the canonical one would count that thing twice. Resolving to the canonical
// spelling settles both.
//
// Input is sorted first so a catalog holding two names that differ only in case
// resolves deterministically rather than by map order.
func nameIndex(names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	idx := make(map[string]string, len(sorted))
	for _, name := range sorted {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, taken := idx[key]; taken {
			continue // first by sort order wins
		}
		idx[key] = name
	}
	return idx
}

// mergeExplored unions this run's self-reported coverage into the set carried
// from earlier runs, keeping only names the run's catalog actually contains.
//
// Validating is what makes the counter mean anything. Without it a model
// answering with an invented name, a stale one, or — once there are two
// catalogs — a cube metric in covered_tables lands in the explored set
// verbatim, pushing the explored count past the catalog size and leaving a
// frontier that is wrong in both directions: clamped to zero when it is not
// empty, and inflated by names that were never there.
//
// Only the INCOMING names are checked. What earlier runs stored is left alone:
// coverage is cumulative, and a project whose discovery scope was narrowed, or
// whose warehouse dropped a table, would otherwise lose the record of work it
// really did on the run after. An empty index is the honest answer for a
// catalog this run had none of, and drops everything claimed against it.
func mergeExplored(carried, reported []string, catalog map[string]string) []string {
	explored := make(map[string]struct{}, len(carried)+len(reported))
	for _, name := range carried {
		explored[name] = struct{}{}
	}
	for _, name := range reported {
		canonical, known := catalog[strings.ToLower(strings.TrimSpace(name))]
		if !known {
			continue
		}
		explored[canonical] = struct{}{}
	}
	merged := make([]string, 0, len(explored))
	for name := range explored {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged
}

// applyReflection persists the LLM outputs: prior-finding status updates,
// durable learnings (fixing the dead AddNote channel), next-tasks, and — when
// the evolution mode allows — proposed domain-pack deltas.
func (o *Orchestrator) applyReflection(ctx context.Context, result *models.DiscoveryResult, pol agentplugin.DiscoveryPolicy, ref *parsedReflection, cons consolidationResult) {
	if ref == nil {
		return
	}

	// Prior-finding status re-judgement (confirmed/monitoring/resolved/refuted).
	// The consolidation pass already set "changed" for trends it detected; here
	// we honor the model's grounded verdicts on findings it reasoned about.
	if len(ref.StatusUpdates) > 0 {
		// By the ids the prompt SHOWED, not the active set. Another
		// reflection can archive one of them while the model is deciding,
		// and re-reading the active set would drop a verdict on a finding
		// that still exists and that this run put in front of the model.
		prior, err := o.findingRepo.ListMatching(ctx, o.projectID, nil, o.promptFindingIDs)
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
				now := time.Now()
				f.Status = su.Status
				f.UpdatedAt = now
				// The model already emits a reason per status change and it
				// used to be dropped on the floor, which left the page showing
				// a status with no account of why it moved or which run moved
				// it. Recording it costs nothing — it is already in hand.
				change := commonmodels.FindingStatusChange{
					Status:      su.Status,
					Reason:      truncate(strings.TrimSpace(su.Reason), maxReasonLength),
					RunID:       o.runID,
					DiscoveryID: result.ID,
					At:          now,
					Source:      commonmodels.FindingStatusSourceReflection,
				}
				if !appendFindingStatusChange(f, change) {
					continue // already the last word on this finding
				}
				if err := o.findingRepo.ApplyStatus(ctx, o.projectID, f.ID, su.Status, change); err != nil {
					applog.WithError(err).Warn("Reflection: apply finding status update failed")
				}
			}
		}
	}

	// Durable learnings → the ledger's own attributed collection, so the
	// investigation view can show what the run learned and which run learned
	// it. Best-effort and independent of the project-context write below.
	o.applyLearnings(ctx, result, ref)

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
					rel = defaultLearningRelevance
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

	// Close open tasks this run resolved. Runs regardless of evolution mode —
	// keeping the queue honest is hygiene, not self-direction — and returns the
	// closed tasks' keys so applyNextTasks won't recreate one as "new".
	closedTaskKeys := o.applyTaskStatusUpdates(ctx, result, ref, cons)

	// The rest (next-tasks + domain-pack deltas) is the self-directing part the
	// evolution mode governs. Off records coverage/findings/learnings only.
	if pol.EvolutionMode == agentplugin.EvolutionModeOff {
		return
	}

	o.applyNextTasks(ctx, ref, closedTaskKeys, cons)
	o.applyPackProposals(ctx, result, ref)
}

// applyLearnings records the run's durable learnings on the ledger, attributed
// to the run that produced them.
//
// They were already being written into the project-context notes — with no run
// attribution, into a collection the ledger API never reads — so on the
// investigation view they did not exist. That write stays exactly as it is;
// this is a second, owned copy, not a migration. Deduped on the normalized
// key, so a learning a later run repeats bumps the existing row.
func (o *Orchestrator) applyLearnings(ctx context.Context, result *models.DiscoveryResult, ref *parsedReflection) {
	if o.learningRepo == nil || len(ref.Learnings) == 0 {
		return
	}
	for _, l := range ref.Learnings {
		note := strings.TrimSpace(l.Note)
		if note == "" {
			continue
		}
		category := strings.TrimSpace(l.Category)
		rel := l.Relevance
		if rel <= 0 || rel > 1 {
			rel = defaultLearningRelevance
		}
		key := commonmodels.NormalizedLearningKey(category, note)
		if key == "" {
			continue
		}
		if err := o.learningRepo.Upsert(ctx, &commonmodels.LedgerLearning{
			ID:            uuid.New().String(),
			ProjectID:     o.projectID,
			Category:      category,
			Note:          truncate(note, 600),
			Relevance:     rel,
			RunID:         o.runID,
			DiscoveryID:   result.ID,
			NormalizedKey: key,
		}); err != nil {
			applog.WithError(err).Warn("Reflection: upsert ledger learning failed")
		}
	}
}

// markThreadsPursued records that this run had the open threads in front of it,
// whether or not it closed any of them. It is the honest answer to "which
// threads did this run pursue?", and the only edge from a run to a thread it
// worked on but did not finish.
//
// Gated on the evolution mode for the same reason loadLedgerReadContext is: in
// `off` mode the open-task queue is never rendered into the exploration prompt,
// so the run genuinely did not have these threads in front of it and saying
// otherwise would be a fabricated edge.
func (o *Orchestrator) markThreadsPursued(ctx context.Context) {
	if o.taskRepo == nil || o.runID == "" {
		return
	}
	// The threads this run was SHOWN, as they were when it was shown them.
	// Recorded by loadLedgerReadContext at the start of the run, because the
	// queue can move underneath a run: the previous run's reflection can
	// close one of these and open another while this one is exploring, and
	// reading the queue again here would record a thread this run was never
	// told about and miss the one it was.
	ids := o.shownTaskIDs
	if len(ids) == 0 {
		// This run was shown no threads: either no read context was loaded,
		// or evolution was off when it was. Nothing to record either way.
		//
		// Which is also why the evolution mode is NOT re-checked here. It can
		// be switched off, or fail to resolve and default to off, while a run
		// is in flight — and discarding what that run was actually shown
		// would throw away a true record because a setting changed after the
		// fact. The capture at the start of the run already encodes the
		// policy that applied to it.
		return
	}
	if err := o.taskRepo.MarkPursued(ctx, o.projectID, ids, o.runID); err != nil {
		applog.WithError(err).Warn("Reflection: mark tasks pursued failed")
	}
}

// applyTaskStatusUpdates closes the open tasks the reflection resolved this run
// (done / dropped), wiring the previously-dead LedgerTask lifecycle. It is
// grounded: the model only closes a task a finding from this run answered, or a
// proven dead-end. Returns the normalized keys of the tasks it closed so the
// follow-up generation below does not immediately recreate one.
func (o *Orchestrator) applyTaskStatusUpdates(ctx context.Context, result *models.DiscoveryResult, ref *parsedReflection, cons consolidationResult) map[string]bool {
	closed := make(map[string]bool)
	if o.taskRepo == nil || len(ref.TaskStatusUpdates) == 0 {
		return closed
	}
	open, err := o.taskRepo.List(ctx, o.projectID,
		commonmodels.LedgerTaskStatusOpen, commonmodels.LedgerTaskStatusInProgress)
	if err != nil {
		applog.WithError(err).Warn("Reflection: list tasks for status update failed")
		return closed
	}
	byID := make(map[string]*commonmodels.LedgerTask, len(open))
	for i := range open {
		byID[open[i].ID] = &open[i]
	}
	known := o.knownFindingIDs(ctx)
	now := time.Now()
	for _, u := range ref.TaskStatusUpdates {
		status := strings.TrimSpace(u.Status)
		if status != commonmodels.LedgerTaskStatusDone && status != commonmodels.LedgerTaskStatusDropped {
			continue
		}
		id := strings.TrimSpace(u.TaskID)
		t := byID[id]
		if t == nil {
			continue // unknown / already-closed id
		}
		// The findings that answered the thread. Only on a `done`: a `dropped`
		// is a proven dead end and has nothing to point at, which is also why
		// the schema never demands the field alongside one.
		var resolvedBy []string
		if status == commonmodels.LedgerTaskStatusDone {
			resolvedBy = cons.resolveFindingIDs(u.ResolvedByFindingIDs, known)
		}
		won, err := o.taskRepo.Close(ctx, o.projectID, t.ID, database.TaskClose{
			Status:               status,
			RunID:                o.runID,
			DiscoveryID:          result.ID,
			At:                   now,
			Reason:               truncate(strings.TrimSpace(u.Reason), maxReasonLength),
			ResolvedByFindingIDs: resolvedBy,
		})
		if err != nil {
			applog.WithError(err).Warn("Reflection: close task failed")
			continue
		}
		// Closed either way — by this run or by the one that got there first.
		// The key is recorded before the claim is checked, because what it
		// stops is the follow-up pass below recreating a thread that is now
		// closed, and that is just as true when someone else closed it.
		closed[t.NormalizedKey] = true
		if !won {
			// Another reflection closed this thread between the read above and
			// this write. Its account of the close stands; mirroring an edge
			// here would point findings at a close they had no part in.
			continue
		}
		// Out of the lookup, so a second update naming the same thread cannot
		// close it again. A `done` followed by a `dropped` would otherwise
		// leave the thread dropped while keeping the findings that answered
		// it — and the mirrored edge on those findings — which is a record
		// that contradicts itself.
		delete(byID, id)
		// Mirror the edge onto the findings, so the investigation tree is
		// walkable from either end without a second query at read time.
		o.markFindingsAnsweredTask(ctx, resolvedBy, t.ID)
	}
	return closed
}

// knownFindingIDs is the set of ledger finding ids the reflection prompt
// actually showed the model. An id outside it and outside this run's insight
// ids was invented, and is dropped.
//
// What was SHOWN, not what is active now. The active set moves during the LLM
// call — another reflection can archive a finding while the model is
// thinking — and reading it again afterwards would throw out an id this run
// genuinely put on the page, leaving a thread with no parent or a close with
// no answering finding.
func (o *Orchestrator) knownFindingIDs(_ context.Context) map[string]struct{} {
	known := make(map[string]struct{}, len(o.promptFindingIDs))
	for _, id := range o.promptFindingIDs {
		known[id] = struct{}{}
	}
	return known
}

// markFindingsAnsweredTask writes the reverse of a task's
// resolved_by_finding_ids onto each finding. Best-effort: the forward edge is
// already stored, so a failure here costs a convenience, not the record.
//
// The write is an atomic append rather than a read-merge-replace. Only the
// edge is being added, and replacing the whole document to add it would
// discard whatever a reflection running beside this one had just written to
// the same finding.
func (o *Orchestrator) markFindingsAnsweredTask(ctx context.Context, findingIDs []string, taskID string) {
	if len(findingIDs) == 0 || o.findingRepo == nil {
		return
	}
	if err := o.findingRepo.MarkAnsweredTask(ctx, o.projectID, findingIDs, taskID); err != nil {
		applog.WithError(err).Warn("Reflection: mirror answered-task edge onto finding failed")
	}
}

// applyNextTasks inserts fresh next-tasks/hypotheses, deduped against the open
// queue by normalized text key.
func (o *Orchestrator) applyNextTasks(ctx context.Context, ref *parsedReflection, closedKeys map[string]bool, cons consolidationResult) {
	if len(ref.NextTasks) == 0 || o.taskRepo == nil {
		return
	}
	known := o.knownFindingIDs(ctx)
	existing, err := o.taskRepo.List(ctx, o.projectID,
		commonmodels.LedgerTaskStatusOpen, commonmodels.LedgerTaskStatusInProgress)
	if err != nil {
		applog.WithError(err).Warn("Reflection: list tasks failed; proceeding without dedup")
	}
	// Seed dedup with the open queue AND the tasks just closed this run, so a
	// follow-up never recreates a task we resolved moments ago (close→recreate
	// ping-pong). A genuine re-open only comes from contradicting evidence,
	// which flows through the finding-status path, not task churn.
	seen := make(map[string]bool, len(existing)+len(closedKeys))
	for _, t := range existing {
		seen[t.NormalizedKey] = true
	}
	for k := range closedKeys {
		seen[k] = true
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
		// A short, user-facing title; fall back to the detailed text if the
		// model omitted one so the UI always has something to lead with.
		title := strings.TrimSpace(nt.Title)
		if title == "" {
			title = text
		}
		task := commonmodels.LedgerTask{
			ID:        uuid.New().String(),
			ProjectID: o.projectID,
			Title:     title,
			Text:      text,
			Kind:      kind,
			Status:    commonmodels.LedgerTaskStatusOpen,
			// Supersedes links a follow-up to the TASK it continues;
			// OriginFindingIDs attributes the thread to the findings it grew
			// out of, which is what gives the root of a chain a parent at all.
			Supersedes:       strings.TrimSpace(nt.Supersedes),
			OriginFindingIDs: cons.resolveFindingIDs(nt.OriginFindingIDs, known),
			CreatedRunID:     o.runID,
			NormalizedKey:    key,
			CreatedAt:        now,
			UpdatedAt:        now,
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
