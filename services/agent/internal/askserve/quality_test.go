package askserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

func withheldResult() *queryexec.ExecuteResult {
	return &queryexec.ExecuteResult{
		Data:     []map[string]interface{}{{"channel": "Organic", "sessions": 100}},
		RowCount: 1,
		Quality: []gowarehouse.QualityCaveat{
			{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld"},
		},
	}
}

// The failure this closes: a source can return well-formed rows and report,
// only in metadata, that they are not the whole answer. Ask carried the rows
// and dropped the metadata, so a thresholded result reached the model looking
// exactly like a complete one — and unlike a wrong number, a plausible number
// computed over a population the source declined to show has nothing to smell.
func TestSummarizeResult_CarriesWhatTheSourceSaidAboutTheResult(t *testing.T) {
	sum := summarizeResult(withheldResult(), "traffic by channel", Config{PreviewRows: 10, MaxFetchRows: 100})

	if len(sum.Quality) != 1 || sum.Quality[0].Kind != gowarehouse.QualityWithheld {
		t.Fatalf("quality = %+v, want the source's withheld caveat", sum.Quality)
	}
	if sum.Truncated {
		t.Error("a source caveat was recorded as our own preview truncation; they are different facts")
	}
}

// A caveat the model is not shown is worth nothing. It has to arrive as an
// instruction, in the same words discovery uses — the same caveat on the same
// data must not read as two different severities depending on which path asked.
func TestObservation_TellsTheModelWhatTheResultIsNot(t *testing.T) {
	sum := summarizeResult(withheldResult(), "traffic by channel", Config{PreviewRows: 10, MaxFetchRows: 100})
	obs := sum.observation()

	if !strings.Contains(obs, "37 of 412 rows withheld") {
		t.Errorf("observation does not name what was degraded:\n%s", obs)
	}
	// Verbatim, allowing for the observation's own trailing-newline trim: the
	// wording is shared so that the same caveat cannot read as two different
	// severities depending on which path asked.
	want := strings.TrimRight(gowarehouse.CaveatInstruction(sum.Quality), "\n")
	if !strings.Contains(obs, want) {
		t.Errorf("observation does not carry the shared instruction verbatim:\n%s", obs)
	}
	// And an ordinary result is unchanged, to the character.
	clean := summarizeResult(&queryexec.ExecuteResult{
		Data: []map[string]interface{}{{"a": 1}}, RowCount: 1,
	}, "p", Config{PreviewRows: 10, MaxFetchRows: 100})
	if strings.Contains(clean.observation(), "not a faithful answer") {
		t.Error("a clean result was given a caveat it does not have")
	}
}

// Every kind is a silent degradation, and each one makes a different part of a
// result untrue. None of them may pass as complete.
func TestSummarizeResult_CarriesEveryKind(t *testing.T) {
	for _, kind := range []gowarehouse.QualityKind{
		gowarehouse.QualityWithheld, gowarehouse.QualitySampled,
		gowarehouse.QualityTruncated, gowarehouse.QualityRestricted,
	} {
		t.Run(string(kind), func(t *testing.T) {
			sum := summarizeResult(&queryexec.ExecuteResult{
				Data: []map[string]interface{}{{"a": 1}}, RowCount: 1,
				Quality: []gowarehouse.QualityCaveat{{Kind: kind, Detail: "d"}},
			}, "p", Config{PreviewRows: 10, MaxFetchRows: 100})

			if len(sum.Quality) != 1 {
				t.Fatalf("quality = %+v, want the caveat carried", sum.Quality)
			}
			if !strings.Contains(sum.observation(), string(kind)) {
				t.Errorf("observation does not name the %s caveat", kind)
			}
		})
	}
}

// A chart is the most confident form an answer takes, and the least
// interrogable: a bar chart of rows the source withheld part of is a picture of
// a part presented as the whole, with the caveat nowhere on it.
func TestQualityReasons_ListsEveryCaveat(t *testing.T) {
	got := qualityReasons([]gowarehouse.QualityCaveat{
		{Kind: gowarehouse.QualityWithheld, Detail: "37 rows"},
		{Kind: gowarehouse.QualitySampled},
	})
	for _, want := range []string{"withheld: 37 rows", "sampled"} {
		if !strings.Contains(got, want) {
			t.Errorf("qualityReasons = %q, want it to include %q", got, want)
		}
	}
}

// --- charting a degraded result ---

// A chart is the most confident form an answer takes and the least
// interrogable: a bar chart built on rows the source withheld part of is a
// picture of a part, presented as the whole, with the caveat nowhere on it.
// The turn's prose can carry a caveat; a chart cannot.
func TestExecRenderChart_RefusesADegradedSource(t *testing.T) {
	r := &runner{cfg: chartCfg(), store: &fakeStore{}}
	st := groundedChartState()
	src := st.querySummariesByID["q1"]
	src.Quality = []gowarehouse.QualityCaveat{
		{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld"},
	}
	st.querySummariesByID["q1"] = src

	raw, _ := json.Marshal(validChartInput("q1"))
	obs := r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})

	if st.chartsRendered != 0 {
		t.Fatalf("charted a result the source called degraded: chartsRendered = %d", st.chartsRendered)
	}
	if len(st.events) != 1 || st.events[0].Error == "" {
		t.Fatalf("the refusal must be persisted as an error event: %+v", st.events)
	}
	// The model needs the reason, or it re-queries blindly and is refused again.
	if !strings.Contains(obs, "37 of 412 rows withheld") {
		t.Errorf("observation does not name the caveat:\n%s", obs)
	}
	if !strings.Contains(obs, "prose") {
		t.Errorf("observation does not offer the way out:\n%s", obs)
	}
}

// The same spec against an undegraded result still charts, so the refusal is
// about the source's own statement and not about the spec.
func TestExecRenderChart_StillChartsAnUndegradedSource(t *testing.T) {
	r := &runner{cfg: chartCfg(), store: &fakeStore{}}
	st := groundedChartState()
	raw, _ := json.Marshal(validChartInput("q1"))
	r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})

	if st.chartsRendered != 1 {
		t.Fatalf("chartsRendered = %d, want 1", st.chartsRendered)
	}
}

// render_chart is only OFFERED when the turn has a chartable query, so a turn
// whose only query was degraded must not be offered it at all — being refused
// after asking costs a round on something that was never going to work.
func TestQuerySummary_ADegradedResultIsNotChartable(t *testing.T) {
	cfg := Config{PreviewRows: 10, MaxFetchRows: 100}
	clean := summarizeResult(&queryexec.ExecuteResult{
		Data: []map[string]interface{}{{"a": 1}}, RowCount: 1,
	}, "p", cfg)

	if !clean.chartable() {
		t.Error("a complete result was excluded from charting")
	}
	if summarizeResult(withheldResult(), "p", cfg).chartable() {
		t.Error("a result the source called degraded counted as chartable")
	}
	// Truncation is the other reason, and it is a different one — a caveat is
	// about the population, truncation about the preview.
	truncated := summarizeResult(&queryexec.ExecuteResult{
		Data: []map[string]interface{}{{"a": 1}, {"a": 2}, {"a": 3}}, RowCount: 3,
	}, "p", Config{PreviewRows: 1, MaxFetchRows: 100})
	if truncated.chartable() {
		t.Error("a truncated result counted as chartable")
	}
}
