package charts

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

//go:embed schema.json
var schemaJSON []byte

// f64 is a small helper for building *float64 test values.
func f64(v float64) *float64 { return &v }

func boolp(v bool) *bool { return &v }

// source is a convenient grounding source: two-column monthly revenue.
func revenueSource() GroundingSource {
	return GroundingSource{
		StepID:  "q2",
		Columns: []string{"month", "revenue", "cost"},
		Preview: []map[string]any{
			{"month": "2024-01", "revenue": int64(100), "cost": int64(40)},
			{"month": "2024-02", "revenue": int64(150), "cost": int64(55)},
			{"month": "2024-03", "revenue": int64(210), "cost": int64(70)},
		},
	}
}

func barSpec() ChartSpec {
	return ChartSpec{
		Type:         ChartBar,
		Title:        "Revenue by month",
		X:            &Axis{Field: "month", Type: AxisCategory},
		Y:            []Series{{Field: "revenue"}},
		SourceStepID: "q2",
		Data: []map[string]any{
			{"month": "2024-01", "revenue": 100.0},
			{"month": "2024-02", "revenue": 150.0},
			{"month": "2024-03", "revenue": 210.0},
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	for _, spec := range []ChartSpec{
		barSpec(),
		func() ChartSpec { s := barSpec(); s.Type = ChartLine; return s }(),
		func() ChartSpec { s := barSpec(); s.Type = ChartArea; s.Stacked = boolp(true); return s }(),
		func() ChartSpec { s := barSpec(); s.Type = ChartScatter; return s }(),
		{
			Type: ChartKPI, Title: "Total", SourceStepID: "q2",
			KPI: &KPI{Value: 460, Unit: "USD", ValueField: "revenue"},
		},
	} {
		if err := Validate(spec, DefaultCaps); err != nil {
			t.Errorf("Validate(%s) = %v, want nil", spec.Type, err)
		}
	}
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name string
		spec ChartSpec
		rule string
	}{
		{"unknown type", func() ChartSpec { s := barSpec(); s.Type = "radar"; return s }(), "type"},
		{"missing source_step_id", func() ChartSpec { s := barSpec(); s.SourceStepID = ""; return s }(), "grounding"},
		{"no x", func() ChartSpec { s := barSpec(); s.X = nil; return s }(), "shape"},
		{"no y", func() ChartSpec { s := barSpec(); s.Y = nil; return s }(), "shape"},
		{"empty data", func() ChartSpec { s := barSpec(); s.Data = nil; return s }(), "shape"},
		{"stacked on line", func() ChartSpec { s := barSpec(); s.Type = ChartLine; s.Stacked = boolp(true); return s }(), "shape"},
		{"series_by on pie", func() ChartSpec { s := barSpec(); s.Type = ChartPie; s.SeriesBy = "region"; return s }(), "shape"},
		{"y field missing from data", func() ChartSpec {
			s := barSpec()
			s.Y = []Series{{Field: "profit"}}
			return s
		}(), "field_ref"},
		{"non-numeric y", func() ChartSpec {
			s := barSpec()
			s.Data = []map[string]any{{"month": "2024-01", "revenue": "lots"}}
			return s
		}(), "shape"},
		{"html in title", func() ChartSpec { s := barSpec(); s.Title = "<script>x</script>"; return s }(), "sanitize"},
		{"javascript in caption", func() ChartSpec { s := barSpec(); s.Caption = "javascript:alert(1)"; return s }(), "sanitize"},
		{"kpi without kpi obj", ChartSpec{Type: ChartKPI, SourceStepID: "q2"}, "shape"},
		{"kpi with x", ChartSpec{Type: ChartKPI, SourceStepID: "q2", X: &Axis{Field: "m"}, KPI: &KPI{ValueField: "revenue"}}, "shape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.spec, DefaultCaps)
			if err == nil {
				t.Fatalf("Validate = nil, want rejection")
			}
			var ve *Error
			if !errors.As(err, &ve) {
				t.Fatalf("error type = %T, want *charts.Error", err)
			}
			if ve.Rule != c.rule {
				t.Errorf("rule = %q, want %q (%v)", ve.Rule, c.rule, err)
			}
		})
	}
}

func TestValidate_Caps(t *testing.T) {
	caps := Caps{MaxPoints: 2, MaxSeries: 1, MaxLabelLen: 10}
	s := barSpec()
	if err := Validate(s, caps); err == nil {
		t.Error("3 points over MaxPoints=2 should reject")
	}
	s2 := barSpec()
	s2.Data = s2.Data[:2]
	s2.Y = []Series{{Field: "revenue"}, {Field: "cost"}}
	s2.Data = []map[string]any{{"month": "a", "revenue": 1.0, "cost": 2.0}, {"month": "b", "revenue": 3.0, "cost": 4.0}}
	if err := Validate(s2, caps); err == nil {
		t.Error("2 series over MaxSeries=1 should reject")
	}
	s3 := barSpec()
	s3.Data = s3.Data[:2]
	s3.Title = "this title is definitely longer than ten characters"
	if err := Validate(s3, caps); err == nil {
		t.Error("over-long label should reject")
	}
}

func TestDecode_RejectsUnknownFieldAndOversize(t *testing.T) {
	// Unknown field -> declarative-only contract violation.
	raw := []byte(`{"type":"bar","source_step_id":"q2","x":{"field":"month"},"y":[{"field":"revenue"}],"data":[{"month":"a","revenue":1}],"onClick":"evil()"}`)
	if _, err := Decode(raw, DefaultCaps); err == nil {
		t.Error("Decode should reject an unknown field")
	}
	// Over byte cap.
	if _, err := Decode(raw, Caps{MaxSpecBytes: 10}); err == nil {
		t.Error("Decode should reject an oversize spec")
	}
	// Valid decode.
	good := []byte(`{"type":"bar","source_step_id":"q2","x":{"field":"month"},"y":[{"field":"revenue"}],"data":[{"month":"a","revenue":1}]}`)
	spec, err := Decode(good, DefaultCaps)
	if err != nil {
		t.Fatalf("Decode(valid) = %v", err)
	}
	if spec.Type != ChartBar {
		t.Errorf("type = %q, want bar", spec.Type)
	}
}

func TestValidateGrounded_ExactProjection(t *testing.T) {
	src := revenueSource()
	if err := ValidateGrounded(barSpec(), src, DefaultCaps); err != nil {
		t.Fatalf("grounded valid bar = %v", err)
	}

	// A reordered/subset projection is still grounded.
	s := barSpec()
	s.Data = []map[string]any{
		{"month": "2024-03", "revenue": 210.0},
		{"month": "2024-01", "revenue": 100.0},
	}
	if err := ValidateGrounded(s, src, DefaultCaps); err != nil {
		t.Errorf("reordered subset should ground: %v", err)
	}
}

func TestValidateGrounded_RejectsDerivedNumbers(t *testing.T) {
	src := revenueSource()
	s := barSpec()
	// The model "helpfully" scaled revenue to thousands — a derived number.
	s.Data = []map[string]any{{"month": "2024-01", "revenue": 0.1}}
	err := ValidateGrounded(s, src, DefaultCaps)
	if err == nil {
		t.Fatal("derived number should be rejected")
	}
	var ve *Error
	if errors.As(err, &ve) && ve.Rule != "grounding" {
		t.Errorf("rule = %q, want grounding", ve.Rule)
	}
}

func TestValidateGrounded_RejectsStitchedRow(t *testing.T) {
	src := revenueSource()
	s := barSpec()
	// month from row 1, revenue from row 2 — a Frankenstein row.
	s.Data = []map[string]any{{"month": "2024-01", "revenue": 150.0}}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("row stitched from two preview rows should be rejected")
	}
}

func TestValidateGrounded_RejectsTruncatedSource(t *testing.T) {
	src := revenueSource()
	src.Truncated = true
	if err := ValidateGrounded(barSpec(), src, DefaultCaps); err == nil {
		t.Error("truncated source must not ground a chart")
	}
}

func TestValidateGrounded_RejectsUnknownColumn(t *testing.T) {
	src := revenueSource()
	s := barSpec()
	s.Y = []Series{{Field: "revenue"}}
	s.X = &Axis{Field: "quarter"} // not a source column
	s.Data = []map[string]any{{"quarter": "Q1", "revenue": 100.0}}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("charting a field the query never returned should be rejected")
	}
}

func TestValidateGrounded_RejectsInventedExtraKey(t *testing.T) {
	src := revenueSource()
	s := barSpec()
	// A real month/revenue projection with an extra invented column smuggled in.
	s.Data = []map[string]any{{"month": "2024-01", "revenue": 100.0, "invented": 999.0}}
	err := ValidateGrounded(s, src, DefaultCaps)
	if err == nil {
		t.Fatal("an invented extra data key must be rejected")
	}
	var ve *Error
	if errors.As(err, &ve) && ve.Rule != "grounding" {
		t.Errorf("rule = %q, want grounding", ve.Rule)
	}
}

func TestValidateGrounded_RejectsAlteredNonPlottedColumn(t *testing.T) {
	src := revenueSource()
	s := barSpec()
	// cost is a real source column but its value here is altered — full-row
	// projection must catch it even though cost is not plotted.
	s.Data = []map[string]any{{"month": "2024-01", "revenue": 100.0, "cost": 999.0}}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("an altered value in a non-plotted source column must be rejected")
	}
}

func TestValidate_KPIDeltaRequiresDeltaField(t *testing.T) {
	s := ChartSpec{Type: ChartKPI, SourceStepID: "q1", KPI: &KPI{Value: 460, ValueField: "total", Delta: f64(60)}}
	err := Validate(s, DefaultCaps)
	if err == nil {
		t.Fatal("a KPI delta without delta_field must be rejected")
	}
	var ve *Error
	if errors.As(err, &ve) && ve.Field != "kpi.delta_field" {
		t.Errorf("field = %q, want kpi.delta_field", ve.Field)
	}
}

func TestValidate_SeriesByFanOutCapped(t *testing.T) {
	caps := Caps{MaxPoints: 100, MaxSeries: 3, MaxLabelLen: 200}
	s := ChartSpec{
		Type: ChartBar, SourceStepID: "q1",
		X: &Axis{Field: "month"}, Y: []Series{{Field: "revenue"}}, SeriesBy: "region",
		Data: []map[string]any{
			{"month": "2024-01", "region": "NA", "revenue": 1.0},
			{"month": "2024-01", "region": "EU", "revenue": 2.0},
			{"month": "2024-01", "region": "APAC", "revenue": 3.0},
			{"month": "2024-01", "region": "LATAM", "revenue": 4.0},
		},
	}
	// 4 distinct regions × 1 measure = 4 rendered series > MaxSeries 3.
	if err := Validate(s, caps); err == nil {
		t.Error("high-cardinality series_by should exceed MaxSeries")
	}
	// Under the cap it passes.
	s.Data = s.Data[:2]
	if err := Validate(s, caps); err != nil {
		t.Errorf("2 series under the cap should pass: %v", err)
	}
}

func TestValidateGrounded_ExactNumbersNoLargeTolerance(t *testing.T) {
	src := GroundingSource{
		StepID:  "q1",
		Columns: []string{"label", "amount"},
		Preview: []map[string]any{{"label": "total", "amount": int64(1_000_000_000_000)}},
	}
	s := ChartSpec{
		Type: ChartBar, SourceStepID: "q1",
		X: &Axis{Field: "label"}, Y: []Series{{Field: "amount"}},
		// A trillion charted as a trillion+1000 — a relative epsilon would have
		// let this pass; exact equality rejects it.
		Data: []map[string]any{{"label": "total", "amount": 1_000_000_001_000.0}},
	}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("a large number altered by ~1000 must not ground")
	}
	// The exact value grounds.
	s.Data = []map[string]any{{"label": "total", "amount": 1_000_000_000_000.0}}
	if err := ValidateGrounded(s, src, DefaultCaps); err != nil {
		t.Errorf("the exact source value should ground: %v", err)
	}
}

func TestValidate_KPIRejectsDataArray(t *testing.T) {
	// A KPI must not carry a data array — the KPI grounding path never inspects
	// it, so it would be an ungrounded, uncapped smuggling channel.
	s := ChartSpec{
		Type: ChartKPI, SourceStepID: "q1",
		KPI:  &KPI{Value: 460, ValueField: "total"},
		Data: []map[string]any{{"x": "a", "invented": 999.0}},
	}
	err := Validate(s, DefaultCaps)
	if err == nil {
		t.Fatal("a kpi carrying a data array must be rejected")
	}
	var ve *Error
	if errors.As(err, &ve) && ve.Field != "data" {
		t.Errorf("field = %q, want data", ve.Field)
	}
}

func TestValidateGrounded_RejectsDuplicatedSourceRow(t *testing.T) {
	// The source has 3 distinct rows; repeating one of them 4× must be rejected —
	// the chart data is a sub-multiset of the preview, not an unbounded subset.
	src := revenueSource()
	s := barSpec()
	s.Data = []map[string]any{
		{"month": "2024-01", "revenue": 100.0},
		{"month": "2024-01", "revenue": 100.0},
	}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("repeating a single source row must be rejected (row multiplicity)")
	}
	// Each distinct source row once is fine.
	s.Data = []map[string]any{
		{"month": "2024-01", "revenue": 100.0},
		{"month": "2024-02", "revenue": 150.0},
	}
	if err := ValidateGrounded(s, src, DefaultCaps); err != nil {
		t.Errorf("distinct source rows should ground: %v", err)
	}
}

func TestValidateGrounded_RejectsBeyondFloat64Precision(t *testing.T) {
	// A value above 2^53 can't survive float64 JSON decoding without possible
	// rounding, so it can't be proven an exact projection — reject it.
	src := GroundingSource{
		StepID:  "q1",
		Columns: []string{"label", "amount"},
		Preview: []map[string]any{{"label": "x", "amount": int64(9007199254740993)}},
	}
	s := ChartSpec{
		Type: ChartBar, SourceStepID: "q1",
		X: &Axis{Field: "label"}, Y: []Series{{Field: "amount"}},
		Data: []map[string]any{{"label": "x", "amount": 9007199254740993.0}},
	}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("a value beyond float64's exact-integer range must be rejected")
	}
}

func TestValidateGrounded_ExactDecimalMatches(t *testing.T) {
	src := GroundingSource{
		StepID:  "q1",
		Columns: []string{"label", "rate"},
		Preview: []map[string]any{{"label": "a", "rate": 33.5}, {"label": "b", "rate": 12.25}},
	}
	s := ChartSpec{
		Type: ChartLine, SourceStepID: "q1",
		X: &Axis{Field: "label"}, Y: []Series{{Field: "rate"}},
		Data: []map[string]any{{"label": "a", "rate": 33.5}, {"label": "b", "rate": 12.25}},
	}
	if err := ValidateGrounded(s, src, DefaultCaps); err != nil {
		t.Errorf("exact decimals should ground: %v", err)
	}
	// An altered decimal is rejected.
	s.Data[0]["rate"] = 33.6
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("an altered decimal must be rejected")
	}
}

func TestValidateGrounded_KPIRejectsBeyondFloat64Precision(t *testing.T) {
	src := GroundingSource{
		StepID:  "q1",
		Columns: []string{"total"},
		Preview: []map[string]any{{"total": int64(9007199254740993)}},
	}
	s := ChartSpec{Type: ChartKPI, SourceStepID: "q1", KPI: &KPI{Value: 9007199254740993, ValueField: "total"}}
	if err := ValidateGrounded(s, src, DefaultCaps); err == nil {
		t.Error("a KPI figure beyond float64's exact-integer range must be rejected")
	}
}

func TestValidateGrounded_KPIProvenance(t *testing.T) {
	src := GroundingSource{
		StepID:  "q1",
		Columns: []string{"total", "prev"},
		Preview: []map[string]any{{"total": int64(460), "prev": int64(400)}},
	}
	ok := ChartSpec{Type: ChartKPI, SourceStepID: "q1", KPI: &KPI{Value: 460, ValueField: "total", Delta: f64(60 - 0), DeltaField: "prev"}}
	// delta must equal the source cell (400), not a computed 60.
	ok.KPI.Delta = f64(400)
	if err := ValidateGrounded(ok, src, DefaultCaps); err != nil {
		t.Fatalf("kpi with source-backed value+delta = %v", err)
	}
	bad := ChartSpec{Type: ChartKPI, SourceStepID: "q1", KPI: &KPI{Value: 999, ValueField: "total"}}
	if err := ValidateGrounded(bad, src, DefaultCaps); err == nil {
		t.Error("kpi value not present in the source should be rejected")
	}
	computed := ChartSpec{Type: ChartKPI, SourceStepID: "q1", KPI: &KPI{Value: 460, ValueField: "total", Delta: f64(60), DeltaField: "prev"}}
	if err := ValidateGrounded(computed, src, DefaultCaps); err == nil {
		t.Error("kpi delta computed as (total-prev) rather than read from the source should be rejected")
	}
}

func TestSchemaJSON_InSyncWithType(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatalf("schema.json is not valid JSON: %v", err)
	}
	// Every chart type constant must appear in the schema enum.
	typeEnum := map[string]bool{}
	for _, e := range schema.Properties["type"].Enum {
		typeEnum[e] = true
	}
	for _, ct := range AllChartTypes {
		if !typeEnum[string(ct)] {
			t.Errorf("schema.json type enum missing %q", ct)
		}
	}
	// The Go struct's required-on-the-wire fields must be marked required.
	if !contains(schema.Required, "type") || !contains(schema.Required, "source_step_id") {
		t.Errorf("schema.json required = %v, want type + source_step_id", schema.Required)
	}
	// Guard the JSON tag the whole grounding mechanism hangs on.
	b, _ := json.Marshal(ChartSpec{Type: ChartBar, SourceStepID: "q2"})
	if !strings.Contains(string(b), `"source_step_id":"q2"`) {
		t.Errorf("ChartSpec JSON tag drift: %s", b)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
