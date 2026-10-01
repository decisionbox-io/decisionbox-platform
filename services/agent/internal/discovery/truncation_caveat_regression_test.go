package discovery

import (
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The query, the row count and the true cardinality are the real ones from the
// discovery that shipped "Category (p_type, 15 values)" for a column holding
// 150. Everything the model needed to avoid that claim is asserted here,
// because everything it needed was absent when it wrote it.
const corpusCappedQuery = "SELECT p.p_type, COUNT(*) AS lineitems, " +
	"SUM(l.l_extendedprice*(1-l.l_discount)) AS net_revenue " +
	"FROM lineitem l JOIN part p ON l.l_partkey = p.p_partkey " +
	"GROUP BY p.p_type ORDER BY net_revenue DESC LIMIT 15"

func cappedCorpusStep(t *testing.T) models.ExplorationStep {
	t.Helper()
	rows := make([]map[string]any, 0, 15)
	for i := 0; i < 15; i++ {
		rows = append(rows, map[string]any{
			"p_type":      string(rune('A'+i)) + " ANODIZED STEEL",
			"lineitems":   41435 + i,
			"net_revenue": 1517252384.0 + float64(i)*5_000_000,
		})
	}
	digest := gomodels.BuildCompactResult(rows)
	rowCap, ok := gowarehouse.TrailingLimit(corpusCappedQuery)
	if !ok || rowCap != 15 {
		t.Fatalf("TrailingLimit on the corpus query = (%d, %v), want (15, true)", rowCap, ok)
	}
	return models.ExplorationStep{
		Step:          9,
		Query:         corpusCappedQuery,
		RowCount:      len(rows),
		QueryResult:   rows,
		CompactResult: &digest,
		Quality:       []gowarehouse.QualityCaveat{gowarehouse.RowCapCaveat(rowCap)},
	}
}

func TestCappedStepRendersTruncationCaveatAndScopedDistinct(t *testing.T) {
	rendered := RenderCompactedSteps([]models.ExplorationStep{cappedCorpusStep(t)})

	// The caveat has to reach the prompt. It is the only thing in the rendered
	// step that says the fifteen rows are not the population.
	if !strings.Contains(rendered, string(gowarehouse.QualityTruncated)) {
		t.Error("rendered step carries no truncation caveat")
	}
	if !strings.Contains(rendered, "quality_caveats") {
		t.Error("rendered step has no quality_caveats field")
	}
	if !strings.Contains(rendered, "not the whole population") {
		t.Error("the caveat text does not say the result is not the population")
	}

	// The field that was misread must no longer be readable as a cardinality.
	if strings.Contains(rendered, `"distinct":`) {
		t.Error(`rendered step still exposes a bare "distinct" field`)
	}

	// Fifteen rows are inline, so the interpolated statistics are gone.
	for _, stat := range []string{`"p25":`, `"median":`, `"p75":`} {
		if strings.Contains(rendered, stat) {
			t.Errorf("rendered step kept statistic %s despite carrying all rows inline", stat)
		}
	}
	if !strings.Contains(rendered, `"all_rows":`) {
		t.Error("fifteen rows should travel inline as all_rows")
	}
}

// A result the cap never bound is complete, and must read as complete.
func TestUncappedStepRendersNoTruncationCaveat(t *testing.T) {
	rows := []map[string]any{{"tier": "ECONOMY", "rev": 36052782612.0}, {"tier": "LARGE", "rev": 36620822374.0}}
	digest := gomodels.BuildCompactResult(rows)
	step := models.ExplorationStep{
		Step:          10,
		Query:         "SELECT tier, SUM(rev) AS rev FROM part GROUP BY tier ORDER BY rev DESC",
		RowCount:      len(rows),
		QueryResult:   rows,
		CompactResult: &digest,
	}
	rendered := RenderCompactedSteps([]models.ExplorationStep{step})
	if strings.Contains(rendered, "quality_caveats") {
		t.Error("an uncapped result should carry no caveat")
	}
	if strings.Contains(rendered, string(gowarehouse.QualityTruncated)) {
		t.Error("an uncapped result should not be labelled truncated")
	}
}
