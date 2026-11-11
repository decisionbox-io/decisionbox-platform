package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
)

func getScoringEngines(t *testing.T) scoringEnginesResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	ScoringEngines(rec, httptest.NewRequest(http.MethodGet, "/api/v1/scoring/engines", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached copy would explain a document "+
			"with whichever version was deployed when it was cached", got)
	}

	var body struct {
		Data scoringEnginesResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the response: %v\n%s", err, rec.Body.String())
	}
	return body.Data
}

// TestScoringEngines_ServesTheEngineThatScoresDocumentsNow is the contract the
// breakdown's cutoff line rests on.
func TestScoringEngines_ServesTheEngineThatScoresDocumentsNow(t *testing.T) {
	got := getScoringEngines(t)

	if got.CurrentVersion != scoring.EngineVersion {
		t.Errorf("current_version = %d, want %d", got.CurrentVersion, scoring.EngineVersion)
	}
	if len(got.Engines) == 0 {
		t.Fatal("no engines were served; every breakdown would lose its cutoff line")
	}

	var current *scoringEngine
	for i := range got.Engines {
		if got.Engines[i].Version == got.CurrentVersion {
			current = &got.Engines[i]
		}
	}
	if current == nil {
		t.Fatalf("the current version %d is not among the engines served: %+v", got.CurrentVersion, got.Engines)
	}

	// Ascending, which is the order the dashboard shows them in.
	for i := 1; i < len(got.Engines); i++ {
		if got.Engines[i-1].Version >= got.Engines[i].Version {
			t.Errorf("the engines are not ascending by version: %+v", got.Engines)
			break
		}
	}
}

// TestScoringEngines_MatchesTheEngineItDescribes — the whole point of serving
// the calibration is that nobody keeps a second copy of it, so the payload has
// to be derived from the engine rather than transcribed beside it.
func TestScoringEngines_MatchesTheEngineItDescribes(t *testing.T) {
	served := getScoringEngines(t)
	want, ok := scoring.ConfigForVersion(scoring.EngineVersion)
	if !ok {
		t.Fatalf("ConfigForVersion(%d) does not resolve", scoring.EngineVersion)
	}
	wantConf := want.Confidence

	var got scoringConfidenceConfig
	for _, e := range served.Engines {
		if e.Version == scoring.EngineVersion {
			got = e.Confidence
		}
	}

	if got.Cutoffs.High != wantConf.HighAt || got.Cutoffs.Medium != wantConf.MediumAt {
		t.Errorf("cutoffs = %+v, want high=%v medium=%v", got.Cutoffs, wantConf.HighAt, wantConf.MediumAt)
	}
	if got.Floor != wantConf.Floor {
		t.Errorf("floor = %v, want %v", got.Floor, wantConf.Floor)
	}
	if got.Default != wantConf.Default {
		t.Errorf("default = %v, want %v", got.Default, wantConf.Default)
	}

	// Every verdict the engine prices is served under the name a document
	// carries, and nothing else is.
	if len(got.Bases) != len(wantConf.Bases) {
		t.Errorf("bases has %d entries, want %d: %+v", len(got.Bases), len(wantConf.Bases), got.Bases)
	}
	for status, base := range wantConf.Bases {
		if served, ok := got.Bases[string(status)]; !ok || served != base {
			t.Errorf("bases[%q] = %v (present=%v), want %v", status, served, ok, base)
		}
	}
	for _, unpriced := range []valmodels.Status{valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap} {
		if _, ok := got.Bases[string(unpriced)]; ok {
			t.Errorf("bases prices %q; the never-checked statuses take `default`", unpriced)
		}
	}

	// Every dock the engine can charge, with its own label and amount.
	wantDocks := map[string]float64{
		scoring.DockDegradedEvidence:  wantConf.Docks.DegradedEvidence.Amount,
		scoring.DockRefutedArithmetic: wantConf.Docks.RefutedArithmetic.Amount,
		scoring.DockRefutedQuantifier: wantConf.Docks.RefutedQuantifier.Amount,
		scoring.DockUnresolvedFigure:  wantConf.Docks.UnresolvedFigure.Amount,
		scoring.DockSampleEndedEarly:  wantConf.Docks.SampleEndedEarly.Amount,
	}
	if len(got.Docks) != len(wantDocks) {
		t.Errorf("docks has %d entries, want %d: %+v", len(got.Docks), len(wantDocks), got.Docks)
	}
	for _, dock := range got.Docks {
		want, known := wantDocks[dock.Code]
		if !known {
			t.Errorf("an unknown dock code was served: %q", dock.Code)
			continue
		}
		if dock.Max != want {
			t.Errorf("dock %q max = %v, want %v", dock.Code, dock.Max, want)
		}
		if dock.Label == "" {
			t.Errorf("dock %q was served with no label", dock.Code)
		}
		delete(wantDocks, dock.Code)
	}
	for code := range wantDocks {
		t.Errorf("dock %q was not served", code)
	}
}

// TestScoringEngines_TheSharedCapIsVisible — two 0.10 deductions that can only
// total 0.15 is arithmetic a reader of the calibration cannot otherwise
// reconstruct, so the pairing is named rather than implied.
func TestScoringEngines_TheSharedCapIsVisible(t *testing.T) {
	served := getScoringEngines(t)
	byCode := map[string]scoringDock{}
	for _, e := range served.Engines {
		if e.Version != scoring.EngineVersion {
			continue
		}
		for _, d := range e.Confidence.Docks {
			byCode[d.Code] = d
		}
	}

	arith, quant := byCode[scoring.DockRefutedArithmetic], byCode[scoring.DockRefutedQuantifier]
	if arith.SharedCapWith != scoring.DockRefutedQuantifier {
		t.Errorf("%q shared_cap_with = %q, want %q", arith.Code, arith.SharedCapWith, quant.Code)
	}
	if quant.SharedCapWith != scoring.DockRefutedArithmetic {
		t.Errorf("%q shared_cap_with = %q, want %q", quant.Code, quant.SharedCapWith, arith.Code)
	}
	if arith.Cap != quant.Cap || arith.Cap == 0 {
		t.Errorf("the shared cap is not the same on both sides: %v / %v", arith.Cap, quant.Cap)
	}
	if arith.Max+quant.Max <= arith.Cap {
		t.Errorf("the cap (%v) does not bind on the two amounts (%v + %v); this pairing says nothing",
			arith.Cap, arith.Max, quant.Max)
	}

	quality := byCode[scoring.DockDegradedEvidence]
	if quality.Cap == 0 {
		t.Error("the quality dock is served uncapped; it is charged once per caveat kind and must be bounded")
	}
	if quality.SharedCapWith != "" {
		t.Errorf("the quality dock claims to share a cap with %q", quality.SharedCapWith)
	}
	for _, code := range []string{scoring.DockUnresolvedFigure, scoring.DockSampleEndedEarly} {
		if d := byCode[code]; d.Cap != 0 || d.SharedCapWith != "" {
			t.Errorf("dock %q is served capped (%v / %q); it fires at most once", code, d.Cap, d.SharedCapWith)
		}
	}
}
