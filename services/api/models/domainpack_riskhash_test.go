package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func hashPack() *DomainPack {
	weight := 1.25
	return &DomainPack{
		Slug: "acme",
		RiskTaxonomy: []PackRiskCategory{
			{ID: "revenue_leak", Label: "Revenue leakage", Description: "Orders lost.", Weight: 2.5},
			{ID: "customer_churn", Label: "Customer churn", Description: "Buyers who stop.", Weight: 2},
		},
		AnalysisAreas: PackAnalysisAreas{
			Base: []PackAnalysisArea{
				{ID: "conversion", Name: "Conversion", Prompt: "p1", RiskCategories: []string{"revenue_leak"}},
				{ID: "retention", Name: "Retention", Prompt: "p2", RiskCategories: []string{"customer_churn"}, SeverityWeight: &weight},
			},
			Categories: map[string][]PackAnalysisArea{
				"buyers": {{ID: "repeat", Name: "Repeat", Prompt: "p3", RiskCategories: []string{"revenue_leak"}}},
			},
		},
	}
}

// TestRiskConfigHashCoversTheConfigurationAndNothingElse is the whole contract.
//
// Every case below is something a concurrent writer either must or must not be
// told about, and the fingerprint is what tells them.
func TestRiskConfigHashCoversTheConfigurationAndNothingElse(t *testing.T) {
	base := RiskConfigHash(hashPack())
	if base == "" {
		t.Fatal("a pack with a configuration hashed to nothing")
	}
	if RiskConfigHash(hashPack()) != base {
		t.Fatal("the same configuration hashed twice to different values")
	}

	changes := []struct {
		name   string
		mutate func(*DomainPack)
		differ bool
	}{
		{"a category's weight", func(p *DomainPack) { p.RiskTaxonomy[0].Weight = 0.5 }, true},
		{"a category's label", func(p *DomainPack) { p.RiskTaxonomy[0].Label = "Lost revenue" }, true},
		{"a category removed", func(p *DomainPack) { p.RiskTaxonomy = p.RiskTaxonomy[:1] }, true},
		// Declaration order is the order an insight's risk flags come out in,
		// so it is part of the configuration.
		{"the taxonomy reordered", func(p *DomainPack) {
			p.RiskTaxonomy[0], p.RiskTaxonomy[1] = p.RiskTaxonomy[1], p.RiskTaxonomy[0]
		}, true},
		{"an area's mapping", func(p *DomainPack) {
			p.AnalysisAreas.Base[0].RiskCategories = []string{"customer_churn"}
		}, true},
		{"an area's override cleared", func(p *DomainPack) { p.AnalysisAreas.Base[1].SeverityWeight = nil }, true},
		{"an override set to zero rather than absent", func(p *DomainPack) {
			zero := 0.0
			p.AnalysisAreas.Base[0].SeverityWeight = &zero
		}, true},
		{"a per-category area's mapping", func(p *DomainPack) {
			p.AnalysisAreas.Categories["buyers"][0].RiskCategories = []string{"customer_churn"}
		}, true},

		// And the things a concurrent risk edit has no stake in. A prompt
		// rewrite is the common case: a section regeneration touches prose on
		// every area, and conflicting a risk save over that would make the
		// two features unusable together.
		{"an area's prompt", func(p *DomainPack) { p.AnalysisAreas.Base[0].Prompt = "rewritten" }, false},
		{"an area's name", func(p *DomainPack) { p.AnalysisAreas.Base[0].Name = "Funnel" }, false},
		{"the pack's description", func(p *DomainPack) { p.Description = "something else" }, false},
		// An area's position is not something anyone chose, and the array is
		// rewritten wholesale by a regeneration.
		{"the areas reordered", func(p *DomainPack) {
			p.AnalysisAreas.Base[0], p.AnalysisAreas.Base[1] = p.AnalysisAreas.Base[1], p.AnalysisAreas.Base[0]
		}, false},
	}

	for _, tc := range changes {
		pack := hashPack()
		tc.mutate(pack)
		got := RiskConfigHash(pack)
		if tc.differ && got == base {
			t.Errorf("%s did not change the fingerprint", tc.name)
		}
		if !tc.differ && got != base {
			t.Errorf("%s changed the fingerprint", tc.name)
		}
	}
}

// TestRiskConfigHashSurvivesABSONRoundTrip -- the value is compared against
// one computed from a document read back out of MongoDB. If the two differ,
// every write is a conflict and no risk configuration can be saved at all.
func TestRiskConfigHashSurvivesABSONRoundTrip(t *testing.T) {
	before := hashPack()
	raw, err := bson.Marshal(before)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var after DomainPack
	if err := bson.Unmarshal(raw, &after); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := RiskConfigHash(&after), RiskConfigHash(before); got != want {
		t.Errorf("hash changed across a BSON round trip:\n before %s\n after  %s", want, got)
	}
}

// TestRiskConfigHashDistinguishesAnAreaIDUnderTwoCategories -- an id is not
// unique across a pack (the shipped system-test pack has one area id under two
// categories), and those are two areas with two configurations.
func TestRiskConfigHashDistinguishesAnAreaIDUnderTwoCategories(t *testing.T) {
	pack := hashPack()
	pack.AnalysisAreas.Categories["sellers"] = []PackAnalysisArea{
		{ID: "repeat", Name: "Repeat", Prompt: "p4", RiskCategories: []string{"revenue_leak"}},
	}
	withBoth := RiskConfigHash(pack)

	// Changing only the second one's mapping has to move the fingerprint.
	pack.AnalysisAreas.Categories["sellers"][0].RiskCategories = []string{"customer_churn"}
	if RiskConfigHash(pack) == withBoth {
		t.Error("two areas sharing an id were collapsed, so an edit to one of them is invisible")
	}
}

// TestRiskConfigHashOnAPackWithNoConfiguration -- a pack from before this
// existed must hash to something stable, not to the empty string, or a filter
// built from it cannot tell "no configuration" from "no pack".
func TestRiskConfigHashOnAPackWithNoConfiguration(t *testing.T) {
	bare := &DomainPack{Slug: "legacy"}
	if got := RiskConfigHash(bare); got == "" {
		t.Error("a pack with no risk configuration hashed to the empty string")
	}
	if RiskConfigHash(bare) != RiskConfigHash(&DomainPack{Slug: "other"}) {
		t.Error("the fingerprint depends on something outside the risk configuration")
	}
	if RiskConfigHash(nil) != "" {
		t.Error("a nil pack should hash to nothing, which is distinguishable from a bare one")
	}
}

// TestRiskConfigHashIsNeverMarshalledToBSON pins the `bson:"-"` on the served
// field.
//
// The fingerprint goes out to clients and this model comes back from them, so
// a value a client holds must not be able to reach storage by riding along in
// a whole-document write. The integration test over the repository cannot
// distinguish this from the repository's own explicit write of the field —
// both produce the right stored value — so the tag is pinned here, where it
// is the only thing in play.
func TestRiskConfigHashIsNeverMarshalledToBSON(t *testing.T) {
	pack := hashPack()
	pack.RiskConfigHash = "deadbeef-from-a-stale-client"

	raw, err := bson.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["risk_config_hash"]; ok {
		t.Error("the served fingerprint is marshalled into the document, so a client's copy of it can be stored")
	}

	// And it does not come back in on a read, either: a document that somehow
	// carries the field must not populate it, or a stale stored value would be
	// served as though it were computed.
	doc["risk_config_hash"] = "stale-stored-value"
	regressed, err := bson.Marshal(doc)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	var back DomainPack
	if err := bson.Unmarshal(regressed, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.RiskConfigHash != "" {
		t.Errorf("decoded fingerprint = %q, want it unset so the read path computes it", back.RiskConfigHash)
	}
}
