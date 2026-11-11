package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func feedbackTestDiscoveries() []*models.DiscoveryResult {
	return []*models.DiscoveryResult{{
		ID: "disc-1",
		Insights: []models.Insight{
			{ID: "ins-a", Name: "Insight A"},
		},
		// Stored order. The dashboard sorts by priority, so the card shown
		// first is "Fix delivery delays" even though it is stored second.
		Recommendations: []models.Recommendation{
			{ID: "rec-low", Title: "Tweak banner copy", Priority: 3},
			{ID: "rec-high", Title: "Fix delivery delays", Priority: 1},
		},
	}}
}

func TestBuildFeedbackSummaries_RecommendationByID(t *testing.T) {
	entries := []database.FeedbackEntry{
		{DiscoveryID: "disc-1", TargetType: "recommendation", TargetID: "rec-high", Rating: "like", Comment: "yes"},
	}

	got := buildFeedbackSummaries(feedbackTestDiscoveries(), entries)
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	if got[0].InsightName != "Fix delivery delays" {
		t.Errorf("InsightName = %q, want %q", got[0].InsightName, "Fix delivery delays")
	}
	if got[0].Rating != "like" || got[0].Comment != "yes" {
		t.Errorf("rating/comment not carried over: %+v", got[0])
	}
}

func TestBuildFeedbackSummaries_LegacyKeys(t *testing.T) {
	entries := []database.FeedbackEntry{
		{DiscoveryID: "disc-1", TargetType: "recommendation", TargetID: "0", Rating: "dislike"},
		{DiscoveryID: "disc-1", TargetType: "insight", TargetID: "0", Rating: "like"},
		{DiscoveryID: "disc-1", TargetType: "insight", TargetID: "ins-a", Rating: "like"},
	}

	got := buildFeedbackSummaries(feedbackTestDiscoveries(), entries)
	want := []string{"Tweak banner copy", "Insight A", "Insight A"}
	if len(got) != len(want) {
		t.Fatalf("got %d summaries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].InsightName != w {
			t.Errorf("summary %d: InsightName = %q, want %q", i, got[i].InsightName, w)
		}
	}
}

func TestBuildFeedbackSummaries_UnknownTarget(t *testing.T) {
	entries := []database.FeedbackEntry{
		{DiscoveryID: "disc-1", TargetType: "recommendation", TargetID: "missing", Rating: "like"},
	}

	got := buildFeedbackSummaries(feedbackTestDiscoveries(), entries)
	if len(got) != 1 || got[0].InsightName != "recommendation #missing" {
		t.Errorf("got %+v, want fallback name %q", got, "recommendation #missing")
	}
}
