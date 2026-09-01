package models

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// InsightValidation is an alias for the shared validation type in
// libs/go-common/models/validation so the agent and the API see one
// struct on the wire.
type InsightValidation = valmodels.InsightValidation

// DiscoveryResult represents the complete output of a discovery run.
// Every LLM interaction is stored for traceability and fine-tuning.
type DiscoveryResult struct {
	ID        string `bson:"_id,omitempty" json:"id"`
	ProjectID string `bson:"project_id" json:"project_id"`
	// WarehouseID is the datasource this discovery ran against (multi-warehouse).
	// Empty for legacy / single-warehouse runs. The originating warehouse of
	// every insight + SQL example flows from here (fine-tuning routes its SQL
	// validation to the right datasource by it).
	WarehouseID   string    `bson:"warehouse_id,omitempty" json:"warehouse_id,omitempty"`
	Domain        string    `bson:"domain" json:"domain"`
	Category      string    `bson:"category" json:"category"`
	DiscoveryDate time.Time `bson:"discovery_date" json:"discovery_date"`

	RunType        string   `bson:"run_type" json:"run_type"`                                   // "full" or "partial"
	AreasRequested []string `bson:"areas_requested,omitempty" json:"areas_requested,omitempty"` // for partial runs

	TotalSteps int           `bson:"total_steps" json:"total_steps"`
	Duration   time.Duration `bson:"duration" json:"duration"`

	Schemas map[string]TableSchema `bson:"schemas,omitempty" json:"schemas,omitempty"`

	// Final outputs
	Insights        []Insight        `bson:"insights" json:"insights"`
	Recommendations []Recommendation `bson:"recommendations" json:"recommendations"`
	Summary         Summary          `bson:"summary" json:"summary"`

	// Complete LLM dialog logs were previously embedded here as
	// ExplorationLog / AnalysisLog / RecommendationLog / ValidationLog
	// arrays. A 97-step run on a wide warehouse blew past the 16MB BSON
	// document limit ("an inserted document is too large"), killing the
	// discovery save. The logs now live in dedicated per-step
	// collections — see DiscoveryLogRepository (discovery_exploration_steps,
	// discovery_analysis_steps, discovery_validation_results,
	// discovery_recommendation_log) — keyed by this discovery's _id. The
	// dashboard hydrates them through paginated GET endpoints rather than
	// re-reading the parent doc.

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// TableSchema represents a warehouse table's schema.
type TableSchema struct {
	TableName    string                   `bson:"table_name" json:"table_name"`
	RowCount     int64                    `bson:"row_count" json:"row_count"`
	Columns      []ColumnInfo             `bson:"columns" json:"columns"`
	KeyColumns   []string                 `bson:"key_columns" json:"key_columns"`
	Metrics      []string                 `bson:"metrics" json:"metrics"`
	Dimensions   []string                 `bson:"dimensions" json:"dimensions"`
	SampleData   []map[string]interface{} `bson:"sample_data,omitempty" json:"sample_data,omitempty"`
	DiscoveredAt time.Time                `bson:"discovered_at" json:"discovered_at"`
}

// ColumnInfo represents a single column's metadata.
type ColumnInfo struct {
	Name     string `bson:"name" json:"name"`
	Type     string `bson:"type" json:"type"`
	Nullable bool   `bson:"nullable" json:"nullable"`
	Category string `bson:"category" json:"category"` // primary_key, time, metric, dimension
}

// ---------------------------------------------------------------------------
// Insight & Recommendation (final outputs)
// ---------------------------------------------------------------------------

// Insight is a domain-agnostic discovered pattern or finding.
type Insight struct {
	ID           string `bson:"id" json:"id"`
	AnalysisArea string `bson:"analysis_area" json:"analysis_area"` // "churn", "levels", etc.
	Name         string `bson:"name" json:"name"`
	Description  string `bson:"description" json:"description"`
	// DescriptionMd is the GitHub-Flavored Markdown rendition of Description,
	// authored by the analysis LLM. Description is the plain-text reduction
	// derived from it at parse time (raw, for API consumers / previews /
	// embeddings); DescriptionMd is empty when the description carries no
	// formatting and on legacy documents.
	DescriptionMd string `bson:"description_md,omitempty" json:"description_md,omitempty"`
	Severity      string `bson:"severity" json:"severity"` // "critical", "high", "medium", "low"

	AffectedCount int     `bson:"affected_count" json:"affected_count"`
	RiskScore     float64 `bson:"risk_score" json:"risk_score"`
	Confidence    float64 `bson:"confidence" json:"confidence"`

	// Flexible domain-specific metrics
	Metrics    map[string]interface{} `bson:"metrics,omitempty" json:"metrics,omitempty"`
	Indicators []string               `bson:"indicators,omitempty" json:"indicators,omitempty"`

	TargetSegment string `bson:"target_segment,omitempty" json:"target_segment,omitempty"`

	// Source exploration steps that this insight is based on.
	// Set by the LLM during analysis — cites which exploration queries it used.
	SourceSteps []int `bson:"source_steps,omitempty" json:"source_steps,omitempty"`

	// Quality is the union of the caveats carried by the steps this insight
	// was drawn from — what the sources said about how faithful that evidence
	// was.
	//
	// Derived from SourceSteps rather than authored, because the model cannot
	// be relied on to carry it: an insight computed from withheld rows reads
	// exactly like one computed from complete rows, and a model that failed to
	// mention the caveat would produce a finding indistinguishable from a
	// sound one. Deriving it means the label survives regardless of what the
	// model wrote.
	Quality []gowarehouse.QualityCaveat `bson:"quality,omitempty" json:"quality,omitempty"`

	SQLMetadata  *SQLMetadata `bson:"sql_metadata,omitempty" json:"sql_metadata,omitempty"`
	DiscoveredAt time.Time    `bson:"discovered_at" json:"discovered_at"`

	// Validation result (populated after warehouse verification)
	Validation *InsightValidation `bson:"validation,omitempty" json:"validation,omitempty"`
}

// UnmarshalJSON decodes an insight tolerantly. Like the recommendation decoder,
// it coerces the numeric and list fields that smaller/open models frequently
// emit with the wrong JSON type — affected_count / risk_score / confidence as
// strings ("1200", "0.8"), source_steps as string-typed or single values, and
// indicators as a bare string instead of a list. Under strict decoding a single
// off-typed field failed the whole insight and — because the analysis batch was
// decoded in one shot — silently zeroed the area's insights (the insight
// analogue of the recommendation loss fixed in issue #342). It reuses the
// recommendation coercion helpers, so valid input (Opus, GPT) decodes to
// identical values.
//
// Only JSON decoding is customized; BSON is unaffected, so persisted insights
// read back unchanged.
func (in *Insight) UnmarshalJSON(data []byte) error {
	type alias Insight
	aux := &struct {
		AffectedCount json.RawMessage `json:"affected_count"`
		RiskScore     json.RawMessage `json:"risk_score"`
		Confidence    json.RawMessage `json:"confidence"`
		SourceSteps   json.RawMessage `json:"source_steps"`
		Indicators    json.RawMessage `json:"indicators"`
		*alias
	}{alias: (*alias)(in)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	in.AffectedCount = coerceFlexInt(aux.AffectedCount)
	in.RiskScore = coerceFlexFloat(aux.RiskScore)
	in.Confidence = coerceFlexFloat(aux.Confidence)
	in.SourceSteps = coerceFlexIntSlice(aux.SourceSteps)
	in.Indicators = coerceStringSlice(aux.Indicators)
	return nil
}

// Recommendation is an actionable suggestion based on discovered insights.
type Recommendation struct {
	ID          string `bson:"id" json:"id"`
	Category    string `bson:"category" json:"category"`
	Title       string `bson:"title" json:"title"`
	Description string `bson:"description" json:"description"`
	// DescriptionMd is the GitHub-Flavored Markdown rendition of Description.
	// Description stays plain text; DescriptionMd is empty when unformatted
	// and on legacy documents.
	DescriptionMd string `bson:"description_md,omitempty" json:"description_md,omitempty"`
	Priority      int    `bson:"priority" json:"priority"` // 1-5

	TargetSegment string `bson:"target_segment" json:"target_segment"`
	SegmentSize   int    `bson:"segment_size" json:"segment_size"`

	ExpectedImpact    Impact   `bson:"expected_impact" json:"expected_impact"`
	Actions           []string `bson:"actions" json:"actions"`
	RelatedInsightIDs []string `bson:"related_insight_ids,omitempty" json:"related_insight_ids,omitempty"`

	Confidence float64   `bson:"confidence" json:"confidence"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`

	// Validation is the verifier+refuter verdict attached after the
	// orchestrator's recommendation-validation phase runs. Nil on
	// legacy docs.
	Validation *InsightValidation `bson:"validation,omitempty" json:"validation,omitempty"`
}

// UnmarshalJSON decodes a recommendation tolerantly. Beyond the tolerant
// Impact decoding, it coerces the numeric scalar fields (priority,
// segment_size, confidence) when a model emits them as strings — a common,
// model-independent behaviour that, under strict decoding, failed the whole
// recommendation and (because the batch was decoded in one shot) silently
// zeroed the run's recommendations (issue #342). priority accepts descriptive
// words ("high", "critical", "P2") as well as numbers; segment_size and
// confidence accept numeric strings (including thousands separators).
//
// Only JSON decoding is customized; BSON is unaffected, so persisted
// recommendations read back unchanged.
func (r *Recommendation) UnmarshalJSON(data []byte) error {
	type alias Recommendation
	aux := &struct {
		Priority    json.RawMessage `json:"priority"`
		SegmentSize json.RawMessage `json:"segment_size"`
		Confidence  json.RawMessage `json:"confidence"`
		*alias
	}{alias: (*alias)(r)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	r.Priority = coercePriority(aux.Priority)
	r.SegmentSize = coerceFlexInt(aux.SegmentSize)
	r.Confidence = coerceFlexFloat(aux.Confidence)
	return nil
}

// coercePriority reads a recommendation priority that may be a number or a
// descriptive string. Numbers pass through; "P2"/"2" parse directly; and
// severity words map onto the 1 (highest) – 5 (lowest) scale. An
// unrecognized value yields 0 (unset) rather than failing the decode.
func coercePriority(raw json.RawMessage) int {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0
		}
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(s, "p") // "P2" -> "2"
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
		// Order matters: more specific words are checked before substrings
		// they contain ("lowest"/"highest" before "low"/"high").
		switch {
		case strings.Contains(s, "critical"), strings.Contains(s, "highest"), strings.Contains(s, "urgent"):
			return 1
		case strings.Contains(s, "optional"), strings.Contains(s, "minimal"), strings.Contains(s, "lowest"):
			return 5
		case strings.Contains(s, "high"):
			return 2
		case strings.Contains(s, "medium"), strings.Contains(s, "moderate"), strings.Contains(s, "med"):
			return 3
		case strings.Contains(s, "low"):
			return 4
		}
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return int(n)
	}
	return 0
}

// coerceFlexInt reads an int that may arrive as a number or a numeric string
// (thousands separators tolerated). Unparseable input yields 0.
func coerceFlexInt(raw json.RawMessage) int {
	if f, ok := flexNumber(raw); ok {
		return int(f)
	}
	return 0
}

// coerceFlexFloat reads a float that may arrive as a number or a numeric
// string. Unparseable input yields 0.
func coerceFlexFloat(raw json.RawMessage) float64 {
	if f, ok := flexNumber(raw); ok {
		return f
	}
	return 0
}

// flexNumber parses a JSON number or numeric string into a finite float64.
// Non-finite values (NaN/Inf, which strconv.ParseFloat accepts from strings
// like "NaN"/"Inf") are rejected — storing one would later break JSON
// serialization of the whole discovery ("json: unsupported value: NaN").
func flexNumber(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, false
		}
		s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
		return n, true
	}
	return 0, false
}

// coerceFlexIntSlice reads a []int that may arrive as a JSON array of numbers
// or numeric strings (["1","2"] / [1,2]), a single number or numeric string, or
// a comma-joined string ("1,2,3"). Unparseable elements are skipped. A JSON
// null or empty input yields nil. Used for insight source_steps, which smaller
// models often emit with string-typed elements or as a single value. A valid
// array of numbers decodes to the identical slice.
func coerceFlexIntSlice(raw json.RawMessage) []int {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '[' {
		var elems []json.RawMessage
		if json.Unmarshal(raw, &elems) != nil {
			return nil
		}
		out := make([]int, 0, len(elems))
		for _, e := range elems {
			if f, ok := flexNumber(e); ok {
				out = append(out, int(f))
			}
		}
		return out
	}
	if raw[0] == '"' {
		// A single numeric string ("5") or a comma-joined list ("1,2,3").
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		out := make([]int, 0)
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if f, err := strconv.ParseFloat(p, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				out = append(out, int(f))
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if f, ok := flexNumber(raw); ok {
		return []int{int(f)}
	}
	return nil
}

// coerceStringSlice reads a []string that may arrive as a JSON array of strings,
// a single bare string (wrapped into a one-element slice), or an array mixing
// strings with numbers (numbers keep their JSON text). A JSON null or empty
// input yields nil. Used for insight indicators, which smaller models sometimes
// emit as a single string instead of a list. A valid array of strings decodes
// to the identical slice.
func coerceStringSlice(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return []string{s}
	}
	if raw[0] == '[' {
		var elems []json.RawMessage
		if json.Unmarshal(raw, &elems) != nil {
			return nil
		}
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			e = bytes.TrimSpace(e)
			if len(e) == 0 || string(e) == "null" {
				continue
			}
			var s string
			if json.Unmarshal(e, &s) == nil {
				out = append(out, s)
				continue
			}
			// Non-string scalar (number/bool): keep its JSON text so the
			// indicator isn't silently lost.
			out = append(out, string(e))
		}
		return out
	}
	// Bare non-string scalar (number/bool): stringify into a one-element slice.
	return []string{string(raw)}
}

// Impact represents the expected impact of a recommendation.
type Impact struct {
	Metric               string  `bson:"metric" json:"metric"`
	EstimatedImprovement string  `bson:"estimated_improvement" json:"estimated_improvement"`
	Reasoning            string  `bson:"reasoning" json:"reasoning"`
	ReturnRate           float64 `bson:"return_rate,omitempty" json:"return_rate,omitempty"`
	ConversionRate       float64 `bson:"conversion_rate,omitempty" json:"conversion_rate,omitempty"`
	EstimatedValue       float64 `bson:"estimated_value,omitempty" json:"estimated_value,omitempty"`
	TotalValue           float64 `bson:"total_value,omitempty" json:"total_value,omitempty"`
}

// UnmarshalJSON decodes expected_impact tolerantly: it accepts either the
// Impact object or a bare JSON string. Several LLMs describe the expected
// impact as prose (e.g. "improves utilization accuracy and revenue
// forecasting") instead of the structured object; strict decoding of a
// string into this struct fails and — because the whole recommendation
// envelope is decoded in one shot — used to discard the entire batch of
// recommendations (issue #342). A bare string is coerced into
// Impact{Reasoning: <string>}, which is the free-text field that flows into
// the embedding text and the validation digest, so the prose is preserved
// end-to-end rather than dropped.
//
// A JSON null leaves the zero value. Any other shape (number, array, …)
// returns the decode error so the offending recommendation is skipped
// per-item by the recommendation parser rather than silently accepted.
//
// Only JSON decoding is customized; BSON decoding of persisted documents is
// unaffected (the driver uses the struct tags), so already-stored impacts
// read back exactly as before.
func (im *Impact) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*im = Impact{Reasoning: s}
		return nil
	}
	// Object form. Decode through an alias type so this method is not called
	// recursively (the alias has no UnmarshalJSON).
	type impactAlias Impact
	var a impactAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*im = Impact(a)
	return nil
}

// Summary holds the executive summary of a discovery run.
type Summary struct {
	Date                 time.Time `bson:"date" json:"date"`
	Text                 string    `bson:"text" json:"text"`
	KeyFindings          []string  `bson:"key_findings" json:"key_findings"`
	TopRecommendations   []string  `bson:"top_recommendations" json:"top_recommendations"`
	TotalInsights        int       `bson:"total_insights" json:"total_insights"`
	TotalRecommendations int       `bson:"total_recommendations" json:"total_recommendations"`
	QueriesExecuted      int       `bson:"queries_executed" json:"queries_executed"`
	Errors               []string  `bson:"errors,omitempty" json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// LLM Dialog Logs (for traceability and fine-tuning)
// ---------------------------------------------------------------------------

// ExplorationStep represents a single step in the autonomous exploration loop.
// Captures the complete LLM dialog for each step.
type ExplorationStep struct {
	Step      int       `bson:"step" json:"step"`
	Timestamp time.Time `bson:"timestamp" json:"timestamp"`
	// WarehouseID is the datasource this step queried (multi-warehouse). Empty
	// for legacy / single-warehouse runs; carried so each SQL example can be
	// tagged with the datasource it must be validated against.
	WarehouseID string `bson:"warehouse_id,omitempty" json:"warehouse_id,omitempty"`

	// LLM decision
	Action       string `bson:"action" json:"action"` // query_data, lookup_schema, search_tables, complete, complete_rejected
	Thinking     string `bson:"thinking" json:"thinking"`
	QueryPurpose string `bson:"query_purpose,omitempty" json:"query_purpose,omitempty"`

	// Query execution (if action = query_data)
	Query           string                   `bson:"query,omitempty" json:"query,omitempty"`
	QueryResult     []map[string]interface{} `bson:"query_result,omitempty" json:"query_result,omitempty"`
	RowCount        int                      `bson:"row_count,omitempty" json:"row_count,omitempty"`
	ExecutionTimeMs int64                    `bson:"execution_time_ms,omitempty" json:"execution_time_ms,omitempty"`

	// CompactResult is the deterministic digest of QueryResult, built
	// once at exploration time so the analysis phase can render a
	// fixed-size summary instead of inlining every row. Pointer so a
	// step that didn't run a query (lookup_schema, complete_rejected)
	// serializes without an empty digest field.
	CompactResult *gomodels.CompactResult `bson:"compact_result,omitempty" json:"compact_result,omitempty"`

	// Quality carries what the source said about how faithful this step's
	// result is to the query that produced it — rows withheld, values
	// sampled, a tail truncated, fields restricted.
	//
	// Stored on the step because that is the only place it is knowable. The
	// query succeeded and the rows look complete; nothing later in the
	// pipeline can re-derive that some of them were withheld. An analysis
	// reading these rows without this would compute a share of a population
	// it cannot see, and report it as a finding.
	//
	// Empty for every SQL warehouse, which answers exactly what was asked or
	// fails.
	Quality []gowarehouse.QualityCaveat `bson:"quality,omitempty" json:"quality,omitempty"`

	// Error handling
	Error       string `bson:"error,omitempty" json:"error,omitempty"`
	FixAttempts int    `bson:"fix_attempts,omitempty" json:"fix_attempts,omitempty"`
	Fixed       bool   `bson:"fixed,omitempty" json:"fixed,omitempty"`

	// FixHistory is the per-attempt log produced when an initial query
	// failed and the self-healing path called the LLM to repair the SQL.
	// One entry per fix call: the prompt sent, the raw response, the
	// broken SQL, the proposed fix, the warehouse error that triggered
	// the call, and the token / latency accounting.
	//
	// Both successful and failed fix calls are recorded — failed
	// attempts (LLM transport error, unparseable response, post-fix
	// security-filter rejection) carry FixAttempt.FixerError set, and
	// the executor did NOT retry the warehouse with those proposals.
	// Successful (applied) attempts have FixerError empty.
	//
	// FixAttempts counts only applied attempts (those that resulted in
	// a warehouse retry) — so `FixAttempts <= len(FixHistory)`, with
	// the gap being failed-fixer rows. FixAttempts is retained as a
	// fast scalar for dashboards that only need to ask "did self-heal
	// kick in for this step".
	FixHistory []FixAttempt `bson:"fix_history,omitempty" json:"fix_history,omitempty"`

	// Complete LLM dialog (for fine-tuning)
	LLMRequest  string `bson:"llm_request" json:"llm_request"`   // full prompt sent to LLM
	LLMResponse string `bson:"llm_response" json:"llm_response"` // full response from LLM
	TokensIn    int    `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
	TokensOut   int    `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
	DurationMs  int64  `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	IsInsight bool `bson:"is_insight" json:"is_insight"`
}

// FixAttempt is the per-attempt record produced by the self-healing SQL
// fix loop. The exploration engine fills one entry per LLM call the
// fixer made for a given step — including unsuccessful attempts (where
// the proposed SQL still failed) so downstream tooling has visibility
// into the full repair trajectory, not just the last call.
type FixAttempt struct {
	// Step is the parent ExplorationStep.Step number, duplicated here so
	// a flattened export of FixAttempt rows is self-contained without
	// joining back to the step.
	Step int `bson:"step" json:"step"`

	// Attempt is the zero-based retry index inside the step. Matches the
	// `attempt` argument the executor passes to SQLFixer.FixSQL.
	Attempt int `bson:"attempt" json:"attempt"`

	// PromptIn is the fully rendered prompt the fixer sent to the LLM
	// (system instruction + user message, concatenated as the LLM saw
	// it).
	PromptIn string `bson:"prompt_in" json:"prompt_in"`

	// ResponseOut is the raw LLM response text — before SQL extraction —
	// so consumers can see the model's full output, not just the parsed
	// SQL.
	ResponseOut string `bson:"response_out" json:"response_out"`

	// SQLBefore is the broken SQL handed to the fixer.
	SQLBefore string `bson:"sql_before" json:"sql_before"`

	// SQLAfter is the SQL the fixer proposed. Whether it ran cleanly is
	// determined by the subsequent attempt: if attempt N's SQLAfter is
	// attempt N+1's SQLBefore, the proposal still failed; if it's the
	// final attempt and the step has no Error, it succeeded. Empty when
	// the fixer failed to produce any parseable SQL (see FixerError).
	SQLAfter string `bson:"sql_after" json:"sql_after"`

	// ErrorIn is the warehouse error message that triggered this fix
	// call.
	ErrorIn string `bson:"error_in" json:"error_in"`

	// FixerError, when non-empty, captures the reason the fixer's
	// proposal was NOT applied to the next warehouse retry. Three cases
	// produce a non-empty value:
	//   - LLM transport error (network / 5xx / context cancelled).
	//   - Response-extraction failure (model returned no parseable SQL,
	//     e.g. Gemma running to max_tokens with a truncated body).
	//   - Post-fix security-filter rejection (the proposed SQL was valid
	//     but did not carry the required filter clause).
	// When empty, the proposal was applied and the warehouse was retried
	// with SQLAfter. Even with FixerError set, PromptIn / ResponseOut /
	// InputTokens / OutputTokens / DurationMs still reflect the LLM call
	// that was made (when one was made) so downstream tooling can use
	// failed attempts as negative training examples.
	FixerError string `bson:"fixer_error,omitempty" json:"fixer_error,omitempty"`

	InputTokens  int   `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int   `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`
	DurationMs   int64 `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	Timestamp time.Time `bson:"timestamp" json:"timestamp"`
}

// AnalysisStep captures the complete LLM dialog for a single analysis area.
// One per analysis area (churn, engagement, levels, etc.).
type AnalysisStep struct {
	AreaID   string    `bson:"area_id" json:"area_id"`     // "churn", "levels", etc.
	AreaName string    `bson:"area_name" json:"area_name"` // "Churn Risks", "Level Difficulty"
	RunAt    time.Time `bson:"run_at" json:"run_at"`

	// Input
	Prompt          string `bson:"prompt" json:"prompt"`                     // full analysis prompt sent
	RelevantQueries int    `bson:"relevant_queries" json:"relevant_queries"` // how many exploration queries fed in

	// QueryResultsChars is the byte size of the rendered
	// {{QUERY_RESULTS}} block. Useful for debugging prompt size and
	// cross-checking the picker's budget logic against what was
	// actually shipped.
	QueryResultsChars int `bson:"query_results_chars,omitempty" json:"query_results_chars,omitempty"`

	// SelectedSteps records which exploration steps fed this area's
	// analysis prompt and how they were picked (vector vs.
	// exact-match boost). One entry per picked step.
	SelectedSteps []SelectedStep `bson:"selected_steps,omitempty" json:"selected_steps,omitempty"`

	// DroppedSteps records steps the picker considered but excluded —
	// either below the min-score floor or trimmed for budget. The
	// dashboard's debug view surfaces this so a human reviewer can
	// see what the LLM didn't get.
	DroppedSteps []DroppedAnalysisStep `bson:"dropped_steps,omitempty" json:"dropped_steps,omitempty"`

	// LLM output
	Response   string `bson:"response" json:"response"` // full LLM response
	TokensIn   int    `bson:"tokens_in" json:"tokens_in"`
	TokensOut  int    `bson:"tokens_out" json:"tokens_out"`
	DurationMs int64  `bson:"duration_ms" json:"duration_ms"`

	// Parsed results
	Insights []Insight `bson:"insights" json:"insights"`

	// InsightsDroppedParse counts insights skipped because their individual
	// JSON object could not be decoded even after tolerant coercion (the
	// insight analogue of RecommendationsDroppedParse). Kept separate so the
	// per-item parse-failure rate can be measured per LLM provider. Omitted on
	// a clean run.
	InsightsDroppedParse int `bson:"insights_dropped_parse,omitempty" json:"insights_dropped_parse,omitempty"`

	// AnalysisParseRetries counts how many corrective re-prompts this area
	// issued after the model's first response yielded zero parseable insights
	// from a non-empty response (bounded by ANALYSIS_PARSE_MAX_RETRIES). Zero
	// on the happy path; omitted when zero.
	AnalysisParseRetries int `bson:"analysis_parse_retries,omitempty" json:"analysis_parse_retries,omitempty"`

	// Validation
	ValidationResults []ValidationResult `bson:"validation_results,omitempty" json:"validation_results,omitempty"`

	Error string `bson:"error,omitempty" json:"error,omitempty"`
}

// SelectedStep is one step the analysis picker fed to the LLM. Source
// is "vector" or "exact_match"; Score is the cosine similarity (or
// the exact-match floor when promoted).
type SelectedStep struct {
	Step   int     `bson:"step" json:"step"`
	Score  float64 `bson:"score" json:"score"`
	Source string  `bson:"source" json:"source"`
}

// DroppedAnalysisStep is one step the picker excluded. Reason is
// "below_min_score" or "over_budget".
type DroppedAnalysisStep struct {
	Step   int     `bson:"step" json:"step"`
	Score  float64 `bson:"score" json:"score"`
	Reason string  `bson:"reason" json:"reason"`
}

// RecommendationStep captures the complete LLM dialog for recommendation generation.
type RecommendationStep struct {
	RunAt time.Time `bson:"run_at" json:"run_at"`

	// Input
	Prompt       string `bson:"prompt" json:"prompt"`
	InsightCount int    `bson:"insight_count" json:"insight_count"`

	// LLM output
	Response   string `bson:"response" json:"response"`
	TokensIn   int    `bson:"tokens_in" json:"tokens_in"`
	TokensOut  int    `bson:"tokens_out" json:"tokens_out"`
	DurationMs int64  `bson:"duration_ms" json:"duration_ms"`

	// Parsed results
	Recommendations []Recommendation `bson:"recommendations" json:"recommendations"`

	// Status is an optional observability marker. Empty on the
	// regular happy path; set to "skipped_no_eligible_insights" when
	// the orchestrator skips Phase 5 because no insight survived the
	// {supported, confirmed} eligibility filter, or to
	// "recommendation_parse_error" when the phase produced zero
	// recommendations because the LLM response could not be parsed
	// (issue #342). The dashboard renders a clear reason for the empty
	// recommendations section instead of a generic "none found".
	Status string `bson:"status,omitempty" json:"status,omitempty"`

	// Telemetry for recommendations the orchestrator parsed from the
	// LLM response but discarded before persistence because their
	// `related_insight_ids` could not be resolved to an eligible
	// insight. RecommendationsDropped is the total; the per-reason
	// fields break it down so we can measure regression rates per LLM
	// provider (e.g. some models emit category:severity:theme slugs
	// instead of UUIDs and all of those fall into
	// RecommendationsDroppedUnknownID). Zero values are omitted to
	// keep legacy documents from gaining noisy fields when re-read.
	RecommendationsDropped           int `bson:"recommendations_dropped,omitempty" json:"recommendations_dropped,omitempty"`
	RecommendationsDroppedMissingIDs int `bson:"recommendations_dropped_missing_ids,omitempty" json:"recommendations_dropped_missing_ids,omitempty"`
	RecommendationsDroppedUnknownID  int `bson:"recommendations_dropped_unknown_id,omitempty" json:"recommendations_dropped_unknown_id,omitempty"`

	// RecommendationsDroppedParse counts recommendations skipped because
	// their individual JSON object could not be decoded (issue #342 —
	// e.g. a field with the wrong type). It is a subset of
	// RecommendationsDropped, kept separate so the parse-failure rate can
	// be measured independently of the related_insight_ids drops above.
	// Omitted on a clean run.
	RecommendationsDroppedParse int `bson:"recommendations_dropped_parse,omitempty" json:"recommendations_dropped_parse,omitempty"`

	// RecommendationParseRetries counts how many corrective re-prompts the
	// recommendation phase issued after the model's first response yielded
	// zero parseable recommendations (issue #342, bounded by
	// RECOMMENDATION_PARSE_MAX_RETRIES). Zero on the happy path; omitted
	// when zero.
	RecommendationParseRetries int `bson:"recommendation_parse_retries,omitempty" json:"recommendation_parse_retries,omitempty"`

	// Citation-recovery telemetry (#347). Small/open models routinely emit
	// good recommendations but omit or mis-cite `related_insight_ids`; rather
	// than dropping those recs to zero (the old behaviour), the phase now
	// salvages the valid citations, self-heals via a bounded re-prompt, and
	// finally fail-open backfills so a grounded recommendation survives. Big
	// models cite correctly and hit none of these paths, so all three stay
	// zero (and omitted) on a clean run.
	//
	// RecommendationsCitationsSalvaged: recs that cited ≥1 bad id which was
	// trimmed while ≥1 valid id was kept. RecommendationsCitationsBackfilled:
	// recs that had zero valid citations and were linked to the run's eligible
	// insights as a last resort. RecommendationCitationRetries: corrective
	// citation re-prompts issued (bounded by RECOMMENDATION_CITATION_MAX_RETRIES).
	RecommendationsCitationsSalvaged   int `bson:"recommendations_citations_salvaged,omitempty" json:"recommendations_citations_salvaged,omitempty"`
	RecommendationsCitationsBackfilled int `bson:"recommendations_citations_backfilled,omitempty" json:"recommendations_citations_backfilled,omitempty"`
	RecommendationCitationRetries      int `bson:"recommendation_citation_retries,omitempty" json:"recommendation_citation_retries,omitempty"`

	Error string `bson:"error,omitempty" json:"error,omitempty"`
}

// ValidationResult captures warehouse verification for an insight or
// recommendation. The legacy fields are populated by old discoveries;
// the new fields (DocKind, Verifier, Refuter, Combined,
// RefuterDisabled) are populated by the v5 LLM-native verifier.
type ValidationResult struct {
	InsightID    string `bson:"insight_id" json:"insight_id"`
	AnalysisArea string `bson:"analysis_area" json:"analysis_area"`
	// WarehouseID is the datasource this doc was verified against
	// (multi-warehouse) — the datasource the insight/recommendation is
	// about. Empty on a single-warehouse run.
	WarehouseID string    `bson:"warehouse_id,omitempty" json:"warehouse_id,omitempty"`
	ValidatedAt time.Time `bson:"validated_at" json:"validated_at"`

	// What was claimed (legacy)
	ClaimedCount  int    `bson:"claimed_count" json:"claimed_count"`
	ClaimedMetric string `bson:"claimed_metric,omitempty" json:"claimed_metric,omitempty"`

	// What the warehouse returned (legacy)
	VerifiedCount int    `bson:"verified_count" json:"verified_count"`
	Query         string `bson:"query" json:"query"`
	QueryError    string `bson:"query_error,omitempty" json:"query_error,omitempty"`

	// Assessment (legacy)
	Status    string `bson:"status" json:"status"`
	Reasoning string `bson:"reasoning" json:"reasoning"`

	// Per-insight LLM token usage (legacy field, also populated by the
	// new verifier as the sum of verifier+refuter tokens).
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`

	// --- new-shape fields ---

	// DocKind discriminates "insight" from "recommendation" so the
	// dashboard can render the right view. Empty on legacy docs.
	DocKind valmodels.DocKind `bson:"doc_kind,omitempty" json:"doc_kind,omitempty"`

	// Verifier carries the structured defender verdict (claims +
	// per-claim evidence + overall).
	Verifier *valmodels.StructuredVerdict `bson:"verifier,omitempty" json:"verifier,omitempty"`

	// Refuter carries the structured skeptic verdict. Nil when
	// RefuterDisabled or when the refuter run failed at transport.
	Refuter *valmodels.StructuredVerdict `bson:"refuter,omitempty" json:"refuter,omitempty"`

	// Combined is the merge of Verifier and Refuter via Combine().
	Combined valmodels.Status `bson:"combined,omitempty" json:"combined,omitempty"`

	// RefuterDisabled distinguishes "refuter intentionally not run"
	// from "refuter expected but missing" — see Combine().
	RefuterDisabled bool `bson:"refuter_disabled,omitempty" json:"refuter_disabled,omitempty"`
}

// ---------------------------------------------------------------------------
// Supporting types
// ---------------------------------------------------------------------------

// SQLMetadata represents metadata about a SQL query that produced an insight.
type SQLMetadata struct {
	Query           string    `bson:"query" json:"query"`
	ExecutionTimeMs int64     `bson:"execution_time_ms" json:"execution_time_ms"`
	RowsReturned    int       `bson:"rows_returned" json:"rows_returned"`
	ExecutedAt      time.Time `bson:"executed_at" json:"executed_at"`
}

// QueryHistory tracks queries executed during discovery.
type QueryHistory struct {
	Query           string    `bson:"query" json:"query"`
	Purpose         string    `bson:"purpose" json:"purpose"`
	ExecutedAt      time.Time `bson:"executed_at" json:"executed_at"`
	Success         bool      `bson:"success" json:"success"`
	Error           string    `bson:"error,omitempty" json:"error,omitempty"`
	FixAttempts     int       `bson:"fix_attempts,omitempty" json:"fix_attempts,omitempty"`
	RowsReturned    int       `bson:"rows_returned,omitempty" json:"rows_returned,omitempty"`
	ExecutionTimeMs int64     `bson:"execution_time_ms,omitempty" json:"execution_time_ms,omitempty"`
}
