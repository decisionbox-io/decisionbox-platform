package models

import (
	"encoding/json"
	"testing"
	"time"
)

// JSON round-trip and field tagging are the contract the dashboard
// build depends on (the enterprise overlay assumes the wire JSON
// matches the struct here). The tests below exercise every nested
// type with both populated and zero values so a future field-name
// rename surfaces in CI rather than at runtime in the renderer.

func TestExecutiveSummary_JSONRoundTrip_PopulatesEveryField(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	want := fullFixture()
	if got.ID != want.ID || got.ProjectID != want.ProjectID || got.DiscoveryID != want.DiscoveryID {
		t.Errorf("identifiers lost: got %+v want %+v", got, want)
	}
	if got.Version != want.Version {
		t.Errorf("Version: got %d want %d", got.Version, want.Version)
	}
	if got.Language != want.Language || got.Model != want.Model || got.Status != want.Status {
		t.Errorf("metadata lost: got %+v want %+v", got, want)
	}
	if !got.GeneratedAt.Equal(want.GeneratedAt) {
		t.Errorf("GeneratedAt: got %v want %v", got.GeneratedAt, want.GeneratedAt)
	}
	// TokensUsed → InputTokens / OutputTokens split.
	if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.DurationMS != want.DurationMS {
		t.Errorf("telemetry lost: got %+v want %+v", got, want)
	}
	if got.Issue != want.Issue {
		t.Errorf("Issue: got %+v want %+v", got.Issue, want.Issue)
	}
	if len(got.CitedInsightIDs) != len(want.CitedInsightIDs) || got.CitedInsightIDs[0] != want.CitedInsightIDs[0] {
		t.Errorf("CitedInsightIDs: got %v want %v", got.CitedInsightIDs, want.CitedInsightIDs)
	}
	if len(got.CitedRecIDs) != len(want.CitedRecIDs) || got.CitedRecIDs[0] != want.CitedRecIDs[0] {
		t.Errorf("CitedRecIDs: got %v want %v", got.CitedRecIDs, want.CitedRecIDs)
	}
}

func TestExecutiveSummary_JSONRoundTrip_LeadSectionFields(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	want := fullFixture()
	if got.Lead.Headline != want.Lead.Headline {
		t.Errorf("Lead.Headline: got %q want %q", got.Lead.Headline, want.Lead.Headline)
	}
	if got.Lead.HeadlineAccent != want.Lead.HeadlineAccent {
		t.Errorf("Lead.HeadlineAccent: got %q want %q", got.Lead.HeadlineAccent, want.Lead.HeadlineAccent)
	}
	if got.Lead.PullQuote != want.Lead.PullQuote {
		t.Errorf("Lead.PullQuote: got %q want %q", got.Lead.PullQuote, want.Lead.PullQuote)
	}
	if len(got.Lead.Article) != 2 {
		t.Fatalf("Lead.Article len = %d, want 2", len(got.Lead.Article))
	}
	if !got.Lead.Article[0].Lede {
		t.Error("Lead.Article[0].Lede lost — expected true")
	}
	if got.Lead.Article[1].Text != want.Lead.Article[1].Text {
		t.Errorf("Lead.Article[1].Text changed")
	}
}

func TestExecutiveSummary_JSONRoundTrip_SidebarCardsAndStats(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	if got.Lead.Sidebar.CriticalCard == nil {
		t.Fatal("CriticalCard dropped")
	}
	if got.Lead.Sidebar.BrandCard == nil {
		t.Fatal("BrandCard dropped")
	}
	if len(got.Lead.Sidebar.CriticalCard.Items) != 1 {
		t.Errorf("CriticalCard.Items: got %d want 1", len(got.Lead.Sidebar.CriticalCard.Items))
	}
	if got.Lead.Sidebar.CriticalCard.Items[0].Bold != "Action required" {
		t.Errorf("CardItem.Bold: got %q want Action required", got.Lead.Sidebar.CriticalCard.Items[0].Bold)
	}
	if len(got.Lead.Sidebar.CriticalCard.Items[0].Citations) != 1 {
		t.Errorf("CardItem.Citations dropped")
	}
	if len(got.Lead.Sidebar.BrandCard.BigStats) != 1 {
		t.Errorf("BrandCard.BigStats: got %d want 1", len(got.Lead.Sidebar.BrandCard.BigStats))
	}
	if got.Lead.Sidebar.BrandCard.BigStats[0].Value != "357K" {
		t.Errorf("BigStat.Value: got %q want 357K", got.Lead.Sidebar.BrandCard.BigStats[0].Value)
	}

	if len(got.Lead.StatRow) != 1 {
		t.Errorf("Lead.StatRow: got %d want 1", len(got.Lead.StatRow))
	}
	if got.Lead.StatRow[0].Severity != "critical" {
		t.Errorf("Stat.Severity: got %q want critical", got.Lead.StatRow[0].Severity)
	}
}

func TestExecutiveSummary_JSONRoundTrip_ThemedSectionAndSubStories(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	if len(got.Sections) != 1 {
		t.Fatalf("Sections: got %d want 1", len(got.Sections))
	}
	sec := got.Sections[0]
	if sec.Slug != "stock" || sec.NavTitle != "Stock" {
		t.Errorf("section identity: got slug=%q nav=%q want stock/Stock", sec.Slug, sec.NavTitle)
	}
	if sec.AnalysisArea != "inventory_health" {
		t.Errorf("AnalysisArea: got %q", sec.AnalysisArea)
	}
	if len(sec.SubStories) != 1 {
		t.Fatalf("SubStories: got %d want 1", len(sec.SubStories))
	}
	if len(sec.SubStories[0].BarList) != 2 {
		t.Fatalf("BarList: got %d want 2", len(sec.SubStories[0].BarList))
	}
	if sec.SubStories[0].BarList[0].BarPercent != 92 {
		t.Errorf("BarPercent: got %d want 92", sec.SubStories[0].BarList[0].BarPercent)
	}
	if sec.SubStories[0].BarList[0].Severity != "critical" {
		t.Errorf("BarItem.Severity: got %q want critical", sec.SubStories[0].BarList[0].Severity)
	}
}

func TestExecutiveSummary_JSONRoundTrip_ActionPlan(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	if len(got.Action.Timeframes) != 1 {
		t.Fatalf("Timeframes: got %d want 1", len(got.Action.Timeframes))
	}
	tf := got.Action.Timeframes[0]
	if tf.Label != "Today" {
		t.Errorf("Label: got %q want Today", tf.Label)
	}
	if len(tf.Items) != 1 {
		t.Fatalf("Items: got %d want 1", len(tf.Items))
	}
	if tf.Items[0].Title != "Pull 24Y inventory from stores" {
		t.Errorf("Item.Title lost")
	}
	if len(tf.Items[0].Citations) != 1 || tf.Items[0].Citations[0].Type != "recommendation" {
		t.Errorf("Item.Citations: got %+v", tf.Items[0].Citations)
	}
}

func TestExecutiveSummary_JSONRoundTrip_Stories(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	if len(got.Lead.Stories) != 1 {
		t.Fatalf("Stories: got %d want 1", len(got.Lead.Stories))
	}
	if got.Lead.Stories[0].RefSlug != "stock" {
		t.Errorf("Stories[0].RefSlug: got %q want stock", got.Lead.Stories[0].RefSlug)
	}
}

func TestExecutiveSummary_JSONRoundTrip_AboutSection(t *testing.T) {
	got := mustRoundTrip(t, fullFixture())
	if got.About.Headline == "" {
		t.Error("About.Headline dropped")
	}
	if len(got.About.Body) != 1 || got.About.Body[0].Text == "" {
		t.Error("About.Body dropped")
	}
}

func TestExecutiveSummary_OmitemptyOnZeroValueDocument(t *testing.T) {
	// A document with only required identifying fields should not serialise
	// hundreds of "field": null lines — keeps Mongo storage and wire payloads
	// small for the "generating" placeholder doc the API writes first.
	doc := ExecutiveSummary{
		ID:          "summary-1",
		ProjectID:   "p-1",
		DiscoveryID: "d-1",
		Version:     1,
		Language:    "English",
		Model:       "claude-opus-4-7",
		GeneratedAt: time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		GeneratedBy: "auto",
		Status:      "generating",
		Issue:       IssueMeta{Title: "X", IssueLabel: "Issue 1", DateLabel: "2026-05-12"},
		Lead:        LeadSection{Headline: "..."},
		Action:      ActionSection{Headline: "..."},
		About:       AboutSection{},
	}
	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	// Verify the typical bloat fields aren't present. `tokens_used` is
	// gone and the new `input_tokens` / `output_tokens` must also omit
	// on a near-empty doc.
	for _, key := range []string{"prompt_version", "tokens_used", "input_tokens", "output_tokens", "duration_ms", "error", "sections", "pull_quote", "cited_insight_ids", "cited_rec_ids", "stat_row", "stories"} {
		if contains(s, `"`+key+`":`) {
			t.Errorf("omitempty broken: field %q present on near-empty document", key)
		}
	}
}

func TestParagraph_LedeFalseIsOmitted(t *testing.T) {
	// JSON output for {Lede: false} should drop the field — saves bytes for
	// the dozens of non-lede paragraphs every report carries.
	p := Paragraph{Text: "hi"}
	blob, _ := json.Marshal(p)
	if contains(string(blob), `"lede"`) {
		t.Errorf("Lede:false should be omitted; got %s", string(blob))
	}

	pLede := Paragraph{Text: "hi", Lede: true}
	blob, _ = json.Marshal(pLede)
	if !contains(string(blob), `"lede":true`) {
		t.Errorf("Lede:true should be present; got %s", string(blob))
	}
}

func TestSidebar_NilCardsOmitted(t *testing.T) {
	s := Sidebar{}
	blob, _ := json.Marshal(s)
	got := string(blob)
	if contains(got, `"critical_card"`) || contains(got, `"brand_card"`) {
		t.Errorf("nil cards should omit: got %s", got)
	}
}

func TestCitation_StableJSONShape(t *testing.T) {
	c := Citation{Type: "insight", ID: "abc-123"}
	blob, _ := json.Marshal(c)
	want := `{"type":"insight","id":"abc-123"}`
	if string(blob) != want {
		t.Errorf("got %s, want %s", string(blob), want)
	}
}

// fullFixture returns an ExecutiveSummary populated in every field so
// the round-trip tests can catch any tag or type change.
func fullFixture() ExecutiveSummary {
	when := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	criticalCard := Card{
		Eyebrow: "This week",
		Items: []CardItem{
			{Bold: "Action required", Sub: "Pull 24Y", Citations: []Citation{{Type: "recommendation", ID: "rec-1"}}},
		},
	}
	brandCard := Card{
		Eyebrow:  "Numbers",
		BigStats: []BigStat{{Value: "357K", Sub: "items in warehouse", Citations: []Citation{{Type: "insight", ID: "ins-2"}}}},
	}
	return ExecutiveSummary{
		ID:            "summary-1",
		ProjectID:     "proj-1",
		DiscoveryID:   "disc-1",
		Version:       2,
		Language:      "Turkish",
		Model:         "claude-opus-4-7",
		PromptVersion: "v1",
		GeneratedAt:  when,
		GeneratedBy:  "user-42",
		InputTokens:  9000,
		OutputTokens: 3345,
		DurationMS:   9876,
		Status:        "ready",
		Issue: IssueMeta{
			Title:               "OXXO Raporu",
			IssueLabel:          "Issue 1 · May 2026",
			DateLabel:           "Tuesday, 12 May 2026",
			ScanCount:           4,
			InsightCount:        220,
			RecommendationCount: 33,
		},
		Lead: LeadSection{
			Kicker:         "Lead story",
			Headline:       "Stores blocked by ₺275M of old stock",
			HeadlineAccent: "₺275M",
			Deck:           "320K items sitting on shelves for 24 months.",
			Article: []Paragraph{
				{Text: "Lead paragraph {{I:ins-1}}.", Lede: true},
				{Text: "Second paragraph references {{R:rec-1}}."},
			},
			PullQuote: "In store: nothing. In warehouse: everything.",
			Sidebar:   Sidebar{CriticalCard: &criticalCard, BrandCard: &brandCard},
			StatRow: []Stat{
				{Value: "₺275M", Label: "Retail value of stuck stock", Severity: "critical", Citations: []Citation{{Type: "insight", ID: "ins-1"}}},
			},
			Stories: []Story{
				{Kicker: "Color · Supply", Headline: "Five winning colors", RefSlug: "stock", RefLabel: "Page 02 · Stock", Citations: []Citation{{Type: "insight", ID: "ins-3"}}},
			},
		},
		Sections: []ThemedSection{
			{
				Slug:         "stock",
				NavTitle:     "Stock",
				AnalysisArea: "inventory_health",
				Headline:     "Stock detail",
				SubStories: []SubStory{
					{
						Headline: "Size bottleneck",
						BarList: []BarItem{
							{Label: "Size 34", Value: "%37,6", Severity: "critical", BarPercent: 92, Citations: []Citation{{Type: "insight", ID: "ins-4"}}},
							{Label: "Size 36", Value: "%35,0", Severity: "warn", BarPercent: 86},
						},
					},
				},
			},
		},
		Action: ActionSection{
			Headline: "The next 30 days",
			Timeframes: []ActionTimeframe{
				{
					Label:    "Today",
					Subtitle: "Decisions needing a sign-off before Friday",
					Items: []ActionItem{
						{Title: "Pull 24Y inventory from stores", Body: "97K units, 1526-day cover", Citations: []Citation{{Type: "recommendation", ID: "rec-1"}}},
					},
				},
			},
		},
		About: AboutSection{
			Headline: "What DecisionBox did",
			Body:     []Paragraph{{Text: "Four scans across April-May produced 220 insights."}},
		},
		CitedInsightIDs: []string{"ins-1", "ins-2", "ins-3", "ins-4"},
		CitedRecIDs:     []string{"rec-1"},
	}
}

func mustRoundTrip(t *testing.T, in ExecutiveSummary) ExecutiveSummary {
	t.Helper()
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ExecutiveSummary
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
