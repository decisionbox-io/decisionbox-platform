//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The dashboard renders the copies of insights and recommendations EMBEDDED in
// the discovery document, not the denormalised collections — the project's
// insight list, its recommendation list and a run's own detail page all read
// those. So a duplicate mark that lives only on the standalone document reaches
// the API and none of the screens anybody looks at.
//
// This needs a real MongoDB because the whole of it is a positional
// array-filter update: whether the right element is written, whether a wrong id
// is reported rather than silently touching `updated_at`, and whether the BSON
// round-trip puts the fields back where the UI reads them.
func TestMarkEmbeddedDuplicate(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	repo := NewDiscoveryRepository(db)
	ctx := context.Background()

	save := func(t *testing.T) *models.DiscoveryResult {
		t.Helper()
		res := &models.DiscoveryResult{
			ProjectID: "proj-1",
			Insights: []models.Insight{
				{ID: "ins-canonical", Name: "The original", AnalysisArea: "pricing", Severity: "high"},
				{ID: "ins-restated", Name: "The same thing again", AnalysisArea: "pricing", Severity: "high"},
			},
			Recommendations: []models.Recommendation{
				{ID: "rec-canonical", Title: "Do the thing"},
				{ID: "rec-restated", Title: "Do the thing, again"},
			},
		}
		if err := repo.Save(ctx, res); err != nil {
			t.Fatalf("save: %v", err)
		}
		return res
	}

	t.Run("the marked element is the one written", func(t *testing.T) {
		res := save(t)
		mirrored, err := repo.MarkEmbeddedDuplicate(ctx, res.ID, "insights", "ins-restated", "ins-canonical", 0.93)
		if err != nil || !mirrored {
			t.Fatalf("MarkEmbeddedDuplicate = (%v, %v)", mirrored, err)
		}
		got, err := repo.GetByID(ctx, res.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		byID := map[string]models.Insight{}
		for _, in := range got.Insights {
			byID[in.ID] = in
		}
		if m := byID["ins-restated"]; m.DuplicateOf != "ins-canonical" || m.SimilarityScore != 0.93 {
			t.Errorf("marked insight = %+v, want duplicate_of=ins-canonical score=0.93", m)
		}
		// And only that one: a positional update that matched the wrong
		// element, or every element, would hide the original too.
		if c := byID["ins-canonical"]; c.DuplicateOf != "" || c.SimilarityScore != 0 {
			t.Errorf("the canonical was marked as well: %+v", c)
		}
	})

	t.Run("recommendations too", func(t *testing.T) {
		res := save(t)
		if mirrored, err := repo.MarkEmbeddedDuplicate(ctx, res.ID, "recommendations", "rec-restated", "rec-canonical", 0.9); err != nil || !mirrored {
			t.Fatalf("MarkEmbeddedDuplicate = (%v, %v)", mirrored, err)
		}
		got, _ := repo.GetByID(ctx, res.ID)
		for _, r := range got.Recommendations {
			switch r.ID {
			case "rec-restated":
				if r.DuplicateOf != "rec-canonical" {
					t.Errorf("restated recommendation not marked: %+v", r)
				}
			case "rec-canonical":
				if r.DuplicateOf != "" {
					t.Errorf("canonical recommendation was marked: %+v", r)
				}
			}
		}
	})

	t.Run("an id that is not there reports so, rather than touching the document", func(t *testing.T) {
		// Without the `insights.id` predicate on the main filter, an UpdateOne
		// with a wrong id still matches the discovery, writes `updated_at`,
		// touches no array element and returns nil — a silent no-op the caller
		// would log as a success.
		res := save(t)
		mirrored, err := repo.MarkEmbeddedDuplicate(ctx, res.ID, "insights", "ins-nonexistent", "ins-canonical", 0.9)
		if err != nil {
			t.Fatalf("a missing element must not be an error: %v", err)
		}
		if mirrored {
			t.Error("reported a mirrored mark for an id the document does not contain")
		}
	})

	t.Run("a discovery that is gone is not an error", func(t *testing.T) {
		// This runs after the authoritative mark has landed, so a retired
		// discovery must not turn index maintenance into a failure.
		if mirrored, err := repo.MarkEmbeddedDuplicate(ctx, "60ddf2a5b7f3c20c1f9a0b11", "insights", "ins-restated", "ins-canonical", 0.9); err != nil || mirrored {
			t.Errorf("got (%v, %v), want (false, nil)", mirrored, err)
		}
	})

	t.Run("a bad field name or id is a programmer error", func(t *testing.T) {
		res := save(t)
		if _, err := repo.MarkEmbeddedDuplicate(ctx, res.ID, "findings", "ins-restated", "ins-canonical", 0.9); err == nil {
			t.Error("an unknown embedded field was accepted")
		}
		if _, err := repo.MarkEmbeddedDuplicate(ctx, "not-an-object-id", "insights", "x", "y", 0.9); err == nil {
			t.Error("an invalid discovery id was accepted")
		}
	})
}
