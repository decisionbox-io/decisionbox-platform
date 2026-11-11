package discovery

import (
	"context"
	"fmt"
	"math"
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
			DiscoveredAt:  ins.DiscoveredAt,
			CreatedAt:     now,
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

	// Batch embed
	applog.WithFields(applog.Fields{
		"count": len(allTexts),
		"model": modelName,
	}).Info("Phase 9: Embedding documents")

	vectors, err := o.embeddingProvider.Embed(ctx, allTexts)
	if err != nil {
		return fmt.Errorf("embed documents: %w", err)
	}

	if len(vectors) != len(allTexts) {
		return fmt.Errorf("embedding count mismatch: expected %d, got %d", len(allTexts), len(vectors))
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
			id: ins.ID, docType: "insight", projectID: ins.ProjectID, vec: vec,
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
			id: rec.ID, docType: "recommendation", projectID: rec.ProjectID, vec: vec,
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

// dedupCandidate is one document this run produced, as the duplicate check sees
// it: an id, what kind of document it is, and the vector that was just embedded
// for it.
type dedupCandidate struct {
	id        string
	docType   string
	projectID string
	vec       []float64
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
	for i := range candidates {
		indexByID[candidates[i].id] = i
	}
	crossRun, sameRun := 0, 0

	for i := range candidates {
		c := candidates[i]

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
			score, ok := cosineSimilarity(c.vec, earlier.vec)
			if !ok || score < threshold {
				continue
			}
			target, targetScore := earlier.id, score
			if resolved, folded := canonical[earlier.id]; folded {
				// Cosine is not transitive. Three vectors at 0°, 25° and 50°
				// score 0.906 between neighbours and 0.643 end to end, so
				// matching the middle one says nothing about the first — and
				// folding into it anyway would hide a document behind a row it
				// does not resemble, under the neighbour's score. So the claim
				// is re-checked against the canonical itself, and when it does
				// not hold the document is left alone: it is not a duplicate of
				// anything the reader can see.
				k, inRun := indexByID[resolved]
				if !inRun {
					// The canonical belongs to an earlier RUN, so its vector is
					// not in hand. The stored pass already asked the store
					// about this document and got no match, which is the only
					// evidence available — and it said no.
					continue
				}
				s, ok := cosineSimilarity(c.vec, candidates[k].vec)
				if !ok || s < threshold {
					continue
				}
				target, targetScore = resolved, s
			}
			if target == "" || target == c.id {
				continue
			}

			o.markDuplicate(ctx, c, target, targetScore, "this run")
			canonical[c.id] = target
			sameRun++
			break
		}
	}

	applog.WithFields(applog.Fields{
		"documents":   len(candidates),
		"cross_run":   crossRun,
		"same_run":    sameRun,
		"threshold":   threshold,
		"duration_ms": time.Since(started).Milliseconds(),
	}).Info("Phase 9: Duplicate check complete")
}

// storedDuplicate asks the vector store whether an EARLIER run already
// published this document. A failed lookup is reported and treated as "no
// duplicate": the phase is non-fatal, and a store hiccup must not start
// stamping documents.
func (o *Orchestrator) storedDuplicate(ctx context.Context, c dedupCandidate, excludeDiscoveryIDs []string, threshold float64) (string, float64, bool) {
	results, err := o.vectorStore.FindDuplicates(ctx, c.vec, c.projectID, c.docType, excludeDiscoveryIDs, threshold)
	if err != nil {
		applog.WithError(err).WithField("id", c.id).Warn("Duplicate check failed")
		return "", 0, false
	}
	if len(results) == 0 || results[0].ID == "" || results[0].ID == c.id {
		return "", 0, false
	}
	hit := results[0]

	// An earlier run's duplicate stays indexed, so the best match can be a row
	// that is itself hidden. Folding into it would give this document a pointer
	// to something the reader cannot see — and cosine is not transitive, so
	// resembling a suppressed row says nothing about resembling the visible one
	// it was folded into: 0°, 25° and 50° score 0.906 between neighbours and
	// 0.643 end to end. The canonical's vector is not in hand to check against,
	// so the fold is refused rather than guessed, exactly as the same-run pass
	// refuses it. The document stays visible, which is the recoverable error.
	if o.embedIndexStore != nil {
		switch folded, err := o.embedIndexStore.DuplicateOf(ctx, collectionFor(c.docType), hit.ID); {
		case err != nil:
			applog.WithError(err).WithFields(applog.Fields{"id": c.id, "hit": hit.ID}).
				Warn("could not tell whether the matched document is itself a duplicate; not folding")
			return "", 0, false
		case folded != "":
			applog.WithFields(applog.Fields{"id": c.id, "hit": hit.ID, "hit_folded_into": folded}).
				Info("Phase 9: best stored match is itself suppressed; leaving this document visible")
			return "", 0, false
		}
	}
	return hit.ID, hit.Score, true
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
