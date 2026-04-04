package discovery

import (
	"context"
	"fmt"
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

	if err := o.embedAndIndex(ctx, insights, recs); err != nil {
		applog.WithError(err).Error("Phase 9: Embedding/indexing failed (non-fatal)")
	}
}

// denormalizeInsights converts discovery result insights to standalone documents with UUIDs.
func (o *Orchestrator) denormalizeInsights(result *models.DiscoveryResult) []*commonmodels.StandaloneInsight {
	insights := make([]*commonmodels.StandaloneInsight, 0, len(result.Insights))
	now := time.Now()

	for _, ins := range result.Insights {
		insights = append(insights, &commonmodels.StandaloneInsight{
			ID:            uuid.New().String(),
			ProjectID:     result.ProjectID,
			DiscoveryID:   result.ID,
			Domain:        result.Domain,
			Category:      result.Category,
			AnalysisArea:  ins.AnalysisArea,
			Name:          ins.Name,
			Description:   ins.Description,
			Severity:      ins.Severity,
			AffectedCount: ins.AffectedCount,
			RiskScore:     ins.RiskScore,
			Confidence:    ins.Confidence,
			Metrics:       ins.Metrics,
			Indicators:    ins.Indicators,
			TargetSegment: ins.TargetSegment,
			SourceSteps:   ins.SourceSteps,
			Validation:    convertValidation(ins.Validation),
			DiscoveredAt:  ins.DiscoveredAt,
			CreatedAt:     now,
		})
	}

	return insights
}

// denormalizeRecommendations converts discovery result recommendations to standalone documents with UUIDs.
func (o *Orchestrator) denormalizeRecommendations(result *models.DiscoveryResult) []*commonmodels.StandaloneRecommendation {
	recs := make([]*commonmodels.StandaloneRecommendation, 0, len(result.Recommendations))
	now := time.Now()

	for _, rec := range result.Recommendations {
		recs = append(recs, &commonmodels.StandaloneRecommendation{
			ID:                     uuid.New().String(),
			ProjectID:              result.ProjectID,
			DiscoveryID:            result.ID,
			Domain:                 result.Domain,
			Category:               result.Category,
			RecommendationCategory: rec.Category,
			Title:                  rec.Title,
			Description:            rec.Description,
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
			CreatedAt:         now,
		})
	}

	return recs
}

// saveStandaloneDocuments saves denormalized insights/recommendations to MongoDB.
func (o *Orchestrator) saveStandaloneDocuments(ctx context.Context, insights []*commonmodels.StandaloneInsight, recs []*commonmodels.StandaloneRecommendation) error {
	if len(insights) > 0 {
		docs := make([]interface{}, len(insights))
		for i, ins := range insights {
			docs[i] = ins
		}
		_, err := o.db.Collection("insights").InsertMany(ctx, docs)
		if err != nil {
			return fmt.Errorf("insert insights: %w", err)
		}
	}

	if len(recs) > 0 {
		docs := make([]interface{}, len(recs))
		for i, rec := range recs {
			docs[i] = rec
		}
		_, err := o.db.Collection("recommendations").InsertMany(ctx, docs)
		if err != nil {
			return fmt.Errorf("insert recommendations: %w", err)
		}
	}

	return nil
}

// embedAndIndex embeds documents and upserts vectors to Qdrant.
func (o *Orchestrator) embedAndIndex(ctx context.Context, insights []*commonmodels.StandaloneInsight, recs []*commonmodels.StandaloneRecommendation) error {
	dims := o.embeddingProvider.Dimensions()
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

	// Update MongoDB with embedding text/model, run dedup, and build Qdrant points
	var points []vectorstore.Point
	vecIdx := 0

	for _, ins := range insights {
		vec := vectors[vecIdx]
		vecIdx++

		// Update MongoDB with embedding fields
		_, err := o.db.Collection("insights").UpdateByID(ctx, ins.ID, map[string]interface{}{
			"$set": map[string]interface{}{
				"embedding_text":  ins.EmbeddingText,
				"embedding_model": ins.EmbeddingModel,
			},
		})
		if err != nil {
			applog.WithError(err).WithField("id", ins.ID).Warn("Failed to update insight embedding")
		}

		// Deduplication check
		o.checkAndMarkDuplicate(ctx, ins.ID, vec, ins.ProjectID, "insight", ins.DiscoveryID)

		points = append(points, vectorstore.Point{
			ID:     ins.ID,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":     ins.ProjectID,
				"discovery_id":   ins.DiscoveryID,
				"embedding_model": modelName,
				"severity":       ins.Severity,
				"analysis_area":  ins.AnalysisArea,
				"confidence":     ins.Confidence,
				"created_at":     ins.CreatedAt.Format(time.RFC3339),
			},
		})
	}

	for _, rec := range recs {
		vec := vectors[vecIdx]
		vecIdx++

		// Update MongoDB with embedding fields
		_, err := o.db.Collection("recommendations").UpdateByID(ctx, rec.ID, map[string]interface{}{
			"$set": map[string]interface{}{
				"embedding_text":  rec.EmbeddingText,
				"embedding_model": rec.EmbeddingModel,
			},
		})
		if err != nil {
			applog.WithError(err).WithField("id", rec.ID).Warn("Failed to update recommendation embedding")
		}

		// Deduplication check
		o.checkAndMarkDuplicate(ctx, rec.ID, vec, rec.ProjectID, "recommendation", rec.DiscoveryID)

		points = append(points, vectorstore.Point{
			ID:     rec.ID,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "recommendation",
				"project_id":     rec.ProjectID,
				"discovery_id":   rec.DiscoveryID,
				"embedding_model": modelName,
				"confidence":     rec.Confidence,
				"created_at":     rec.CreatedAt.Format(time.RFC3339),
			},
		})
	}

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

// checkAndMarkDuplicate searches for near-duplicate vectors and marks the document.
func (o *Orchestrator) checkAndMarkDuplicate(ctx context.Context, docID string, vec []float64, projectID, docType, discoveryID string) {
	results, err := o.vectorStore.FindDuplicates(ctx, vec, projectID, docType, discoveryID, 0.95)
	if err != nil {
		applog.WithError(err).WithField("id", docID).Warn("Duplicate check failed")
		return
	}

	if len(results) == 0 {
		return
	}

	dup := results[0]
	applog.WithFields(applog.Fields{
		"id":           docID,
		"duplicate_of": dup.ID,
		"score":        dup.Score,
	}).Info("Phase 9: Duplicate detected")

	collection := "insights"
	if docType == "recommendation" {
		collection = "recommendations"
	}

	_, err = o.db.Collection(collection).UpdateByID(ctx, docID, map[string]interface{}{
		"$set": map[string]interface{}{
			"duplicate_of":     dup.ID,
			"similarity_score": dup.Score,
		},
	})
	if err != nil {
		applog.WithError(err).WithField("id", docID).Warn("Failed to mark duplicate")
	}
}

// convertValidation converts agent-side validation to shared model validation.
func convertValidation(v *models.InsightValidation) *commonmodels.InsightValidation {
	if v == nil {
		return nil
	}
	return &commonmodels.InsightValidation{
		Status:        v.Status,
		VerifiedCount: v.VerifiedCount,
		OriginalCount: v.OriginalCount,
		Reasoning:     v.Reasoning,
		ValidatedAt:   v.ValidatedAt,
	}
}
