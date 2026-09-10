// Package insightsearch is the ask-serve agent's read-only retriever over a
// project's discovered insights and recommendations. It implements
// ai.InsightsProvider by embedding the query, searching the shared vector store
// (scoped to the project), and enriching each hit from the Mongo insights /
// recommendations collections. It mirrors the enterprise ask/services
// InsightSearcher but takes the per-project embedder the ask-serve builder
// already constructs, so it needs no secret or project lookup.
package insightsearch

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

const (
	defaultLimit = 5
	maxLimit     = 20
	// overfetchFactor widens the vector fetch beyond the requested k so dropping
	// stale/unenrichable top hits doesn't starve the enrichable ones below them.
	overfetchFactor = 2
)

// enrichFunc resolves a hit's display fields (name/description/severity/area)
// by id + type. Mongo-backed in production; stubbed in tests. Returns false
// when the doc can't be found — the hit is still returned (id/type/score) so a
// stale vector entry degrades gracefully rather than dropping the result.
type enrichFunc func(ctx context.Context, id, docType string) (ai.InsightHit, bool)

// Searcher is a per-project ai.InsightsProvider.
type Searcher struct {
	projectID string
	vs        vectorstore.Provider
	embedder  goembedding.Provider
	enrich    enrichFunc
}

// New builds a Searcher. Returns nil when the Mongo db, vector store, or
// embedder is absent — the caller treats a nil provider as "insights tool
// unavailable" and simply doesn't offer the tool.
func New(projectID string, db *mongo.Database, vs vectorstore.Provider, embedder goembedding.Provider) *Searcher {
	if db == nil || vs == nil || embedder == nil {
		return nil
	}
	return &Searcher{projectID: projectID, vs: vs, embedder: embedder, enrich: mongoEnricher(db, projectID)}
}

// mongoEnricher reads the canonical insights / recommendations collections.
func mongoEnricher(db *mongo.Database, projectID string) enrichFunc {
	return func(ctx context.Context, id, docType string) (ai.InsightHit, bool) {
		switch docType {
		case "insight":
			var ins struct {
				Name          string `bson:"name"`
				Description   string `bson:"description"`
				Severity      string `bson:"severity"`
				AnalysisArea  string `bson:"analysis_area"`
				AffectedCount int    `bson:"affected_count"`
				DiscoveryID   string `bson:"discovery_id"`
			}
			if err := db.Collection("insights").FindOne(ctx, bson.M{"_id": id, "project_id": projectID}).Decode(&ins); err != nil {
				return ai.InsightHit{}, false
			}
			return ai.InsightHit{Name: ins.Name, Description: ins.Description, Severity: ins.Severity, AnalysisArea: ins.AnalysisArea, AffectedCount: ins.AffectedCount, DiscoveryID: ins.DiscoveryID}, true
		case "recommendation":
			var rec struct {
				Title        string `bson:"title"`
				Description  string `bson:"description"`
				Severity     string `bson:"severity"`
				AnalysisArea string `bson:"analysis_area"`
				DiscoveryID  string `bson:"discovery_id"`
			}
			if err := db.Collection("recommendations").FindOne(ctx, bson.M{"_id": id, "project_id": projectID}).Decode(&rec); err != nil {
				return ai.InsightHit{}, false
			}
			return ai.InsightHit{Name: rec.Title, Description: rec.Description, Severity: rec.Severity, AnalysisArea: rec.AnalysisArea, DiscoveryID: rec.DiscoveryID}, true
		}
		return ai.InsightHit{}, false
	}
}

// SearchInsights embeds the query, searches the project's insight vectors, and
// enriches each hit with its title/description/severity. Hits are returned in
// vector-score order.
func (s *Searcher) SearchInsights(ctx context.Context, query string, k int) ([]ai.InsightHit, error) {
	if s == nil || s.vs == nil || s.embedder == nil {
		return nil, errors.New("insight search not configured")
	}
	if k <= 0 {
		k = defaultLimit
	}
	if k > maxLimit {
		k = maxLimit
	}

	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, errors.New("embed query: no vector returned")
	}

	// Scope the search to the two payload types this tool can render. Insights,
	// recommendations, knowledge-source chunks AND ledger findings share one
	// project collection, so an unfiltered search also returns ledger_finding /
	// source_chunk points that this searcher cannot enrich — they would surface
	// as empty-titled citations. (Mirrors the enterprise insightSearcher.)
	//
	// Overfetch before enrichment: a stale/deleted top hit that we drop below
	// consumes a slot, so requesting exactly k could starve valid insights ranked
	// just under the stale ones (returning < k, or even 0, and then not grounding).
	// Fetch a wider slice, enrich/filter, then trim back to k. Bounded by maxLimit.
	fetch := k * overfetchFactor
	if fetch > maxLimit {
		fetch = maxLimit
	}
	res, err := s.vs.Search(ctx, vecs[0], vectorstore.SearchOpts{
		ProjectIDs:     []string{s.projectID},
		Types:          []string{"insight", "recommendation"},
		EmbeddingModel: s.embedder.ModelName(),
		Limit:          fetch,
	})
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	out := make([]ai.InsightHit, 0, k)
	for _, r := range res {
		if len(out) >= k {
			break // enough enrichable hits gathered; trim the overfetch
		}
		docType, _ := r.Payload["type"].(string)
		enriched, ok := s.enrich(ctx, r.ID, docType)
		if !ok {
			// A hit this searcher cannot enrich (unknown type, or the underlying
			// doc was deleted while its vector lingered) carries no display
			// content — drop it rather than emit an empty citation.
			continue
		}
		out = append(out, ai.InsightHit{
			ID:            r.ID,
			Type:          docType,
			Score:         r.Score,
			Name:          enriched.Name,
			Description:   enriched.Description,
			Severity:      enriched.Severity,
			AnalysisArea:  enriched.AnalysisArea,
			AffectedCount: enriched.AffectedCount,
			DiscoveryID:   enriched.DiscoveryID,
		})
	}
	return out, nil
}
