//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// The risk-configuration fingerprint, end to end against MongoDB.
//
// It is served on read so a client can tell whether the copy it is editing is
// still the stored one, and maintained on write so a concurrent editor of that
// configuration can tell that THIS route changed it. Both halves have to hold
// against a real server: the value is compared against one computed from a
// document that has been through BSON, and the stored one must survive a write
// that carries a client's echo of it.

func hashTestPack(slug string) *models.DomainPack {
	weight := 1.25
	return &models.DomainPack{
		Slug:        slug,
		Name:        "Acme",
		Description: "A marketplace.",
		Categories:  []models.PackCategory{{ID: "buyers", Name: "Buyers", Description: "End consumers."}},
		RiskTaxonomy: []models.PackRiskCategory{
			{ID: "revenue_leak", Label: "Revenue leakage", Description: "Orders lost.", Weight: 2.5},
			{ID: "customer_churn", Label: "Customer churn", Description: "Buyers who stop.", Weight: 2},
		},
		AnalysisAreas: models.PackAnalysisAreas{
			Base: []models.PackAnalysisArea{
				{ID: "conversion", Name: "Conversion", Description: "The funnel.", Keywords: []string{"funnel"},
					Priority: 1, Prompt: "p1", RiskCategories: []string{"revenue_leak"}},
				{ID: "retention", Name: "Retention", Description: "Coming back.", Keywords: []string{"retention"},
					Priority: 2, Prompt: "p2", RiskCategories: []string{"customer_churn"}, SeverityWeight: &weight},
			},
		},
	}
}

func storedRiskHash(t *testing.T, db *DB, slug string) string {
	t.Helper()
	var doc struct {
		Hash string `bson:"risk_config_hash"`
	}
	if err := db.Collection("domain_packs").FindOne(context.Background(), bson.M{"slug": slug}).Decode(&doc); err != nil {
		t.Fatalf("read stored hash: %v", err)
	}
	return doc.Hash
}

func TestInteg_RiskConfigHashIsServedAndStored(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)
	const slug = "hash-pack"

	pack := hashTestPack(slug)
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Stored at insert, so "has a fingerprint" is the normal case.
	want := models.RiskConfigHash(pack)
	if got := storedRiskHash(t, db, slug); got != want {
		t.Errorf("stored hash = %q, want %q", got, want)
	}

	// Served on read, and computed from the document as it came back out of
	// BSON — which is the comparison every caller actually makes.
	served, err := repo.GetBySlug(ctx, slug)
	if err != nil || served == nil {
		t.Fatalf("get: %v", err)
	}
	if served.RiskConfigHash != want {
		t.Errorf("served hash = %q, want %q — a value that does not survive the round trip makes every edit a conflict",
			served.RiskConfigHash, want)
	}

	// A list serves it too: which handler a client happened to use is not
	// something it can see.
	packs, err := repo.List(ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range packs {
		if p.Slug == slug && p.RiskConfigHash != want {
			t.Errorf("listed hash = %q, want %q", p.RiskConfigHash, want)
		}
	}
}

func TestInteg_RiskConfigHashTracksTheConfigurationOnUpdate(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)
	const slug = "hash-update-pack"

	pack := hashTestPack(slug)
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := storedRiskHash(t, db, slug)

	// A prompt rewrite is not a change to the risk configuration, and a
	// concurrent risk save must not be conflicted over it — a section
	// regeneration rewrites prose on every area.
	prose := hashTestPack(slug)
	prose.AnalysisAreas.Base[0].Prompt = "rewritten entirely"
	prose.Description = "something else"
	if err := repo.Update(ctx, slug, prose); err != nil {
		t.Fatalf("update prose: %v", err)
	}
	if got := storedRiskHash(t, db, slug); got != before {
		t.Errorf("a prose-only edit moved the fingerprint:\n before %s\n after  %s", before, got)
	}

	// A weight is.
	reweighted := hashTestPack(slug)
	reweighted.RiskTaxonomy[0].Weight = 0.5
	if err := repo.Update(ctx, slug, reweighted); err != nil {
		t.Fatalf("update weight: %v", err)
	}
	after := storedRiskHash(t, db, slug)
	if after == before {
		t.Error("a changed weight left the fingerprint alone, so a concurrent editor cannot see this write")
	}
	if want := models.RiskConfigHash(reweighted); after != want {
		t.Errorf("stored hash = %q, want %q", after, want)
	}
}

// TestInteg_AnEchoedRiskConfigHashCannotOverwriteTheStoredOne is the trap this
// design exists to avoid, end to end.
//
// The fingerprint is served to clients and this route takes the whole pack
// back, so a client's echo must not become the stored value — every writer's
// comparison would then be against a number a client chose.
//
// What this pins is the OUTCOME through this route, which two mechanisms
// defend together: the `bson:"-"` on the served field, and the repository
// writing the stored one explicitly from the content. It does not distinguish
// them — removing either leaves this green — so the tag is pinned separately
// in models.TestRiskConfigHashIsNeverMarshalledToBSON, where it is the only
// thing in play.
func TestInteg_AnEchoedRiskConfigHashCannotOverwriteTheStoredOne(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)
	const slug = "hash-echo-pack"

	if err := repo.Create(ctx, hashTestPack(slug)); err != nil {
		t.Fatalf("create: %v", err)
	}

	echoed := hashTestPack(slug)
	echoed.RiskConfigHash = "deadbeef-from-a-stale-client"
	if err := repo.Update(ctx, slug, echoed); err != nil {
		t.Fatalf("update: %v", err)
	}

	if got := storedRiskHash(t, db, slug); got != models.RiskConfigHash(echoed) {
		t.Errorf("stored hash = %q, want the computed %q", got, models.RiskConfigHash(echoed))
	}
	if storedRiskHash(t, db, slug) == "deadbeef-from-a-stale-client" {
		t.Error("a client's echo overwrote the stored fingerprint")
	}
}
