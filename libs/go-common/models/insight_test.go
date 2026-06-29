package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStandaloneInsightBuildEmbeddingText(t *testing.T) {
	insight := &StandaloneInsight{
		Name:          "High churn at Level 45",
		Description:   "Players are leaving at Level 45 due to steep difficulty curve",
		AnalysisArea:  "churn",
		Severity:      "high",
		TargetSegment: "players reaching level 45",
	}

	text := insight.BuildEmbeddingText()

	expected := []string{
		"High churn at Level 45",
		"Players are leaving at Level 45",
		"Area: churn",
		"Severity: high",
		"Segment: players reaching level 45",
	}
	for _, s := range expected {
		if !strings.Contains(text, s) {
			t.Errorf("expected embedding text to contain %q, got: %s", s, text)
		}
	}
}

func TestStandaloneInsightJSONRoundTrip(t *testing.T) {
	insight := &StandaloneInsight{
		ID:              "550e8400-e29b-41d4-a716-446655440000",
		ProjectID:       "proj-123",
		DiscoveryID:     "disc-456",
		Domain:          "gaming",
		Category:        "match3",
		AnalysisArea:    "churn",
		Name:            "High churn at Level 45",
		Description:     "Players leaving",
		DescriptionMd:   "**Players leaving** at Level 45.",
		Severity:        "high",
		AffectedCount:   12450,
		RiskScore:       8.2,
		Confidence:      0.85,
		Metrics:         map[string]interface{}{"churn_rate": 0.34},
		Indicators:      []string{"steep difficulty curve"},
		EmbeddingText:   "High churn at Level 45...",
		EmbeddingModel:  "text-embedding-3-small",
		DuplicateOf:     "550e8400-e29b-41d4-a716-446655440001",
		SimilarityScore: 0.97,
		DiscoveredAt:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		CreatedAt:       time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(insight)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded StandaloneInsight
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.ID != insight.ID {
		t.Errorf("ID mismatch: %s != %s", decoded.ID, insight.ID)
	}
	if decoded.DuplicateOf != insight.DuplicateOf {
		t.Errorf("DuplicateOf mismatch: %s != %s", decoded.DuplicateOf, insight.DuplicateOf)
	}
	if decoded.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("EmbeddingModel mismatch: %s", decoded.EmbeddingModel)
	}
	if decoded.DescriptionMd != insight.DescriptionMd {
		t.Errorf("DescriptionMd mismatch: %q != %q", decoded.DescriptionMd, insight.DescriptionMd)
	}
}

// TestStandaloneInsightDescriptionMdOmittedWhenEmpty guards the `omitempty`
// tag: an insight with no Markdown rendition (plain/legacy) must not emit a
// `description_md` key, so old documents and API consumers see exactly the
// shape they did before.
func TestStandaloneInsightDescriptionMdOmittedWhenEmpty(t *testing.T) {
	data, err := json.Marshal(&StandaloneInsight{Name: "n", Description: "plain"})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data), "description_md") {
		t.Errorf("expected description_md to be omitted when empty, got: %s", data)
	}
}
