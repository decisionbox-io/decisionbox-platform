// Package verifier implements the LLM-native validation agent (the
// verifier defender + refuter skeptic) that replaces the legacy
// UserCountValidator + InsightValidator pair.
//
// The package is exported as part of services/agent/internal but is
// designed to be used only by the orchestrator (and by the
// validation-replay CLI that exercises it standalone).
//
// Wire types live in libs/go-common/models/validation so both
// services/agent and services/api can read the persisted shape; this
// package owns only the running-agent state (Bundle + Agent loop).
package verifier

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Bundle is the read-only context handed to verifier + refuter agents.
// Plan §"The bundle — in-memory, no Mongo".
//
// PriorClaims is set when the refuter runs after the verifier: the
// refuter copies the verifier's enumerated claim set verbatim so both
// agents attack the same surface and Combine() compares apples to
// apples.
type Bundle struct {
	Doc                  DocDigest          `json:"doc"`
	SourceSteps          []SourceStepDigest `json:"source_steps"`
	Warehouse            WarehouseInfo      `json:"warehouse"`
	Discovery            DiscoveryContext   `json:"discovery"`
	SourceStepsTruncated bool               `json:"source_steps_truncated"`
	SourceStepsOmitted   int                `json:"source_steps_omitted"`
	PriorClaims          []string           `json:"prior_claims,omitempty"`
}

// DocDigest is the insight/recommendation summary the agent receives.
// AffectedCount, Priority, and Metrics are optional — the doc shape
// is asymmetric across insight vs. recommendation but the agent only
// needs the headline-plus-description surface to enumerate claims.
type DocDigest struct {
	Kind          valmodels.DocKind `json:"kind"`
	ID            string            `json:"id"`
	Description   string            `json:"description"`
	Headline      string            `json:"headline"`
	Severity      string            `json:"severity,omitempty"`
	Priority      string            `json:"priority,omitempty"`
	AffectedCount int               `json:"affected_count,omitempty"`
	SegmentSize   int               `json:"segment_size,omitempty"`
	Metrics       map[string]any    `json:"metrics,omitempty"`
	SourceStepIDs []int             `json:"source_step_ids"`
	Language      string            `json:"language"`

	// Recommendation-specific fields. These are the quantitative
	// + actionable surface the dashboard renders on the
	// recommendation detail page; without them in the digest the
	// verifier never sees the impact claim ("X% improvement") or
	// the action list and would silently mark recommendations
	// supported despite the displayed impact being unverified.
	ExpectedImpact *ExpectedImpactDigest `json:"expected_impact,omitempty"`
	Actions        []string              `json:"actions,omitempty"`
}

// ExpectedImpactDigest mirrors the shape the dashboard renders so
// the verifier can build claims against the same fields. Maps 1:1
// to models.Recommendation.ExpectedImpact.
type ExpectedImpactDigest struct {
	Metric                string `json:"metric,omitempty"`
	EstimatedImprovement  string `json:"estimated_improvement,omitempty"`
	Reasoning             string `json:"reasoning,omitempty"`
}

// SourceStepDigest is one exploration step boiled down to schema +
// sample rows + execution metadata. The agent uses these via
// read_step_rows; out-of-snapshot offsets force the agent to either
// run query_warehouse or mark the claim unverifiable.
type SourceStepDigest struct {
	StepID       int              `json:"step_id"`
	SQL          string           `json:"sql"`
	Reasoning    string           `json:"reasoning"`
	Schema       []ColumnInfo     `json:"schema"`
	SampleRows   []map[string]any `json:"sample_rows"`
	FullRowCount int              `json:"full_row_count"`
	Truncated    bool             `json:"truncated"`
}

type ColumnInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// WarehouseInfo carries the dialect + dataset + run-wide filter so
// the agent's prompts substitute the correct values per bundle.
// FilterField/FilterValue may be empty (no run-wide filter); Filter
// is the rendered SQL predicate the agent must include in every
// query_warehouse call when non-empty.
type WarehouseInfo struct {
	Dialect     string `json:"dialect"`
	Dataset     string `json:"dataset"`
	Filter      string `json:"filter,omitempty"`
	FilterField string `json:"filter_field,omitempty"`
	FilterValue string `json:"filter_value,omitempty"`
}

// DiscoveryContext is the run-level metadata the prompt surfaces so
// the agent knows which project/run/language frame it is operating
// in.
type DiscoveryContext struct {
	ProjectID string `json:"project_id"`
	RunID     string `json:"run_id"`
	Domain    string `json:"domain"`
	Language  string `json:"language"`
}

// BundleConfig is the per-Agent (per-bundle, in practice) knob set
// that controls truncation. The orchestrator constructs these from
// env vars; tests construct them inline.
type BundleConfig struct {
	SampleRows       int     // per-step sample size cap (default 50)
	CellCharCap      int     // per-cell character cap (default 200)
	RecStepsTokenCap int     // recommendation source-step union token budget (default 12000)
	EstimateRatio    float64 // approximate chars-per-token; default 3.5
}

// DefaultBundleConfig matches plan §"Cost envelope and budgets".
func DefaultBundleConfig() BundleConfig {
	return BundleConfig{
		SampleRows:       50,
		CellCharCap:      200,
		RecStepsTokenCap: 12000,
		EstimateRatio:    3.5,
	}
}

// BuildInsightBundle assembles a Bundle for one insight. No source-
// step cap (an insight cites its own steps; the writer chose them).
//
// stepByID indexes the in-memory exploration snapshot by step number.
// Missing IDs are silently skipped — the orchestrator is responsible
// for keeping the snapshot consistent with the insight's source_steps
// field; a partial snapshot just narrows the evidence available to
// the agent.
func BuildInsightBundle(ins *agentmodels.Insight, stepByID map[int]*agentmodels.ExplorationStep, wh WarehouseInfo, disc DiscoveryContext, cfg BundleConfig) Bundle {
	digests := make([]SourceStepDigest, 0, len(ins.SourceSteps))
	for _, id := range ins.SourceSteps {
		s, ok := stepByID[id]
		if !ok {
			continue
		}
		digests = append(digests, digestStep(s, cfg))
	}
	return Bundle{
		Doc: DocDigest{
			Kind:          valmodels.DocInsight,
			ID:            ins.ID,
			Description:   ins.Description,
			Headline:      insightHeadline(ins),
			Severity:      ins.Severity,
			AffectedCount: ins.AffectedCount,
			Metrics:       ins.Metrics,
			SourceStepIDs: append([]int(nil), ins.SourceSteps...),
			Language:      disc.Language,
		},
		SourceSteps: digests,
		Warehouse:   wh,
		Discovery:   disc,
	}
}

// insightHeadline returns the canonical headline string for an
// agent-emitted insight. Live Mongo docs carry the headline in the
// `name` field — agentmodels.Insight has no separate Headline. Kept
// as a helper so future schema additions stay in one place.
func insightHeadline(ins *agentmodels.Insight) string {
	return ins.Name
}

// BuildRecommendationBundle assembles a Bundle for one recommendation
// using the token-budgeted union of source steps from each related
// insight. Plan §"Source-step budgeting" — recommendation case.
//
// Iterate the recommendation's RelatedInsightIDs in order; for each,
// iterate the insight's source_steps in order; deduplicate; estimate
// per-step token cost; stop adding when the token cap is hit. Set
// SourceStepsTruncated + SourceStepsOmitted accordingly.
func BuildRecommendationBundle(rec *agentmodels.Recommendation, insightByID map[string]*agentmodels.Insight, stepByID map[int]*agentmodels.ExplorationStep, wh WarehouseInfo, disc DiscoveryContext, cfg BundleConfig) Bundle {
	seen := make(map[int]bool, 16)
	digests := make([]SourceStepDigest, 0, 16)
	tokens := 0
	omitted := 0
	truncated := false
	for _, insID := range rec.RelatedInsightIDs {
		ins, ok := insightByID[insID]
		if !ok {
			continue
		}
		for _, sid := range ins.SourceSteps {
			if seen[sid] {
				continue
			}
			s, ok := stepByID[sid]
			if !ok {
				continue
			}
			d := digestStep(s, cfg)
			est := estimateTokens(serialise(d), cfg.EstimateRatio)
			if cfg.RecStepsTokenCap > 0 && tokens+est > cfg.RecStepsTokenCap {
				truncated = true
				omitted++
				continue
			}
			seen[sid] = true
			digests = append(digests, d)
			tokens += est
		}
	}
	sort.Slice(digests, func(i, j int) bool { return digests[i].StepID < digests[j].StepID })
	stepIDs := make([]int, 0, len(seen))
	for k := range seen {
		stepIDs = append(stepIDs, k)
	}
	sort.Ints(stepIDs)
	// Include ExpectedImpact and Actions in the digest so the
	// verifier can build claims against the impact statement and
	// each action — the dashboard displays both, and recommending
	// a doc as "supported" without checking those fields means a
	// fabricated "10% conversion lift" sentence passes silently.
	var impact *ExpectedImpactDigest
	if rec.ExpectedImpact.Metric != "" ||
		rec.ExpectedImpact.EstimatedImprovement != "" ||
		rec.ExpectedImpact.Reasoning != "" {
		impact = &ExpectedImpactDigest{
			Metric:               rec.ExpectedImpact.Metric,
			EstimatedImprovement: rec.ExpectedImpact.EstimatedImprovement,
			Reasoning:            rec.ExpectedImpact.Reasoning,
		}
	}
	return Bundle{
		Doc: DocDigest{
			Kind:           valmodels.DocRecommendation,
			ID:             rec.ID,
			Description:    rec.Description,
			Headline:       rec.Title,
			Priority:       priorityLabel(rec.Priority),
			SegmentSize:    rec.SegmentSize,
			SourceStepIDs:  stepIDs,
			Language:       disc.Language,
			ExpectedImpact: impact,
			Actions:        append([]string(nil), rec.Actions...),
		},
		SourceSteps:          digests,
		Warehouse:            wh,
		Discovery:            disc,
		SourceStepsTruncated: truncated,
		SourceStepsOmitted:   omitted,
	}
}

// priorityLabel maps the Mongo int priority (1..5) onto the string
// label the prompt expects. p<=0 returns empty so omitempty kicks in.
func priorityLabel(p int) string {
	switch p {
	case 1:
		return "high"
	case 2:
		return "medium"
	case 3:
		return "low"
	case 0:
		return ""
	default:
		return fmt.Sprintf("p%d", p)
	}
}

func digestStep(s *agentmodels.ExplorationStep, cfg BundleConfig) SourceStepDigest {
	fullCount := len(s.QueryResult)
	rows := s.QueryResult
	truncated := false
	sampleCap := cfg.SampleRows
	if sampleCap <= 0 {
		sampleCap = 50
	}
	if fullCount > sampleCap {
		rows = rows[:sampleCap]
		truncated = true
	}
	sampled := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		sampled = append(sampled, normaliseRow(r, cfg.CellCharCap))
	}
	return SourceStepDigest{
		StepID:       s.Step,
		SQL:          s.Query,
		Reasoning:    s.Thinking,
		Schema:       inferSchema(rows),
		SampleRows:   sampled,
		FullRowCount: fullCount,
		Truncated:    truncated,
	}
}

// normaliseRow returns a copy of row with each value passed through
// normaliseValue (which handles BigQuery int64-wrapping, recursive
// nested maps, JSON serialisation of arrays, and per-cell char caps).
func normaliseRow(row map[string]any, cellCap int) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = normaliseValue(v, cellCap)
	}
	return out
}

// normaliseValue is the single hop a row cell passes through before
// landing in the bundle. Three transformations:
//  1. BigQuery int64 wrapping `{low, high, unsigned}` → plain number.
//  2. Nested maps + arrays are JSON-serialised and capped (so the
//     prompt sees `"{...}"` rather than an exploded object) — except
//     BQ-wrapped values, which are unwrapped to their numeric form.
//  3. Strings over cellCap chars are truncated with an ellipsis.
//
// Recursion is depth-1 — nested maps are serialised after their inner
// BQ wrappers are unwrapped. Nested STRUCTs were a known leak surface
// before the recursive unwrap landed here.
func normaliseValue(v any, cellCap int) any {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if u, ok := unwrapBQInt64(x); ok {
			return u
		}
		// Recurse into inner cells then serialise compact.
		inner := make(map[string]any, len(x))
		for k, vv := range x {
			inner[k] = normaliseValue(vv, cellCap)
		}
		return capCell(serialise(inner), cellCap)
	case []any:
		inner := make([]any, len(x))
		for i, vv := range x {
			inner[i] = normaliseValue(vv, cellCap)
		}
		return capCell(serialise(inner), cellCap)
	case string:
		return capCell(x, cellCap)
	default:
		return v
	}
}

// unwrapBQInt64 detects the BigQuery driver's int64 wire shape and
// returns the underlying number. `(value, true)` on hit; `(nil, false)`
// otherwise.
func unwrapBQInt64(m map[string]any) (any, bool) {
	if len(m) != 3 {
		return nil, false
	}
	low, hasLow := m["low"]
	if !hasLow {
		return nil, false
	}
	if _, hasHigh := m["high"]; !hasHigh {
		return nil, false
	}
	if _, hasUnsigned := m["unsigned"]; !hasUnsigned {
		return nil, false
	}
	switch n := low.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return nil, false
}

// capCell truncates s to at most cellCap runes (not bytes — UTF-8
// safe) and appends an ellipsis when cut.
func capCell(s string, cellCap int) string {
	if cellCap <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= cellCap {
		return s
	}
	r := 0
	for i := range s {
		if r >= cellCap {
			return s[:i] + "…"
		}
		r++
	}
	return s
}

// inferSchema returns one ColumnInfo per key in the first row, sorted
// by name for deterministic output. Type is inferred from the value
// shape (string/int/float/bool/null/object/array).
func inferSchema(rows []map[string]any) []ColumnInfo {
	if len(rows) == 0 {
		return nil
	}
	keys := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ColumnInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, ColumnInfo{Name: k, Type: inferType(rows[0][k])})
	}
	return out
}

func inferType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int, int32, int64:
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "string"
	case map[string]any:
		if _, hi := x["high"]; hi {
			if _, lo := x["low"]; lo {
				return "int"
			}
		}
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// serialise marshals v to a JSON string. Marshal errors fall back to
// the printf form so the agent never gets a literal `<nil>` cell.
func serialise(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// estimateTokens is the cheap chars-to-tokens approximation used by
// the per-round budget envelope. Plan §"Cost envelope" pins the ratio
// at 3.5 for SQL/code-heavy content; default applies when ratio<=0.
func estimateTokens(s string, ratio float64) int {
	if ratio <= 0 {
		ratio = 3.5
	}
	return int(float64(len(s)) / ratio)
}

// renderForPrompt builds the human-readable rendering of the bundle
// the agent sees. Includes the EVIDENCE OMITTED preamble when source
// steps were dropped under the token budget — the model must mark
// any claim that relied on a dropped step as `unverifiable`.
func (b *Bundle) renderForPrompt() string {
	var sb strings.Builder
	if b.SourceStepsTruncated {
		fmt.Fprintf(&sb, "## EVIDENCE OMITTED\n%d source step(s) were dropped from this bundle to stay within token budget. Claims dependent on omitted steps must be marked `unverifiable`.\n\n", b.SourceStepsOmitted)
	}
	sb.WriteString("## DOC\n")
	sb.WriteString(serialise(b.Doc))
	sb.WriteString("\n\n## SOURCE STEPS\n")
	for i := range b.SourceSteps {
		s := &b.SourceSteps[i]
		fmt.Fprintf(&sb, "\n### step %d (full_row_count=%d, truncated=%v)\n", s.StepID, s.FullRowCount, s.Truncated)
		if s.SQL != "" {
			sb.WriteString("sql:\n```sql\n")
			sb.WriteString(s.SQL)
			sb.WriteString("\n```\n")
		}
		sb.WriteString("schema: ")
		sb.WriteString(serialise(s.Schema))
		sb.WriteString("\nsample_rows:\n")
		for _, r := range s.SampleRows {
			sb.WriteString("- ")
			sb.WriteString(serialise(r))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n## WAREHOUSE\n")
	sb.WriteString(serialise(b.Warehouse))
	sb.WriteString("\n\n## DISCOVERY\n")
	sb.WriteString(serialise(b.Discovery))
	if len(b.PriorClaims) > 0 {
		sb.WriteString("\n\n## PRIOR_CLAIMS (verbatim — copy into your claims_considered)\n")
		for i, c := range b.PriorClaims {
			fmt.Fprintf(&sb, "  [%d] %s\n", i, c)
		}
	}
	return sb.String()
}
