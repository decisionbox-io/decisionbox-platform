// Package charts is the renderer-agnostic source of truth for the data agent's
// chart artifacts. A ChartSpec is a small, declarative JSON description of a
// chart (bar / line / area / pie / scatter / KPI) whose data is an EXACT
// projection of a query result the agent already observed — never derived,
// never invented. The same spec is rendered client-side (Recharts, in the web
// dashboard) and server-side (go-chart, for PNG/SVG export), and mirrored in
// Dart for the mobile app; keeping the type + validator here (a leaf package
// with no service dependencies) lets every renderer share one contract.
//
// This package deliberately holds no product wiring: it defines the shape, the
// structural validator, and the grounding validator, and nothing about who is
// allowed to produce a chart. Gating lives in the caller (the data agent's
// serve loop and the enterprise API), so this package stays neutral and
// importable from anywhere.
package charts

// ChartType is the discriminant selecting how a ChartSpec is rendered. The set
// is closed on purpose — an unknown type is rejected by Validate — so every
// renderer can exhaustively switch over it.
type ChartType string

const (
	ChartBar     ChartType = "bar"
	ChartLine    ChartType = "line"
	ChartArea    ChartType = "area"
	ChartPie     ChartType = "pie"
	ChartScatter ChartType = "scatter"
	ChartKPI     ChartType = "kpi"
)

// AllChartTypes is the canonical ordered list of chart types (used by the
// schema drift test and by callers building UI selectors).
var AllChartTypes = []ChartType{ChartBar, ChartLine, ChartArea, ChartPie, ChartScatter, ChartKPI}

// axisKind constrains an Axis.Type. category = discrete labels; time = a
// temporal axis; number = a continuous numeric axis. Empty is treated as
// category for the X axis.
const (
	AxisCategory = "category"
	AxisTime     = "time"
	AxisNumber   = "number"
)

// Axis describes one chart axis: which row field feeds it, its human label,
// and how the values should be interpreted. Only the X axis is modeled
// explicitly; Y is a list of Series so multi-measure charts share one struct.
type Axis struct {
	Field string `json:"field" bson:"field"`
	Label string `json:"label,omitempty" bson:"label,omitempty"`
	Type  string `json:"type,omitempty" bson:"type,omitempty"` // category | time | number
}

// Series is one plotted measure: the row field carrying its values and an
// optional display label.
type Series struct {
	Field string `json:"field" bson:"field"`
	Label string `json:"label,omitempty" bson:"label,omitempty"`
}

// KPI is a single headline figure (the "kpi" chart type): a value, an optional
// delta, their units, and the source columns those figures were read from. The
// *Field members bind the figure to a column of the source query preview so a
// KPI's provenance is provable, not asserted.
type KPI struct {
	Value      float64  `json:"value" bson:"value"`
	Unit       string   `json:"unit,omitempty" bson:"unit,omitempty"`
	Delta      *float64 `json:"delta,omitempty" bson:"delta,omitempty"`
	ValueField string   `json:"value_field" bson:"value_field"`
	DeltaField string   `json:"delta_field,omitempty" bson:"delta_field,omitempty"`
}

// ChartSpec is the full, declarative chart artifact the agent emits via the
// render_chart tool and the dashboard/export renders. It is intentionally
// declarative-only: no expressions, no formatting code, no URLs — just a chart
// type, its axes/series, and embedded data cells that must exactly match the
// referenced query preview (see ValidateGrounded).
type ChartSpec struct {
	Type    ChartType `json:"type" bson:"type"`
	Title   string    `json:"title,omitempty" bson:"title,omitempty"`
	Caption string    `json:"caption,omitempty" bson:"caption,omitempty"`

	X        *Axis    `json:"x,omitempty" bson:"x,omitempty"`
	Y        []Series `json:"y,omitempty" bson:"y,omitempty"`
	SeriesBy string   `json:"series_by,omitempty" bson:"series_by,omitempty"`
	Stacked  *bool    `json:"stacked,omitempty" bson:"stacked,omitempty"`

	// Data is the embedded, capped, grounded data: an exact projection of the
	// referenced query's preview rows. The model may drop columns/rows and
	// reorder, but every cell must equal a cell of the source preview.
	Data []map[string]any `json:"data,omitempty" bson:"data,omitempty"`

	// SourceStepID names the query step this data came from, e.g. "q2". It binds
	// the chart to a specific observed result so grounding can be verified.
	SourceStepID string `json:"source_step_id" bson:"source_step_id"`

	KPI *KPI `json:"kpi,omitempty" bson:"kpi,omitempty"`
}

// Caps bounds a chart so a single spec cannot exhaust tokens, memory, or the
// renderer. Zero fields mean "unbounded" for that dimension; callers supply
// DefaultCaps (or config-derived caps) rather than the zero value.
type Caps struct {
	MaxPoints    int // max Data rows
	MaxSeries    int // max Y series
	MaxSpecBytes int // max raw JSON bytes of the spec (checked in Decode)
	MaxLabelLen  int // max length of any title/caption/label/string cell
}

// DefaultCaps are conservative defaults used when a caller does not override
// them. MaxPoints intentionally matches the data agent's default query preview
// row count (50): a chart is grounded against the preview, so it can never have
// more points than the preview carries.
var DefaultCaps = Caps{
	MaxPoints:    50,
	MaxSeries:    8,
	MaxSpecBytes: 32768,
	MaxLabelLen:  120,
}
