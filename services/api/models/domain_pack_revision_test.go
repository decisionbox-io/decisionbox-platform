package models

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func revTestPack() *DomainPack {
	return &DomainPack{
		Slug: "retail", Name: "Retail", Version: "1.0.0",
		Categories: []PackCategory{{ID: "general", Name: "General"}},
		Prompts: PackPrompts{Base: BasePrompts{
			BaseContext: "ctx", Exploration: "explore", Recommendations: "recommend",
		}},
		AnalysisAreas: PackAnalysisAreas{
			Base: []PackAnalysisArea{
				{ID: "stock-cover", Name: "Stock cover", Priority: 2, Keywords: []string{"stock"}, Prompt: "p1"},
				{ID: "aging", Name: "Aging", Priority: 3, Keywords: []string{"aging"}, Prompt: "p2"},
			},
			Categories: map[string][]PackAnalysisArea{
				"general": {{ID: "margin", Name: "Margin", Priority: 1, Prompt: "p3"}},
			},
		},
	}
}

func TestChangedAreaIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*DomainPack)
		want []string
	}{
		{"nothing changed", func(*DomainPack) {}, nil},
		{"one base area edited", func(p *DomainPack) { p.AnalysisAreas.Base[0].Prompt = "rewritten" }, []string{"stock-cover"}},
		{"priority only", func(p *DomainPack) { p.AnalysisAreas.Base[1].Priority = 5 }, []string{"aging"}},
		{"keywords only", func(p *DomainPack) { p.AnalysisAreas.Base[1].Keywords = []string{"aging", "slow"} }, []string{"aging"}},
		{"area added", func(p *DomainPack) {
			p.AnalysisAreas.Base = append(p.AnalysisAreas.Base, PackAnalysisArea{ID: "new-area", Name: "New"})
		}, []string{"new-area"}},
		{"area removed", func(p *DomainPack) { p.AnalysisAreas.Base = p.AnalysisAreas.Base[:1] }, []string{"aging"}},
		{"category area edited", func(p *DomainPack) {
			p.AnalysisAreas.Categories["general"][0].Prompt = "x"
		}, []string{"margin"}},
		{"merge: one rewritten, one gone", func(p *DomainPack) {
			p.AnalysisAreas.Base[0].Prompt = "covers both"
			p.AnalysisAreas.Base = p.AnalysisAreas.Base[:1]
		}, []string{"aging", "stock-cover"}},
		// A prompt edit outside the areas is not an area change; the summary
		// falls back to "edited the Playbook" rather than naming an area that
		// did not move.
		{"base prompt only", func(p *DomainPack) { p.Prompts.Base.Exploration = "different" }, nil},
	} {
		before := revTestPack()
		after := revTestPack()
		tc.edit(after)
		if got := ChangedAreaIDs(before, after); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ChangedAreaIDs = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPackContentEqual(t *testing.T) {
	a, b := revTestPack(), revTestPack()
	// Identity, timestamps, token counts and the log itself are not content:
	// a save that touched none of the pack must not record a revision.
	b.ID = "abc123"
	b.CreatedAt = time.Now()
	b.UpdatedAt = time.Now().Add(time.Hour)
	b.InputTokens, b.OutputTokens = 10, 20
	b.Revision = 7
	b.Revisions = []PackRevision{{Revision: 7, Origin: PackRevisionOriginUser}}
	if !PackContentEqual(a, b) {
		t.Error("packs differing only in identity/timestamps/tokens/log reported as different content")
	}

	for _, tc := range []struct {
		name string
		edit func(*DomainPack)
	}{
		{"name", func(p *DomainPack) { p.Name = "Retail v2" }},
		{"an area prompt", func(p *DomainPack) { p.AnalysisAreas.Base[0].Prompt = "x" }},
		{"a base prompt", func(p *DomainPack) { p.Prompts.Base.Exploration = "x" }},
		{"categories", func(p *DomainPack) { p.Categories = append(p.Categories, PackCategory{ID: "c2", Name: "C2"}) }},
		{"published flag", func(p *DomainPack) { p.IsPublished = !p.IsPublished }},
		{"profile schema", func(p *DomainPack) {
			p.ProfileSchema.Base = map[string]interface{}{"type": "object"}
		}},
	} {
		changed := revTestPack()
		tc.edit(changed)
		if PackContentEqual(revTestPack(), changed) {
			t.Errorf("a change to %s reported as no content change", tc.name)
		}
	}

	if !PackContentEqual(nil, nil) {
		t.Error("nil == nil should be equal")
	}
	if PackContentEqual(nil, revTestPack()) {
		t.Error("nil vs a pack should differ")
	}
}

func TestPackAreasByID(t *testing.T) {
	p := revTestPack()
	got := PackAreasByID(p, []string{"aging", "margin", "no-such-area"})
	if len(got) != 2 || got[0].ID != "aging" || got[1].ID != "margin" {
		t.Fatalf("PackAreasByID = %+v, want aging then margin, unknown ids skipped", got)
	}
	if PackAreasByID(p, nil) != nil {
		t.Error("no ids should snapshot nothing")
	}
	if PackAreasByID(p, []string{"gone"}) != nil {
		t.Error("only-unknown ids should snapshot nothing, not an empty slice")
	}
	if PackAreasByID(nil, []string{"aging"}) != nil {
		t.Error("nil pack should snapshot nothing")
	}
}

// Every pack that exists today has neither field. They must stay absent on
// disk and out of the JSON, so a pack with no history reads as unknown rather
// than as a claimed-empty one — and so Export's portable format is unchanged
// for a pack that was never edited.
func TestDomainPack_RevisionFieldsOmittedWhenZero(t *testing.T) {
	bs, err := bson.Marshal(revTestPack())
	if err != nil {
		t.Fatalf("bson.Marshal: %v", err)
	}
	var raw bson.M
	if err := bson.Unmarshal(bs, &raw); err != nil {
		t.Fatalf("bson.Unmarshal: %v", err)
	}
	for _, k := range []string{"revision", "revisions"} {
		if _, present := raw[k]; present {
			t.Errorf("%s present in BSON for a pack with no history", k)
		}
	}

	js, err := json.Marshal(revTestPack())
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var jraw map[string]any
	if err := json.Unmarshal(js, &jraw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, k := range []string{"revision", "revisions"} {
		if _, present := jraw[k]; present {
			t.Errorf("%s present in JSON for a pack with no history", k)
		}
	}

	// And a pack that has a history reports it.
	p := revTestPack()
	p.Revision = 3
	p.Revisions = []PackRevision{{Revision: 3, Origin: PackRevisionOriginAskSession, SessionID: "s1", ProposalID: "pr1"}}
	js, _ = json.Marshal(p)
	if err := json.Unmarshal(js, &jraw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if jraw["revision"] != float64(3) {
		t.Errorf("revision = %v, want 3", jraw["revision"])
	}
	if entries, _ := jraw["revisions"].([]any); len(entries) != 1 {
		t.Errorf("revisions = %v, want one entry", jraw["revisions"])
	}
}
