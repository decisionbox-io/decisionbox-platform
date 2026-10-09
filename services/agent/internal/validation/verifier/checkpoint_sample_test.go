package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Row counts on the evidence path.
//
// A resumed run's exploration steps carry a bounded ROW SAMPLE rather than
// the full result — the rows themselves died with the crashed process. That
// makes the length of the slice the verifier holds a different number from
// the size of the result the insight was computed over, and every test here
// exists because deriving one from the other is a confident falsehood: the
// verifier would be told a 50 000-row result returned fifty rows and would
// compute shares of a population fifty times too small, then confirm or
// refute a claim against it.
//
// The live path is unaffected throughout — queryexec sets
// RowCount = len(Data), so the two numbers are equal by construction and
// every assertion below also describes today's behaviour.

func sampleRows(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"id": int64(i), "label": fmt.Sprintf("row-%d", i)})
	}
	return out
}

// liveStep is a step as exploration left it: every row in hand.
func liveStep(n int) *agentmodels.ExplorationStep {
	return &agentmodels.ExplorationStep{
		Step: 7, Query: "SELECT * FROM ds.t", Thinking: "look",
		QueryResult: sampleRows(n), RowCount: n,
	}
}

// checkpointedStep is the same step after a crash and resume: the true count,
// but only the retained sample.
func checkpointedStep(total, retained int) *agentmodels.ExplorationStep {
	return &agentmodels.ExplorationStep{
		Step: 7, Query: "SELECT * FROM ds.t", Thinking: "look",
		QueryResult: sampleRows(retained), RowCount: total,
	}
}

// --- digestStep ------------------------------------------------------------

// TestDigestStep_SampledStepMatchesTheLiveBundle is the regression this
// change exists to prevent, and the one that would otherwise pass every
// other test in the package: a checkpointed step must produce the SAME
// digest a live step produces, because the live bundle was already a
// SampleRows sample of the same result.
func TestDigestStep_SampledStepMatchesTheLiveBundle(t *testing.T) {
	cfg := DefaultBundleConfig()

	live := digestStep(liveStep(50_000), cfg)
	resumed := digestStep(checkpointedStep(50_000, cfg.SampleRows), cfg)

	liveJSON, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	resumedJSON, err := json.Marshal(resumed)
	if err != nil {
		t.Fatal(err)
	}
	if string(liveJSON) != string(resumedJSON) {
		t.Errorf("a resumed step's digest differs from the live one.\nlive:    %s\nresumed: %s", liveJSON, resumedJSON)
	}

	// Spelled out, so a failure says which fact went wrong rather than only
	// that two blobs differ.
	if resumed.FullRowCount != 50_000 {
		t.Errorf("FullRowCount = %d, want 50000 — the size of the RESULT, not of the sample", resumed.FullRowCount)
	}
	if !resumed.Truncated {
		t.Error("Truncated = false for a 50-row sample of a 50000-row step — the bundle must flag that it is partial")
	}
	if len(resumed.SampleRows) != cfg.SampleRows {
		t.Errorf("SampleRows len = %d, want %d", len(resumed.SampleRows), cfg.SampleRows)
	}
}

// TestDigestStep_CarriesNoRetentionFlag pins the decision NOT to put a
// "are there more rows behind this" flag in the digest.
//
// The digest is rendered into the prompt on every run, so a field here would
// change what every live verifier reads in order to describe a state only a
// resumed run can be in — and the bundle is what gates recommendation
// generation. The distinction is reported where it is actionable instead: by
// the read that comes back short. See ReadStepRows' rows_retained.
func TestDigestStep_CarriesNoRetentionFlag(t *testing.T) {
	rendered, err := json.Marshal(digestStep(liveStep(5), DefaultBundleConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "rows_retained") {
		t.Errorf("the digest must not add a retention flag to every run's prompt: %s", rendered)
	}
}

// TestDigestStep_LivePathIsUnchanged is the regression guard for every run
// that is not a resume: short results, exactly-at-cap results and failed
// steps must digest exactly as they always did.
func TestDigestStep_LivePathIsUnchanged(t *testing.T) {
	cfg := DefaultBundleConfig()
	cases := []struct {
		name          string
		step          *agentmodels.ExplorationStep
		wantFull      int
		wantSample    int
		wantTruncated bool
	}{
		{"short result", liveStep(3), 3, 3, false},
		{"exactly at the cap", liveStep(50), 50, 50, false},
		{"one past the cap", liveStep(51), 51, 50, true},
		{"empty result", liveStep(0), 0, 0, false},
		{
			name: "failed step has no rows and no count",
			step: &agentmodels.ExplorationStep{
				Step: 7, Query: "SELECT bad", Error: "Unrecognized name: bad",
			},
			wantFull: 0, wantSample: 0, wantTruncated: false,
		},
		{
			// A historical row written before row_count was persisted
			// alongside the rows. Never under-report what we are holding.
			name: "rows with no row_count fall back to the slice",
			step: &agentmodels.ExplorationStep{
				Step: 7, QueryResult: sampleRows(4), RowCount: 0,
			},
			wantFull: 4, wantSample: 4, wantTruncated: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := digestStep(tc.step, cfg)
			if d.FullRowCount != tc.wantFull {
				t.Errorf("FullRowCount = %d, want %d", d.FullRowCount, tc.wantFull)
			}
			if len(d.SampleRows) != tc.wantSample {
				t.Errorf("SampleRows len = %d, want %d", len(d.SampleRows), tc.wantSample)
			}
			if d.Truncated != tc.wantTruncated {
				t.Errorf("Truncated = %v, want %v", d.Truncated, tc.wantTruncated)
			}
		})
	}
}

// --- CheckpointSample ------------------------------------------------------

// TestCheckpointSample_IsWhatTheBundleWouldHaveShown pins the contract that
// makes the sample sufficient: it is exactly the rows digestStep would have
// put in the bundle, so a resumed verifier is shown no less evidence than a
// live one.
func TestCheckpointSample_IsWhatTheBundleWouldHaveShown(t *testing.T) {
	cfg := DefaultBundleConfig()
	full := sampleRows(50_000)

	sample := CheckpointSample(full, cfg)
	if len(sample) != cfg.SampleRows {
		t.Fatalf("sample len = %d, want %d", len(sample), cfg.SampleRows)
	}

	fromFull := digestStep(&agentmodels.ExplorationStep{QueryResult: full, RowCount: len(full)}, cfg)
	fromSample := digestStep(&agentmodels.ExplorationStep{QueryResult: sample, RowCount: len(full)}, cfg)

	a, _ := json.Marshal(fromFull.SampleRows)
	b, _ := json.Marshal(fromSample.SampleRows)
	if string(a) != string(b) {
		t.Errorf("the retained sample is not the rows the bundle would have shown.\nfull:   %s\nsample: %s", a, b)
	}
}

// TestCheckpointSample_NormalisationIsIdempotent is what lets the sample be
// normalised once at checkpoint time and again on the way into the bundle
// without the two disagreeing. If it were not idempotent, "the resumed
// bundle equals the live bundle" would be false for every wide cell.
func TestCheckpointSample_NormalisationIsIdempotent(t *testing.T) {
	cfg := BundleConfig{SampleRows: 10, CellCharCap: 20}
	raw := []map[string]any{{
		"long":   strings.Repeat("x", 500),
		"nested": map[string]any{"a": 1, "b": strings.Repeat("y", 500)},
		"list":   []any{1, 2, 3},
		"bq":     map[string]any{"low": 42, "high": 0, "unsigned": false},
		"plain":  int64(7),
		"nil":    nil,
	}}

	once := CheckpointSample(raw, cfg)
	twice := CheckpointSample(once, cfg)

	a, _ := json.Marshal(once)
	b, _ := json.Marshal(twice)
	if string(a) != string(b) {
		t.Errorf("normalising a normalised sample changed it.\nonce:  %s\ntwice: %s", a, b)
	}
}

// TestCheckpointSample_EdgeCases covers the shapes a step can legitimately
// have: no rows at all, and fewer rows than the cap.
func TestCheckpointSample_EdgeCases(t *testing.T) {
	cfg := DefaultBundleConfig()
	if got := CheckpointSample(nil, cfg); got != nil {
		t.Errorf("CheckpointSample(nil) = %v, want nil", got)
	}
	if got := CheckpointSample([]map[string]any{}, cfg); got != nil {
		t.Errorf("CheckpointSample(empty) = %v, want nil", got)
	}
	if got := CheckpointSample(sampleRows(3), cfg); len(got) != 3 {
		t.Errorf("a 3-row result must be retained whole, got %d rows", len(got))
	}
	// A zero-value config (tests construct these inline) must still bound
	// the sample rather than keeping everything.
	if got := CheckpointSample(sampleRows(500), BundleConfig{}); len(got) != 50 {
		t.Errorf("a zero-value config must fall back to the 50-row default, got %d", len(got))
	}
}

// TestCheckpointSample_BoundsCellsEvenWithAZeroConfig is the case a run with
// validation DISABLED takes: it never loads a verifier config, so the sample
// is cut with a zero BundleConfig. A zero CellCharCap means "do not cap" to
// normaliseRow, which would leave fifty rows of wide text or JSON cells
// unbounded and could put the checkpoint document over Mongo's 16MB limit —
// and a step that cannot be written is a step that cannot be resumed.
func TestCheckpointSample_BoundsCellsEvenWithAZeroConfig(t *testing.T) {
	wide := []map[string]any{{
		"blob":   strings.Repeat("x", 100_000),
		"nested": map[string]any{"inner": strings.Repeat("y", 100_000)},
	}}

	got := CheckpointSample(wide, BundleConfig{})
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	want := DefaultBundleConfig().CellCharCap
	for col, v := range got[0] {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("column %q came back as %T, want a string", col, v)
		}
		// capCell appends an ellipsis, so the cap plus one rune.
		if n := len([]rune(s)); n > want+1 {
			t.Errorf("column %q is %d runes, want it capped at %d", col, n, want)
		}
	}

	// An explicit cap is still honoured over the default.
	got = CheckpointSample(wide, BundleConfig{SampleRows: 5, CellCharCap: 10})
	if n := len([]rune(got[0]["blob"].(string))); n > 11 {
		t.Errorf("an explicit cell cap must win: %d runes, want 10", n)
	}
}

// --- ReadStepRows ----------------------------------------------------------

// TestReadStepRows_DoesNotPanicPastTheRetainedSample is the slice-bounds
// trap. ReadStepRows uses one number twice — as the count it reports and as
// the bound on the slice it cuts — and they are only the same number on the
// live path. Taking the reported count from RowCount without separating the
// two computes end = 200 against a 50-element slice and takes the whole
// validation phase down on the first deep read of a resumed run.
func TestReadStepRows_DoesNotPanicPastTheRetainedSample(t *testing.T) {
	e := &DefaultExecutor{
		StepByID:            map[int]*agentmodels.ExplorationStep{7: checkpointedStep(50_000, 50)},
		Cfg:                 DefaultBundleConfig(),
		MaxReadStepRowsCall: 200,
	}

	res, err := e.ReadStepRows(context.Background(), StepRowsRequest{StepID: 7, Offset: 0, Limit: 200})
	if err != nil {
		t.Fatalf("ReadStepRows: %v", err)
	}
	if got := res["row_count"]; got != 50_000 {
		t.Errorf("row_count = %v, want 50000 — the size of the result", got)
	}
	gotRows, ok := res["rows"].([]map[string]any)
	if !ok {
		t.Fatalf("rows has type %T", res["rows"])
	}
	if len(gotRows) != 50 {
		t.Errorf("rows len = %d, want the 50 rows we actually hold", len(gotRows))
	}
	if res["truncated"] != true {
		t.Error("truncated must be true — there are 49950 rows we cannot serve")
	}
	if res["rows_retained"] != false {
		t.Error("rows_retained must be false so the trace shows WHY the read was short")
	}
}

// TestReadStepRows_OffsetPastTheSampleIsUnverifiableNotEmpty pins the
// graceful-degradation contract. A page past the retained sample answers
// empty + truncated, which the verifier prompt turns into `unverifiable` —
// deliberately NOT a tool error (noisier) and deliberately not a silent
// empty result (which reads as "no such rows" and can refute a sound claim).
func TestReadStepRows_OffsetPastTheSampleIsUnverifiableNotEmpty(t *testing.T) {
	e := &DefaultExecutor{
		StepByID:            map[int]*agentmodels.ExplorationStep{7: checkpointedStep(50_000, 50)},
		Cfg:                 DefaultBundleConfig(),
		MaxReadStepRowsCall: 200,
	}

	res, err := e.ReadStepRows(context.Background(), StepRowsRequest{StepID: 7, Offset: 1_000, Limit: 50})
	if err != nil {
		t.Fatalf("a page past the sample must not be a tool error: %v", err)
	}
	if got := res["row_count"]; got != 50_000 {
		t.Errorf("row_count = %v, want 50000", got)
	}
	if rows, _ := res["rows"].([]map[string]any); len(rows) != 0 {
		t.Errorf("rows len = %d, want 0", len(rows))
	}
	if res["truncated"] != true {
		t.Error("truncated must be true")
	}
	if res["rows_retained"] != false {
		t.Error("rows_retained must be false")
	}
}

// TestReadStepRows_LivePathIsUnchanged is the regression guard: on a step
// that holds all its rows, every page answers exactly as it did before.
func TestReadStepRows_LivePathIsUnchanged(t *testing.T) {
	e := &DefaultExecutor{
		StepByID: map[int]*agentmodels.ExplorationStep{
			7: liveStep(120),
			8: liveStep(5),
			9: {Step: 9, Query: "SELECT bad", Error: "boom"},
		},
		Cfg:                 DefaultBundleConfig(),
		MaxReadStepRowsCall: 200,
	}
	cases := []struct {
		name                 string
		req                  StepRowsRequest
		wantCount, wantRows  int
		wantTrunc, wantKeeps bool
	}{
		{"first page", StepRowsRequest{StepID: 7, Offset: 0, Limit: 50}, 120, 50, true, true},
		{"last partial page", StepRowsRequest{StepID: 7, Offset: 100, Limit: 50}, 120, 20, false, true},
		{"limit over the clamp", StepRowsRequest{StepID: 7, Offset: 0, Limit: 1000}, 120, 120, false, true},
		{"offset past the end", StepRowsRequest{StepID: 7, Offset: 500, Limit: 50}, 120, 0, true, true},
		{"whole small result", StepRowsRequest{StepID: 8, Offset: 0, Limit: 50}, 5, 5, false, true},
		{"failed step", StepRowsRequest{StepID: 9, Offset: 0, Limit: 50}, 0, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := e.ReadStepRows(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("ReadStepRows: %v", err)
			}
			if got := res["row_count"]; got != tc.wantCount {
				t.Errorf("row_count = %v, want %d", got, tc.wantCount)
			}
			rows, _ := res["rows"].([]map[string]any)
			if len(rows) != tc.wantRows {
				t.Errorf("rows len = %d, want %d", len(rows), tc.wantRows)
			}
			if res["truncated"] != tc.wantTrunc {
				t.Errorf("truncated = %v, want %v", res["truncated"], tc.wantTrunc)
			}
			if res["rows_retained"] != tc.wantKeeps {
				t.Errorf("rows_retained = %v, want %v", res["rows_retained"], tc.wantKeeps)
			}
		})
	}
}

// TestReadStepRows_UnknownStepStillErrors pins the one case that IS a tool
// error: a step id that is not in the snapshot at all is a different fault
// from an offset past the rows, and must stay distinguishable.
func TestReadStepRows_UnknownStepStillErrors(t *testing.T) {
	e := &DefaultExecutor{
		StepByID:            map[int]*agentmodels.ExplorationStep{7: liveStep(5)},
		Cfg:                 DefaultBundleConfig(),
		MaxReadStepRowsCall: 200,
	}
	if _, err := e.ReadStepRows(context.Background(), StepRowsRequest{StepID: 999}); err == nil {
		t.Error("an unknown step_id must be a tool error, not an empty result")
	}
}
