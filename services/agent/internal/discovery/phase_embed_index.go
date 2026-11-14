package discovery

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// runPhaseEmbedIndex runs Phase 9: denormalize, embed, deduplicate, and index.
// This phase is NON-FATAL — errors are logged but don't fail the discovery.
func (o *Orchestrator) runPhaseEmbedIndex(ctx context.Context, result *models.DiscoveryResult) {
	o.statusReporter.SetPhase(ctx, models.PhaseEmbedIndex, "Indexing insights for search...", 97)

	// Step 1: Denormalize insights and recommendations into standalone documents
	insights := o.denormalizeInsights(result)
	recs := o.denormalizeRecommendations(result)

	applog.WithFields(applog.Fields{
		"insights":        len(insights),
		"recommendations": len(recs),
	}).Info("Phase 9: Denormalized documents")

	// Step 2: Save to MongoDB insights/recommendations collections
	if err := o.saveStandaloneDocuments(ctx, insights, recs); err != nil {
		applog.WithError(err).Error("Phase 9: Failed to save standalone documents")
		return
	}

	// Step 3-6: Embed and index (only if both embedding provider and Qdrant are configured)
	if o.embeddingProvider == nil || o.vectorStore == nil {
		applog.Info("Phase 9: Embedding or Qdrant not configured — skipping embedding and indexing")
		return
	}

	if err := o.embedAndIndex(ctx, insights, recs, o.dedupExclusions(ctx, result.ID)); err != nil {
		applog.WithError(err).Error("Phase 9: Embedding/indexing failed (non-fatal)")
	}
}

// dedupExclusions is the set of discovery ids the duplicate check must
// ignore: this discovery, and every other one this RUN produced.
//
// The second half is what a resumed run needs. Its superseded attempts each
// left a discovery behind, and those are retired at THIS attempt's tail —
// after the duplicate check has already run, because the retire step
// deliberately writes the new result before removing the old one. Without
// the exclusion a restored finding matches its own predecessor (identical
// text, so a score of 1.0) and is stamped `duplicate_of` a row that is
// deleted a few lines later.
//
// A no-op on a run with one attempt: ListByRun returns that run's single
// discovery, which is the id being excluded anyway. Best-effort — a failed
// read falls back to excluding this discovery alone, which is exactly the
// behaviour this replaced.
func (o *Orchestrator) dedupExclusions(ctx context.Context, discoveryID string) []string {
	exclusions := []string{discoveryID}
	if o.discoveryRepo == nil || o.runID == "" {
		return exclusions
	}
	refs, err := o.discoveryRepo.ListByRun(ctx, o.runID)
	if err != nil {
		applog.WithError(err).WithField("run_id", o.runID).
			Warn("could not list this run's discoveries; a restored finding may be marked a duplicate of a superseded attempt's row")
		return exclusions
	}
	for _, ref := range refs {
		if ref.ID != "" && ref.ID != discoveryID {
			exclusions = append(exclusions, ref.ID)
		}
	}
	return exclusions
}

// denormalizeInsights converts discovery result insights to standalone documents.
// The standalone `_id` is the same UUID the agent assigned to the embedded
// insight during analysis (see generateInsights). Reusing that id means the
// discovery document, the standalone collection, and the Qdrant point all
// share one key — no lookup-by-index fallback is needed anywhere downstream.
func (o *Orchestrator) denormalizeInsights(result *models.DiscoveryResult) []*commonmodels.StandaloneInsight {
	insights := make([]*commonmodels.StandaloneInsight, 0, len(result.Insights))
	now := time.Now()

	for _, ins := range result.Insights {
		// Defensive default: if the embedded id is empty (older data flowing
		// through this path, or a bug upstream), mint a UUID so we don't
		// insert empty _ids. The normal path has ins.ID already populated.
		id := ins.ID
		if id == "" {
			id = uuid.New().String()
		}
		insights = append(insights, &commonmodels.StandaloneInsight{
			ID:            id,
			ProjectID:     result.ProjectID,
			DiscoveryID:   result.ID,
			Datasource:    result.WarehouseID,
			Domain:        result.Domain,
			Category:      result.Category,
			AnalysisArea:  ins.AnalysisArea,
			Name:          ins.Name,
			Description:   ins.Description,
			DescriptionMd: ins.DescriptionMd,
			Severity:      ins.Severity,
			AffectedCount: ins.AffectedCount,
			RiskScore:     ins.RiskScore,
			Confidence:    ins.Confidence,
			Metrics:       ins.Metrics,
			Indicators:    ins.Indicators,
			TargetSegment: ins.TargetSegment,
			SourceSteps:   ins.SourceSteps,
			Validation:    ins.Validation,
			// The marks the mid-run consolidation pass wrote, carried onto
			// the standalone copy. The standalone document is what the API
			// filters on, so a mark that stayed on the embedded copy would
			// hide a finding on the dashboard and return it from the API —
			// the mirror image of the drift markDuplicate exists to prevent.
			DuplicateOf:     ins.DuplicateOf,
			SimilarityScore: ins.SimilarityScore,
			DiscoveredAt:    ins.DiscoveredAt,
			CreatedAt:       now,
		})
	}

	return insights
}

// denormalizeRecommendations converts discovery result recommendations to
// standalone documents. The standalone `_id` matches the embedded
// recommendation's id — same reasoning as denormalizeInsights.
func (o *Orchestrator) denormalizeRecommendations(result *models.DiscoveryResult) []*commonmodels.StandaloneRecommendation {
	recs := make([]*commonmodels.StandaloneRecommendation, 0, len(result.Recommendations))
	now := time.Now()

	for _, rec := range result.Recommendations {
		id := rec.ID
		if id == "" {
			id = uuid.New().String()
		}
		recs = append(recs, &commonmodels.StandaloneRecommendation{
			ID:                     id,
			ProjectID:              result.ProjectID,
			DiscoveryID:            result.ID,
			Domain:                 result.Domain,
			Category:               result.Category,
			RecommendationCategory: rec.Category,
			Title:                  rec.Title,
			Description:            rec.Description,
			DescriptionMd:          rec.DescriptionMd,
			Priority:               rec.Priority,
			TargetSegment:          rec.TargetSegment,
			SegmentSize:            rec.SegmentSize,
			ExpectedImpact: commonmodels.ExpectedImpact{
				Metric:               rec.ExpectedImpact.Metric,
				EstimatedImprovement: rec.ExpectedImpact.EstimatedImprovement,
				Reasoning:            rec.ExpectedImpact.Reasoning,
			},
			Actions:           rec.Actions,
			RelatedInsightIDs: rec.RelatedInsightIDs,
			Confidence:        rec.Confidence,
			Validation:        rec.Validation,
			CreatedAt:         now,
		})
	}

	return recs
}

// saveStandaloneDocuments saves denormalized insights/recommendations to MongoDB.
func (o *Orchestrator) saveStandaloneDocuments(ctx context.Context, insights []*commonmodels.StandaloneInsight, recs []*commonmodels.StandaloneRecommendation) error {
	if err := o.embedIndexStore.InsertInsights(ctx, insights); err != nil {
		return err
	}
	if err := o.embedIndexStore.InsertRecommendations(ctx, recs); err != nil {
		return err
	}
	return nil
}

// embedAndIndex embeds documents and upserts vectors to Qdrant.
func (o *Orchestrator) embedAndIndex(ctx context.Context, insights []*commonmodels.StandaloneInsight, recs []*commonmodels.StandaloneRecommendation, dedupExcludes []string) error {
	// Resolve the dimension robustly — the provider's declared size when it
	// knows it, else a probe embedding — so a model the catalog doesn't know
	// (e.g. the decisionbox-embed-model gateway alias, which reports 0) sizes
	// its collection correctly instead of failing with "dimensions must be
	// positive, got 0".
	dims, err := resolveEmbeddingDimensions(ctx, o.embeddingProvider)
	if err != nil {
		return fmt.Errorf("resolve embedding dimensions: %w", err)
	}
	modelName := o.embeddingProvider.ModelName()

	// Ensure Qdrant collection exists for this dimension
	if err := o.vectorStore.EnsureCollection(ctx, dims); err != nil {
		return fmt.Errorf("ensure qdrant collection: %w", err)
	}

	// Collect all texts to embed in one batch
	var allTexts []string
	for _, ins := range insights {
		text := ins.BuildEmbeddingText()
		ins.EmbeddingText = text
		ins.EmbeddingModel = modelName
		allTexts = append(allTexts, text)
	}
	for _, rec := range recs {
		text := rec.BuildEmbeddingText()
		rec.EmbeddingText = text
		rec.EmbeddingModel = modelName
		allTexts = append(allTexts, text)
	}

	if len(allTexts) == 0 {
		return nil
	}

	// Batch embed what this run has not embedded already.
	//
	// The mid-run consolidation pass embedded every insight it compared, so on
	// a run that consolidated, the insight half of this batch is already in
	// hand and only the recommendations are new. Reusing it is what keeps the
	// embedding spend unchanged by that pass rather than doubled — and the
	// cache is keyed on the embedding TEXT, so an insight whose description was
	// merged during consolidation misses the cache and is embedded here, which
	// is correct: its meaning changed.
	vectors, err := o.embedWithCache(ctx, allTexts, modelName)
	if err != nil {
		return err
	}

	// Update MongoDB with embedding text/model, collect the duplicate-check
	// candidates, and build Qdrant points.
	var points []vectorstore.Point
	candidates := make([]dedupCandidate, 0, len(insights)+len(recs))
	vecIdx := 0

	for _, ins := range insights {
		vec := vectors[vecIdx]
		vecIdx++

		// Update MongoDB with embedding fields
		if err := o.embedIndexStore.UpdateEmbedding(ctx, "insights", ins.ID, ins.EmbeddingText, ins.EmbeddingModel); err != nil {
			applog.WithError(err).WithField("id", ins.ID).Warn("Failed to update insight embedding")
		}

		candidates = append(candidates, dedupCandidate{
			id: ins.ID, docType: "insight", projectID: ins.ProjectID, discoveryID: ins.DiscoveryID, vec: vec,
			text: ins.EmbeddingText, magnitude: magnitudeOf("insight", ins.AffectedCount, ins.Metrics, nil, ins.Indicators),
			markedBy: ins.DuplicateOf,
		})

		points = append(points, vectorstore.Point{
			ID:     ins.ID,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":      ins.ProjectID,
				"discovery_id":    ins.DiscoveryID,
				"embedding_model": modelName,
				"severity":        ins.Severity,
				"analysis_area":   ins.AnalysisArea,
				"confidence":      ins.Confidence,
				"created_at":      ins.CreatedAt.Format(time.RFC3339),
			},
		})
	}

	for _, rec := range recs {
		vec := vectors[vecIdx]
		vecIdx++

		// Update MongoDB with embedding fields
		if err := o.embedIndexStore.UpdateEmbedding(ctx, "recommendations", rec.ID, rec.EmbeddingText, rec.EmbeddingModel); err != nil {
			applog.WithError(err).WithField("id", rec.ID).Warn("Failed to update recommendation embedding")
		}

		candidates = append(candidates, dedupCandidate{
			id: rec.ID, docType: "recommendation", projectID: rec.ProjectID, discoveryID: rec.DiscoveryID, vec: vec,
			// A recommendation's signature is its actions and the size of the
			// segment it targets: the expected impact and the segment's NAME
			// are already in the text its vector was built from, these are
			// not, and they are what a reader would act on and act at.
			text: rec.EmbeddingText, magnitude: magnitudeOf("recommendation", rec.SegmentSize, nil, rec.Actions, nil),
		})

		points = append(points, vectorstore.Point{
			ID:     rec.ID,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "recommendation",
				"project_id":      rec.ProjectID,
				"discovery_id":    rec.DiscoveryID,
				"embedding_model": modelName,
				"confidence":      rec.Confidence,
				"created_at":      rec.CreatedAt.Format(time.RFC3339),
			},
		})
	}

	// Run the duplicate check over the whole run at once, now that every
	// vector exists. One pass, in document order, so a later duplicate is
	// always the one marked and the first occurrence stays canonical.
	o.markDuplicates(ctx, candidates, dedupExcludes)

	// Upsert all vectors to Qdrant
	applog.WithField("points", len(points)).Info("Phase 9: Upserting vectors to Qdrant")
	if err := o.vectorStore.Upsert(ctx, points); err != nil {
		return fmt.Errorf("upsert vectors: %w", err)
	}

	applog.WithFields(applog.Fields{
		"indexed": len(points),
		"model":   modelName,
		"dims":    dims,
	}).Info("Phase 9: Embed & index complete")

	return nil
}

// embedWithCache embeds texts, serving whatever the run already embedded from
// the per-run vector cache and asking the provider only for the rest.
//
// The returned slice is in the same order as texts and the same length, so the
// caller's index walk is unchanged. A count mismatch from the provider is an
// error rather than a partial fill: a vector assigned to the wrong document is
// a duplicate check comparing two findings that were never compared.
func (o *Orchestrator) embedWithCache(ctx context.Context, texts []string, modelName string) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	missing := make([]string, 0, len(texts))
	missingAt := make(map[string][]int, len(texts))
	for i, t := range texts {
		if vec := o.cachedInsightVector(t); vec != nil {
			vectors[i] = vec
			continue
		}
		if _, queued := missingAt[t]; !queued {
			missing = append(missing, t)
		}
		missingAt[t] = append(missingAt[t], i)
	}

	applog.WithFields(applog.Fields{
		"count":  len(texts),
		"cached": len(texts) - len(missingAt),
		"embed":  len(missing),
		"model":  modelName,
	}).Info("Phase 9: Embedding documents")

	if len(missing) == 0 {
		return vectors, nil
	}
	fresh, err := o.embeddingProvider.Embed(ctx, missing)
	if err != nil {
		return nil, fmt.Errorf("embed documents: %w", err)
	}
	if len(fresh) != len(missing) {
		return nil, fmt.Errorf("embedding count mismatch: expected %d, got %d", len(missing), len(fresh))
	}
	for i, t := range missing {
		o.cacheInsightVector(t, fresh[i])
		for _, at := range missingAt[t] {
			vectors[at] = fresh[i]
		}
	}
	return vectors, nil
}

// dedupCandidate is one document this run produced, as the duplicate check sees
// it: an id, what kind of document it is, the vector that was just embedded for
// it, and the two things needed to tell apart documents the vector cannot.
type dedupCandidate struct {
	id        string
	docType   string
	projectID string
	// discoveryID is the discovery document this document is also embedded in.
	// The dashboard renders those embedded copies, so a mark has to reach them
	// too or the suppression lands on the API and on none of the screens.
	discoveryID string
	vec         []float64
	// text is what the vector was built from. Two documents with the same text
	// are identical to the search by construction, so whatever the text leaves
	// out is all there is to distinguish them.
	text string
	// magnitude is a signature of the fields the embedding text omits — an
	// insight's affected count and metrics, a recommendation's actions and
	// segment size. Derived by magnitudeOf, which is the one derivation both
	// sides of every comparison go through.
	magnitude string
	// markedBy is the canonical this document was ALREADY folded into by the
	// mid-run consolidation pass, or "" when it carries no mark.
	//
	// A document that carries one is not re-decided here. That mark was made
	// with judgement this check does not have — which member a reader should
	// be sent to, and what the folded members added that should survive the
	// fold — and re-deciding it against the stored index could point it
	// somewhere else, which is a reader sent to a different finding on a
	// second opinion nobody asked for.
	markedBy string
}

// subject projects a candidate onto the comparable part the shipped pairing
// rule reads, which is also what the mid-run consolidation pass projects its
// in-flight insights onto. See consolidate_cluster.go.
func (c dedupCandidate) subject() foldSubject {
	return foldSubject{id: c.id, docType: c.docType, vec: c.vec, text: c.text, magnitude: c.magnitude}
}

// magnitudeOf is the signature of whatever a document type's embedding text
// leaves out, which is what has to decide between two documents whose text is
// identical.
//
// For an insight that is the affected count, the metrics and the indicators —
// the supporting figures the model is asked to list, which carry a claim the
// prose need not repeat: two insights reporting a 0% and a 20% purchase rate in
// their indicators can read identically. For a recommendation it is the
// implementation actions and the segment size:
// BuildEmbeddingText carries the title, description, expected impact and the
// segment's NAME, so two recommendations that read the same and tell you to do
// DIFFERENT things — or to do the same thing to twice as many customers —
// embed identically, and suppressing the second hides the only part that
// differs.
//
// count is whichever population the document type counts: an insight's
// affected entities, a recommendation's target segment.
//
// One derivation, used for both sides of every comparison. Deriving the
// candidate's signature one way and the stored document's another gave a
// recommendation "" on one side and "affected=0" on the other, so sameClaim
// refused even an exact duplicate scoring 1.0 and cross-run recommendation
// suppression stopped working altogether.
func magnitudeOf(docType string, count int, metrics map[string]interface{}, actions, indicators []string) string {
	if docType == "recommendation" {
		return recommendationKey(count, actions)
	}
	return magnitudeKey(count, metrics, indicators)
}

// recommendationKey is the comparable form of what a recommendation's embedding
// text leaves out.
//
// The segment SIZE is in it because the text names the segment and never says
// how large it is: "win back lapsed subscribers" written word for word against
// 1,200 of them and against 14,000 is the same sentence and a different claim,
// which is the case AffectedCount covers on the insight side.
func recommendationKey(segmentSize int, actions []string) string {
	return fmt.Sprintf("segment=%d;%s", segmentSize, actionsKey(actions))
}

// actionsKey is the comparable form of a recommendation's implementation steps.
// Order is kept: "cut the price, then re-list" and "re-list, then cut the
// price" are different instructions.
func actionsKey(actions []string) string {
	if len(actions) == 0 {
		return ""
	}
	trimmed := make([]string, 0, len(actions))
	for _, a := range actions {
		trimmed = append(trimmed, strings.TrimSpace(a))
	}
	return "actions=" + strings.Join(trimmed, "|")
}

// magnitudeKey is the comparable form of the quantitative fields an insight's
// embedding text leaves out.
//
// Deterministic across runs and processes, so keys sorted and values formatted
// rather than fmt'ed off a map: ranging a Go map is randomised, and a signature
// that changes order between two documents would read as a magnitude change on
// every pair.
func magnitudeKey(affected int, metrics map[string]interface{}, indicators []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "affected=%d", affected)
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, ";%s=%v", k, metrics[k])
	}
	if key := indicatorsKey(indicators); key != "" {
		b.WriteString(";" + key)
	}
	return b.String()
}

// indicatorsKey is the comparable form of an insight's supporting figures.
//
// Sorted, unlike a recommendation's actions: an action list is an instruction
// and its order is part of it, while indicators are a set of observations about
// the same finding, and the same two figures listed the other way round are not
// a changed claim. Sorted on a COPY — ranging and sorting the model's own slice
// in place would reorder what gets stored and rendered.
func indicatorsKey(indicators []string) string {
	if len(indicators) == 0 {
		return ""
	}
	trimmed := make([]string, 0, len(indicators))
	for _, in := range indicators {
		if in = strings.TrimSpace(in); in != "" {
			trimmed = append(trimmed, in)
		}
	}
	if len(trimmed) == 0 {
		return ""
	}
	sort.Strings(trimmed)
	return "indicators=" + strings.Join(trimmed, "|")
}

// sameClaim reports whether two documents may be folded together at all.
//
// A duplicate check on a similarity score cannot see what the embedded text
// omits. StandaloneInsight.BuildEmbeddingText carries the name, description,
// area, severity and segment — and not the affected count or the metrics. So
// two insights with identical prose and materially different numbers embed
// identically, score 1.0, and the later one is suppressed: 100 affected
// entities hidden behind 1,000, when a magnitude that has MOVED is precisely
// what the prompt asks the model to report.
//
// The guard is narrow on purpose. It applies only when the texts are
// IDENTICAL, which is the case where the score carries no information at all.
// Two findings reworded between runs — the same shortfall restated with a
// slightly different count and amount — have different texts and different
// numbers and SHOULD still fold: that
// restatement is the whole point of the feature, and requiring equal magnitudes
// in general would switch it off.
func sameClaim(aText, aMagnitude, bText, bMagnitude string) bool {
	if aText == "" || bText == "" || aText != bText {
		return true
	}
	return aMagnitude == bMagnitude
}

// collectionFor maps a document type to the Mongo collection the duplicate
// mark is written to.
func collectionFor(docType string) string {
	if docType == "recommendation" {
		return "recommendations"
	}
	return "insights"
}

// markDuplicates finds near-duplicates for every document this run produced and
// stamps the later one, in two passes per document.
//
// FIRST against the vector store, which holds earlier runs' documents. A hit
// there is the truer original — it was published first and is the row the user
// has already seen — so it wins over anything in this run.
//
// THEN against this run's own earlier documents, in memory. That half could not
// exist before: the run's vectors are upserted only AFTER every check has run
// (deliberately — see dedupExclusions), and the check excludes every discovery
// this run produced, so two duplicates born in the same run could never match
// each other, by construction. On a run that emits a hundred insights, most of
// the repetition is exactly there. The comparison is cosine over vectors
// already in memory, which is the same measure the store's cosine collection
// scores on, so one threshold governs both passes.
//
// Chains are collapsed: if B duplicated A and C duplicates B, C is stamped as a
// duplicate of A. Pointing C at B would otherwise make C's only pointer a row
// that is itself hidden, which is how a suppression rule loses a document
// instead of folding it.
func (o *Orchestrator) markDuplicates(ctx context.Context, candidates []dedupCandidate, excludeDiscoveryIDs []string) {
	if len(candidates) == 0 {
		return
	}
	threshold := documentDedupMinScore()
	started := time.Now()

	// id -> the canonical document it was folded into, already resolved, so a
	// later match needs one lookup rather than a walk.
	canonical := make(map[string]string, len(candidates))
	// id -> position, so a canonical that is one of this run's own documents
	// can be compared against directly.
	indexByID := make(map[string]int, len(candidates))
	// The documents another document is already folded INTO. They cannot be
	// suppressed by this phase: a mid-run mark B -> A is already written and
	// persisted, so folding A into an earlier run's X would leave B pointing
	// at a hidden row — the one outcome the whole chain discipline exists to
	// prevent — and re-pointing B at X is not possible here, because X belongs
	// to another run and its vector is not in hand to re-verify B against.
	// Deferring A's cross-run fold costs one duplicate row this run; getting
	// it wrong costs B its only visible canonical.
	markTargets := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		indexByID[candidates[i].id] = i
		// Seed the chain map with the marks the mid-run pass already wrote,
		// so a document matching one of THOSE resolves to the same final
		// canonical the reader can see rather than to a hidden row.
		if candidates[i].markedBy != "" {
			canonical[candidates[i].id] = candidates[i].markedBy
			markTargets[candidates[i].markedBy] = struct{}{}
		}
	}
	crossRun, sameRun, preMarked, protected := 0, 0, 0, 0

	for i := range candidates {
		c := candidates[i]

		// Already consolidated mid-run. Neither pass runs for it: the stored
		// pass would re-point it at an earlier run's row and the same-run pass
		// would re-decide a judgement already made.
		if c.markedBy != "" {
			preMarked++
			continue
		}
		// A visible canonical with dependants. See markTargets.
		if _, isTarget := markTargets[c.id]; isTarget {
			protected++
			applog.WithField("id", c.id).
				Info("Phase 9: this document is another finding's canonical; not folding it")
			continue
		}

		if id, score, ok := o.storedDuplicate(ctx, c, excludeDiscoveryIDs, threshold); ok {
			o.markDuplicate(ctx, c, id, score, "earlier run")
			canonical[c.id] = id
			crossRun++
			continue
		}

		for _, earlier := range candidates[:i] {
			if earlier.docType != c.docType {
				continue
			}
			// The consolidation pass adjudicated this pair and decided both
			// should stand. It could not write the mark — the member it chose
			// to suppress belongs to an area the run had already finished —
			// but folding them here would apply exactly the direction it
			// rejected: document order, which is the signal it exists to
			// replace.
			if o.adjudicatedKeep(c.id, earlier.id) {
				applog.WithFields(applog.Fields{"id": c.id, "earlier": earlier.id}).
					Info("Phase 9: this pair was adjudicated and left standing; not folding")
				continue
			}
			// The whole pairing decision — threshold, magnitude guard and the
			// canonical-chain re-check that exists because cosine is not
			// transitive — lives in cosineFoldTarget, which the mid-run
			// consolidation pass runs too. One derivation of "are these the
			// same document", for the same reason magnitudeOf is one
			// derivation of what a document's figures are.
			target, targetScore, ok := cosineFoldTarget(
				c.subject(), earlier.subject(), threshold, canonical,
				func(id string) (foldSubject, bool) {
					k, inRun := indexByID[id]
					if !inRun {
						// The canonical belongs to an earlier RUN, so its
						// vector is not in hand. The stored pass already asked
						// the store about this document and said no, which is
						// the only evidence available.
						return foldSubject{}, false
					}
					return candidates[k].subject(), true
				},
				"Phase 9: ",
			)
			if !ok {
				continue
			}

			o.markDuplicate(ctx, c, target, targetScore, "this run")
			canonical[c.id] = target
			sameRun++
			break
		}
	}

	applog.WithFields(applog.Fields{
		"documents":    len(candidates),
		"cross_run":    crossRun,
		"same_run":     sameRun,
		"consolidated": preMarked,
		"protected":    protected,
		"threshold":    threshold,
		"duration_ms":  time.Since(started).Milliseconds(),
	}).Info("Phase 9: Duplicate check complete")
}

// storedDuplicate asks the vector store whether an EARLIER run already
// published this document, and returns the best match that a reader could
// actually be sent to.
//
// It walks the ranked hits rather than taking the first, because a suppressed
// duplicate stays indexed and a stale point can outlive its row. Both are
// matches this document must not be folded into, and both get MORE common as a
// project accumulates duplicates — so stopping at the first unusable hit made
// the check decay exactly where it is needed most. Walking on costs nothing in
// accuracy: every hit's score is this document against THAT document, measured
// by the store, so nothing here assumes cosine is transitive. (It is not: 0°,
// 25° and 50° score 0.906 between neighbours and 0.643 end to end, which is
// why the suppressed row's own canonical cannot simply be inherited.)
//
// A failed lookup is reported and treated as "no duplicate" — the phase is
// non-fatal and a store hiccup must not start stamping documents. A document
// left visible is the recoverable error; one hidden behind a row that is itself
// hidden, or behind no row at all, is not.
func (o *Orchestrator) storedDuplicate(ctx context.Context, c dedupCandidate, excludeDiscoveryIDs []string, threshold float64) (string, float64, bool) {
	results, err := o.vectorStore.FindDuplicates(ctx, c.vec, c.projectID, c.docType, excludeDiscoveryIDs, threshold)
	if err != nil {
		applog.WithError(err).WithField("id", c.id).Warn("Duplicate check failed")
		return "", 0, false
	}
	for _, hit := range results {
		if hit.ID == "" || hit.ID == c.id {
			continue
		}
		if o.embedIndexStore == nil {
			return hit.ID, hit.Score, true
		}
		doc, exists, err := o.embedIndexStore.IndexedDoc(ctx, collectionFor(c.docType), hit.ID)
		switch {
		case err != nil:
			// The state of this hit is unknown, and so is every other hit's —
			// the same store answers for all of them. Refuse the whole check
			// rather than walk on and fold into a row that may be just as bad.
			applog.WithError(err).WithFields(applog.Fields{"id": c.id, "hit": hit.ID}).
				Warn("could not read the matched document; not folding")
			return "", 0, false
		case !exists:
			applog.WithFields(applog.Fields{"id": c.id, "hit": hit.ID}).
				Info("Phase 9: a stored match has no document left; looking past it")
			continue
		case doc.DuplicateOf != "":
			applog.WithFields(applog.Fields{"id": c.id, "hit": hit.ID, "hit_folded_into": doc.DuplicateOf}).
				Info("Phase 9: a stored match is itself suppressed; looking past it")
			continue
		case !sameClaim(c.text, c.magnitude, doc.EmbeddingText, doc.magnitude(c.docType)):
			// Identical prose, different numbers. The same rule as the
			// same-run pass, applied at the other read.
			applog.WithFields(applog.Fields{"id": c.id, "hit": hit.ID}).
				Info("Phase 9: same wording as a stored document, different magnitude; looking past it")
			continue
		}
		return hit.ID, hit.Score, true
	}
	return "", 0, false
}

// markDuplicate stamps duplicate_of + similarity_score on the later document.
// The mark is the whole output of this check — nothing else is changed, and the
// document is still embedded and indexed, because it is real content that a
// search should still reach.
func (o *Orchestrator) markDuplicate(ctx context.Context, c dedupCandidate, duplicateOf string, score float64, source string) {
	applog.WithFields(applog.Fields{
		"id":           c.id,
		"duplicate_of": duplicateOf,
		"score":        score,
		"type":         c.docType,
		"source":       source,
	}).Info("Phase 9: Duplicate detected")

	if err := o.embedIndexStore.UpdateDuplicate(ctx, collectionFor(c.docType), c.id, duplicateOf, score); err != nil {
		applog.WithError(err).WithField("id", c.id).Warn("Failed to mark duplicate")
		// The standalone copy is what the API filters on. If that write failed
		// the document is not suppressed anywhere, and mirroring the mark onto
		// the embedded copy would hide it on the dashboard while the API still
		// returned it — two surfaces disagreeing about the same document.
		return
	}

	// And onto the copy embedded in the discovery, which is what the project's
	// insight and recommendation lists and the run's own detail page render.
	// Best-effort: the standalone mark has already landed and it is the
	// authoritative one, so a retired discovery must not turn this into a
	// failure.
	if o.discoveryRepo == nil || c.discoveryID == "" {
		return
	}
	mirrored, err := o.discoveryRepo.MarkEmbeddedDuplicate(ctx, c.discoveryID, collectionFor(c.docType), c.id, duplicateOf, score)
	if err != nil {
		applog.WithError(err).WithFields(applog.Fields{"id": c.id, "discovery_id": c.discoveryID}).
			Warn("Could not mirror the duplicate mark onto the embedded copy; the dashboard will still show it")
		return
	}
	if !mirrored {
		applog.WithFields(applog.Fields{"id": c.id, "discovery_id": c.discoveryID}).
			Warn("No embedded copy to mark; the dashboard will still show this document")
	}
}

// cosineSimilarity returns the cosine of the angle between a and b, and whether
// it could be computed at all.
//
// Proper cosine rather than a dot product: the embeddings are not guaranteed
// normalised, and the vector store's cosine collection scores on this measure —
// so computing anything else here would mean the in-memory pass and the stored
// pass could not share a threshold.
//
// Not-ok for an empty vector, a length mismatch (a provider that changed model
// mid-run), or a zero vector, where the angle is undefined. Those are "no
// comparison", never a match: a zero-norm shortcut returning 0 would read as
// "definitely different", which is also wrong but less obviously.
func cosineSimilarity(a, b []float64) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), true
}
