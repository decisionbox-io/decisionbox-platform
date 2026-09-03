package discovery

import (
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestRenderHistoricalPatterns(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("empty when nothing recurring", func(t *testing.T) {
		out := renderHistoricalPatterns([]models.HistoricalPattern{
			{Name: "seen-once", SeenCount: 1, Status: "active", LastSeen: base},
		})
		if out != "" {
			t.Fatalf("a single-sighting pattern must not render as a trend, got:\n%s", out)
		}
	})

	t.Run("recurring and worsening render, once-seen excluded", func(t *testing.T) {
		out := renderHistoricalPatterns([]models.HistoricalPattern{
			{Name: "recurring-a", AnalysisArea: "churn", SeenCount: 3, Status: "recurring", LastSeen: base},
			{Name: "seen-once", SeenCount: 1, Status: "active", LastSeen: base},
			{Name: "worsening-b", SeenCount: 1, Status: "worsening", LastSeen: base},
		})
		if !strings.Contains(out, "recurring-a") {
			t.Errorf("recurring pattern missing:\n%s", out)
		}
		if !strings.Contains(out, "worsening-b") {
			t.Errorf("worsening pattern (even seen once) should render:\n%s", out)
		}
		if strings.Contains(out, "seen-once") {
			t.Errorf("single-sighting non-worsening pattern must be excluded:\n%s", out)
		}
		if !strings.Contains(out, "seen in 3 runs") {
			t.Errorf("seen-count not narrated:\n%s", out)
		}
	})

	t.Run("caps to the freshest patterns", func(t *testing.T) {
		patterns := make([]models.HistoricalPattern, 0, 20)
		for i := 0; i < 20; i++ {
			patterns = append(patterns, models.HistoricalPattern{
				Name:      "p" + string(rune('a'+i)),
				SeenCount: 2,
				Status:    "recurring",
				LastSeen:  base.Add(time.Duration(i) * time.Hour),
			})
		}
		out := renderHistoricalPatterns(patterns)
		if got := strings.Count(out, "- **"); got != maxHistoricalPatternsInPrompt {
			t.Fatalf("rendered %d patterns, want cap %d", got, maxHistoricalPatternsInPrompt)
		}
		// Freshest (latest LastSeen) must survive the cap; oldest must be dropped.
		if !strings.Contains(out, "p"+string(rune('a'+19))) {
			t.Errorf("freshest pattern should be kept:\n%s", out)
		}
		if strings.Contains(out, "**pa ") || strings.Contains(out, "**pa—") || strings.Contains(out, "**pa*") {
			t.Errorf("oldest pattern should be dropped by the cap:\n%s", out)
		}
	})
}
