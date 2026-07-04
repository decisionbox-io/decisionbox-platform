package charts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// maxExactInt is float64's largest exactly-representable integer (2^53). A chart
// data cell above this can't have survived JSON decoding (float64) without
// possible rounding, so it can't be proven an exact projection of the source —
// grounding rejects it rather than risk a rounded value matching.
const maxExactInt = 1 << 53

// Error is a structured, model-readable validation failure. The data agent
// feeds Error() back to the model as a tool error so it can repair the spec or
// re-query; Rule + Field name the exact rule violated.
type Error struct {
	Rule  string // e.g. "type", "field_ref", "grounding", "caps", "sanitize"
	Field string // the offending field, when known
	Msg   string
}

func (e *Error) Error() string {
	switch {
	case e.Field != "":
		return fmt.Sprintf("chart rejected [%s: %s]: %s", e.Rule, e.Field, e.Msg)
	default:
		return fmt.Sprintf("chart rejected [%s]: %s", e.Rule, e.Msg)
	}
}

func ruleErr(rule, field, msg string) *Error { return &Error{Rule: rule, Field: field, Msg: msg} }

// GroundingSource is the subset of a query result a chart may be grounded
// against: the columns the result exposed, the preview rows the chart data must
// be an exact projection of, and whether the preview omitted rows. A truncated
// preview is not the full result, so it cannot ground a chart — the agent must
// aggregate in SQL until the whole result fits the preview.
//
// It is a plain value type (not the agent's internal QuerySummary) so this leaf
// package stays free of service dependencies; the caller adapts its own summary
// into this shape.
type GroundingSource struct {
	StepID    string
	Columns   []string
	Preview   []map[string]any
	Truncated bool
}

// Decode strict-parses raw render_chart input into a ChartSpec and runs the
// structural Validate. It enforces the byte cap first (a spec far over budget
// is rejected without decoding) and rejects unknown fields so the artifact
// stays strictly declarative — a model cannot smuggle an unmodeled field (an
// expression, a URL, a script) past the contract.
func Decode(raw []byte, caps Caps) (ChartSpec, error) {
	var spec ChartSpec
	if caps.MaxSpecBytes > 0 && len(raw) > caps.MaxSpecBytes {
		return spec, ruleErr("caps", "spec", fmt.Sprintf("chart spec is %d bytes, over the %d-byte limit; reduce the number of points or series", len(raw), caps.MaxSpecBytes))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return spec, ruleErr("shape", "", fmt.Sprintf("could not parse chart spec: %v", err))
	}
	if err := Validate(spec, caps); err != nil {
		return spec, err
	}
	return spec, nil
}

// Validate checks a ChartSpec's structure, field references (within its own
// Data), numeric-y typing, caps, and sanitization — everything that does not
// require the source query. ValidateGrounded adds the grounding rules. Both
// return an *Error the model can act on.
func Validate(spec ChartSpec, caps Caps) error {
	if err := validateType(spec); err != nil {
		return err
	}
	if strings.TrimSpace(spec.SourceStepID) == "" {
		return ruleErr("grounding", "source_step_id", "every chart must set source_step_id to the query step it charts, e.g. \"q2\"")
	}
	if err := sanitizeStrings(spec, caps); err != nil {
		return err
	}
	if spec.Type == ChartKPI {
		return validateKPIShape(spec)
	}
	return validateSeriesShape(spec, caps)
}

// validateType rejects unknown types and enforces the per-type presence rules
// (kpi vs the axis/series family) that the rest of validation assumes.
func validateType(spec ChartSpec) error {
	switch spec.Type {
	case ChartBar, ChartLine, ChartArea, ChartPie, ChartScatter, ChartKPI:
	default:
		return ruleErr("type", "type", fmt.Sprintf("unknown chart type %q; allowed: bar, line, area, pie, scatter, kpi", spec.Type))
	}
	// Stacked only makes sense for bar/area; SeriesBy only for bar/line/area.
	if spec.Stacked != nil && spec.Type != ChartBar && spec.Type != ChartArea {
		return ruleErr("shape", "stacked", "stacked is only valid for bar and area charts")
	}
	if spec.SeriesBy != "" && spec.Type != ChartBar && spec.Type != ChartLine && spec.Type != ChartArea {
		return ruleErr("shape", "series_by", "series_by is only valid for bar, line, and area charts")
	}
	if spec.Type == ChartKPI {
		if spec.X != nil || len(spec.Y) > 0 {
			return ruleErr("shape", "kpi", "a kpi chart must not set x or y")
		}
	}
	return nil
}

// validateKPIShape checks a KPI carries a value binding. Grounding (that the
// value equals a source cell) is checked in ValidateGrounded.
func validateKPIShape(spec ChartSpec) error {
	if spec.KPI == nil {
		return ruleErr("shape", "kpi", "a kpi chart must set the kpi object")
	}
	if spec.KPI.Value == nil {
		return ruleErr("shape", "kpi.value", "kpi.value is required (a missing value is not the same as 0)")
	}
	if strings.TrimSpace(spec.KPI.ValueField) == "" {
		return ruleErr("field_ref", "kpi.value_field", "kpi.value_field must name the source column the value was read from")
	}
	// A delta must also name its source column, or it could be computed/invented
	// (grounding then has nothing to check it against).
	if spec.KPI.Delta != nil && strings.TrimSpace(spec.KPI.DeltaField) == "" {
		return ruleErr("field_ref", "kpi.delta_field", "kpi.delta_field must name the source column when a delta is provided")
	}
	// A KPI's figures come from kpi.value/delta (grounded against the source
	// cell), not from a data array. Reject a stray data array so it can't smuggle
	// ungrounded, uncapped cells past validation (the KPI grounding path never
	// inspects Data).
	if len(spec.Data) > 0 {
		return ruleErr("shape", "data", "a kpi chart must not carry a data array; its figure comes from kpi.value (read from the source)")
	}
	return nil
}

// validateSeriesShape checks the axis/series family: an X axis, at least one Y
// series within the series cap, non-empty Data within the points cap, and that
// every referenced field is a key in every Data row and carries numeric values
// where required.
func validateSeriesShape(spec ChartSpec, caps Caps) error {
	if spec.KPI != nil {
		return ruleErr("shape", "kpi", "kpi is only valid on a kpi chart")
	}
	if spec.X == nil || strings.TrimSpace(spec.X.Field) == "" {
		return ruleErr("shape", "x", "this chart type requires an x axis with a field")
	}
	if spec.X.Type != "" && spec.X.Type != AxisCategory && spec.X.Type != AxisTime && spec.X.Type != AxisNumber {
		return ruleErr("shape", "x.type", fmt.Sprintf("unknown x.type %q; allowed: category, time, number", spec.X.Type))
	}
	if len(spec.Y) == 0 {
		return ruleErr("shape", "y", "this chart type requires at least one y series")
	}
	if caps.MaxSeries > 0 && len(spec.Y) > caps.MaxSeries {
		return ruleErr("caps", "y", fmt.Sprintf("%d series exceeds the limit of %d", len(spec.Y), caps.MaxSeries))
	}
	for i, s := range spec.Y {
		if strings.TrimSpace(s.Field) == "" {
			return ruleErr("shape", fmt.Sprintf("y[%d].field", i), "every y series must name a field")
		}
	}
	if len(spec.Data) == 0 {
		return ruleErr("shape", "data", "this chart type requires non-empty data")
	}
	if caps.MaxPoints > 0 && len(spec.Data) > caps.MaxPoints {
		return ruleErr("caps", "data", fmt.Sprintf("%d data points exceeds the limit of %d; aggregate in SQL to fewer rows", len(spec.Data), caps.MaxPoints))
	}
	// series_by pivots a SINGLE measure into one series per distinct value. Both
	// renderers (Recharts + the SVG export) chart y[0] under series_by, so more
	// than one y with series_by is ambiguous — reject it (chart one measure, or
	// drop series_by and list measures as separate y series).
	if spec.SeriesBy != "" && len(spec.Y) != 1 {
		return ruleErr("shape", "series_by", "series_by pivots a single measure — use exactly one y with series_by (or list measures as separate y series without series_by)")
	}
	// Each (x, series_by) slot must be unique — a pivot has one value per slot, so
	// two rows sharing an (x, series) pair are ambiguous and a renderer would
	// silently drop one. Aggregate in SQL so each slot is single-valued.
	if spec.SeriesBy != "" {
		seen := make(map[string]struct{}, len(spec.Data))
		for ri, row := range spec.Data {
			slot := fmt.Sprintf("%v\x00%v", row[spec.X.Field], row[spec.SeriesBy])
			if _, dup := seen[slot]; dup {
				return ruleErr("shape", "series_by", fmt.Sprintf("data row %d repeats the (x=%v, %s=%v) slot; each x/series pair must be unique (aggregate in SQL)", ri, row[spec.X.Field], spec.SeriesBy, row[spec.SeriesBy]))
			}
			seen[slot] = struct{}{}
		}
	}
	// A pie has one value dimension (its slices) and negative slices are
	// meaningless — require exactly one y and non-negative values so a renderer
	// never has to silently drop or recompute slices.
	if spec.Type == ChartPie {
		if len(spec.Y) != 1 {
			return ruleErr("shape", "y", "a pie chart uses exactly one y measure (its slice values)")
		}
		for ri, row := range spec.Data {
			if v, ok := asFloat(row[spec.Y[0].Field]); ok && v < 0 {
				return ruleErr("shape", spec.Y[0].Field, fmt.Sprintf("a pie slice cannot be negative; data row %d has a negative %q", ri, spec.Y[0].Field))
			}
		}
	}
	// series_by turns one field into one rendered series per distinct value, so
	// it multiplies the effective series count — cap the fan-out too, or a
	// high-cardinality series_by would blow past MaxSeries.
	if spec.SeriesBy != "" && caps.MaxSeries > 0 {
		distinct := map[string]struct{}{}
		for _, row := range spec.Data {
			distinct[fmt.Sprintf("%v", row[spec.SeriesBy])] = struct{}{}
		}
		if effective := len(spec.Y) * len(distinct); effective > caps.MaxSeries {
			return ruleErr("caps", "series_by", fmt.Sprintf("series_by fans out to %d rendered series (%d values × %d measures), over the limit of %d; group the long tail in SQL", effective, len(distinct), len(spec.Y), caps.MaxSeries))
		}
	}

	// Every charted field must be a key in every Data row, and every y cell
	// must be numeric (or null) so the renderer can plot it.
	for _, f := range chartedFields(spec) {
		for ri, row := range spec.Data {
			if _, ok := row[f]; !ok {
				return ruleErr("field_ref", f, fmt.Sprintf("data row %d has no field %q", ri, f))
			}
		}
	}
	for _, s := range spec.Y {
		for ri, row := range spec.Data {
			if !isNumericOrNull(row[s.Field]) {
				return ruleErr("shape", s.Field, fmt.Sprintf("y field %q must be numeric; data row %d is not", s.Field, ri))
			}
		}
	}
	// A scatter plots a continuous x, and a declared number x-axis promises
	// numeric x values — reject non-numeric x cells so a renderer never gets a
	// numeric axis over strings (mis-scaled or blank). category/time x may be
	// non-numeric.
	if spec.Type == ChartScatter || spec.X.Type == AxisNumber {
		for ri, row := range spec.Data {
			if !isNumericOrNull(row[spec.X.Field]) {
				kind := "a scatter"
				if spec.Type != ChartScatter {
					kind = "a number x-axis"
				}
				return ruleErr("shape", spec.X.Field, fmt.Sprintf("%s requires numeric x values; data row %d has a non-numeric %q (use x.type category/time for labels)", kind, ri, spec.X.Field))
			}
		}
	}
	return nil
}

// ValidateGrounded enforces the hard grounding rule: the chart's data must be an
// exact projection of the referenced query's preview — the model may drop
// columns/rows and reorder, but may not compute new numbers (all aggregation
// happens in SQL). It assumes Validate already passed (call Decode first, or
// Validate then this). src must be the query the spec's SourceStepID names.
func ValidateGrounded(spec ChartSpec, src GroundingSource, caps Caps) error {
	if err := Validate(spec, caps); err != nil {
		return err
	}
	if src.Truncated {
		return ruleErr("grounding", "source_step_id",
			fmt.Sprintf("step %q was truncated (its preview omits rows), so it cannot ground a chart; aggregate in SQL (GROUP BY/LIMIT) so the full result fits the preview, then chart that", src.StepID))
	}
	cols := map[string]struct{}{}
	for _, c := range src.Columns {
		cols[c] = struct{}{}
	}
	// Canonicalize both sides through JSON with UseNumber so numeric cells compare
	// as the exact number the model saw in the observation text (the preview is
	// shown as JSON) — as json.Number, not float64, so an integer beyond float64's
	// exact range (|x| >= 2^53) can't be silently rounded into a false match.
	preview := canonicalizeRows(src.Preview)

	if spec.Type == ChartKPI {
		return groundKPI(spec, src, cols, preview)
	}
	dataRows := canonicalizeRows(spec.Data)
	// canonicalizeRows drops a row it can't JSON round-trip (e.g. a NaN/Inf
	// value); a dropped data row would silently skip grounding, so reject the
	// spec instead of validating a subset.
	if len(dataRows) != len(spec.Data) {
		return ruleErr("shape", "data", "a data row holds a value that is not representable in JSON (e.g. NaN or Infinity); chart only finite numbers and plain values")
	}

	// Every charted field must be a real column of the source preview.
	for _, f := range chartedFields(spec) {
		if _, ok := cols[f]; !ok {
			return ruleErr("grounding", f, fmt.Sprintf("field %q is not a column of source step %q; chart only columns the query returned", f, src.StepID))
		}
	}
	// Exact projection over EVERY key in each data row (not only the plotted
	// fields): an invented key, or a real column carrying an altered value, must
	// be rejected. So each row's keys must all be source columns AND the whole
	// row must equal some preview row on all of them. Preview rows are matched
	// WITHOUT replacement (each consumed at most once) so the model cannot repeat
	// one observed row into many duplicate bars/points the query never returned —
	// the chart data is a sub-multiset of the preview, not just a subset.
	used := make([]bool, len(preview))
	for ri, row := range dataRows {
		keys := make([]string, 0, len(row))
		for k, v := range row {
			if _, ok := cols[k]; !ok {
				return ruleErr("grounding", k, fmt.Sprintf("data row %d has field %q that source step %q never returned; chart only the query's own columns (no invented fields)", ri, k, src.StepID))
			}
			if n, ok := v.(json.Number); ok {
				if f, err := n.Float64(); err == nil && math.Abs(f) >= maxExactInt {
					return ruleErr("grounding", k, fmt.Sprintf("data row %d value for %q is too large to chart with exact precision; aggregate, round, or scale it in SQL first", ri, k))
				}
			}
			keys = append(keys, k)
		}
		matched := -1
		for pi, p := range preview {
			if used[pi] {
				continue
			}
			if rowEqualsOn(row, keys, p) {
				matched = pi
				break
			}
		}
		if matched < 0 {
			return ruleErr("grounding", "data",
				fmt.Sprintf("data row %d does not match an unused row of source step %q; chart the exact query cells without inventing, altering, or duplicating rows (aggregate in SQL instead)", ri, src.StepID))
		}
		used[matched] = true
	}
	return nil
}

// groundKPI checks a KPI's value (and optional delta) each equal a source cell,
// read from the named columns.
func groundKPI(spec ChartSpec, src GroundingSource, cols map[string]struct{}, preview []map[string]any) error {
	k := spec.KPI
	// kpi.value/delta are float64, so a magnitude beyond float64's exact-integer
	// range can't be proven equal to a source cell (both sides round) — reject,
	// mirroring the series-data precision guard. (Validate already ensured
	// Value != nil.)
	if math.Abs(*k.Value) >= maxExactInt || (k.Delta != nil && math.Abs(*k.Delta) >= maxExactInt) {
		return ruleErr("grounding", "kpi", "the kpi figure is too large to ground with exact precision; aggregate, round, or scale it in SQL first")
	}
	if _, ok := cols[k.ValueField]; !ok {
		return ruleErr("grounding", "kpi.value_field", fmt.Sprintf("value_field %q is not a column of source step %q", k.ValueField, src.StepID))
	}
	if k.DeltaField != "" {
		if _, ok := cols[k.DeltaField]; !ok {
			return ruleErr("grounding", "kpi.delta_field", fmt.Sprintf("delta_field %q is not a column of source step %q", k.DeltaField, src.StepID))
		}
	}
	for _, row := range preview {
		if !cellsEqual(*k.Value, row[k.ValueField]) {
			continue
		}
		if k.DeltaField == "" || k.Delta == nil {
			return nil
		}
		if cellsEqual(*k.Delta, row[k.DeltaField]) {
			return nil
		}
	}
	return ruleErr("grounding", "kpi",
		fmt.Sprintf("the kpi value (and delta, if set) must equal a cell of source step %q; read the figure from the query result, do not compute it", src.StepID))
}

// chartedFields returns every source field the spec plots: the x field, each y
// field, and series_by. Deduplicated, so a field used twice is checked once.
func chartedFields(spec ChartSpec) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(f string) {
		if f == "" {
			return
		}
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	if spec.X != nil {
		add(spec.X.Field)
	}
	for _, s := range spec.Y {
		add(s.Field)
	}
	add(spec.SeriesBy)
	return out
}

// rowEqualsOn reports whether the data row equals one preview row on all the
// given fields — the per-row "exact projection" test. A data row must come, in
// whole, from one observed preview row; the model may not stitch fields from
// different rows.
func rowEqualsOn(row map[string]any, fields []string, p map[string]any) bool {
	for _, f := range fields {
		if !cellsEqual(row[f], p[f]) {
			return false
		}
	}
	return true
}

// canonicalizeRows JSON round-trips each row so its cells become the same
// post-JSON scalars the model was shown in the observation preview, with numbers
// decoded as json.Number (exact decimal text) rather than float64 — so a large
// integer is compared by value, not by a lossy float. A row that fails to
// round-trip is dropped (it cannot match anything), which only tightens grounding.
func canonicalizeRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var m map[string]any
		if dec.Decode(&m) != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// cellsEqual compares two cell values for the exact-projection rule. When both
// sides are json.Number (the data-grounding path, where both come from JSON
// text), they are compared as exact decimals via big.Rat — so a rounded or
// altered large number ("9007199254740993" charted as "9007199254740992")
// cannot pass, and trivial re-representations ("100" vs "100.0") still match.
// Otherwise (e.g. a KPI's float64 value against a source cell) it falls back to
// float64 numeric equality, then exact string/bool comparison.
func cellsEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if an, aok := a.(json.Number); aok {
		if bn, bok := b.(json.Number); bok {
			return numberEqual(an, bn)
		}
	}
	if af, aok := asFloat(a); aok {
		if bf, bok := asFloat(b); bok {
			return af == bf
		}
		return false
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// numberEqual compares two JSON numbers by exact decimal value (big.Rat), so
// integers beyond float64's exact range compare precisely. Falls back to exact
// string equality if either is not a parseable rational (should not happen for
// JSON numbers).
func numberEqual(a, b json.Number) bool {
	ra, oka := new(big.Rat).SetString(a.String())
	rb, okb := new(big.Rat).SetString(b.String())
	if oka && okb {
		return ra.Cmp(rb) == 0
	}
	return a.String() == b.String()
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func isNumericOrNull(v any) bool {
	if v == nil {
		return true
	}
	if _, ok := asFloat(v); ok {
		return true
	}
	// A numeric STRING counts as numeric: some warehouses (e.g. BigQuery
	// NUMERIC/BIGNUMERIC) encode exact-decimal columns as JSON strings to keep
	// precision, so a monetary measure arrives as "1234.56". The model copies the
	// cell verbatim (grounds string==string) and every renderer parses it.
	if s, ok := v.(string); ok {
		_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return err == nil && strings.TrimSpace(s) != ""
	}
	return false
}

// sanitizeStrings rejects any human-facing string that could break out of a
// declarative render into markup/script, and enforces the label-length cap.
// Applies to titles, captions, axis/series labels, and every string data cell.
func sanitizeStrings(spec ChartSpec, caps Caps) error {
	check := func(field, s string) error {
		if caps.MaxLabelLen > 0 && len(s) > caps.MaxLabelLen {
			return ruleErr("caps", field, fmt.Sprintf("%q exceeds the %d-character label limit", field, caps.MaxLabelLen))
		}
		if unsafeString(s) {
			return ruleErr("sanitize", field, fmt.Sprintf("%q contains disallowed characters (markup, script, or control characters); use plain text", field))
		}
		return nil
	}
	if err := check("title", spec.Title); err != nil {
		return err
	}
	if err := check("caption", spec.Caption); err != nil {
		return err
	}
	// Field references and data keys are rendered too — a renderer falls back to
	// the field name for a legend/axis/tooltip when no label is set — so a column
	// aliased to markup/control text must be rejected just like a label.
	if spec.X != nil {
		if err := check("x.label", spec.X.Label); err != nil {
			return err
		}
		if err := check("x.field", spec.X.Field); err != nil {
			return err
		}
	}
	for i, s := range spec.Y {
		if err := check(fmt.Sprintf("y[%d].label", i), s.Label); err != nil {
			return err
		}
		if err := check(fmt.Sprintf("y[%d].field", i), s.Field); err != nil {
			return err
		}
	}
	if err := check("series_by", spec.SeriesBy); err != nil {
		return err
	}
	if spec.KPI != nil {
		if err := check("kpi.unit", spec.KPI.Unit); err != nil {
			return err
		}
		if err := check("kpi.value_field", spec.KPI.ValueField); err != nil {
			return err
		}
		if err := check("kpi.delta_field", spec.KPI.DeltaField); err != nil {
			return err
		}
	}
	for ri, row := range spec.Data {
		for k, v := range row {
			if err := check(fmt.Sprintf("data[%d] key", ri), k); err != nil {
				return err
			}
			switch cell := v.(type) {
			case nil, bool, float64, float32, int, int32, int64, json.Number:
				// scalar numeric/bool/null — fine.
			case string:
				if err := check(fmt.Sprintf("data[%d].%s", ri, k), cell); err != nil {
					return err
				}
			default:
				// A JSON/RECORD/ARRAY source column decodes to a map/slice; a chart
				// plots scalars, and a nested value could smuggle markup past the
				// top-level string check — reject non-scalar cells outright.
				return ruleErr("shape", fmt.Sprintf("data[%d].%s", ri, k), "chart data cells must be scalar (string, number, bool, or null), not an object or array")
			}
		}
	}
	return nil
}

// unsafeString reports whether s carries markup, a javascript: scheme, or
// control characters — anything a declarative renderer should never receive.
func unsafeString(s string) bool {
	if strings.ContainsAny(s, "<>") {
		return true
	}
	if strings.Contains(strings.ToLower(s), "javascript:") {
		return true
	}
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
