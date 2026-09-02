package askserve

import (
	"encoding/json"
	"fmt"
	"strings"
)

// actionKind is the one decision the model makes per round.
type actionKind string

const (
	actQuery          actionKind = "query_data"
	actLookup         actionKind = "lookup_schema"
	actSearch         actionKind = "search_tables"
	actSearchInsights actionKind = "search_insights"
	actRenderChart    actionKind = "render_chart"
	actAnswer         actionKind = "answer"
	actClarify        actionKind = "clarify"
	actDecline        actionKind = "decline"
)

// terminal reports whether an action ends the turn. render_chart is NOT
// terminal: it is an evidence-consuming side action (the model charts a prior
// query result, then answers in a later step), so the turn continues after it.
func (k actionKind) terminal() bool {
	return k == actAnswer || k == actClarify || k == actDecline
}

// turnAction is one parsed step of the Q&A loop. Exactly one Kind is set per
// round; the parser picks the mode from which JSON keys are present.
type turnAction struct {
	Kind     actionKind
	Thinking string

	Query   string // query_data
	Purpose string // query_data (optional)
	// Datasource is the target datasource (warehouse) id for query_data /
	// lookup_schema on a multi-datasource project. Empty = the primary. Ignored
	// on a single-datasource project or a turn pinned to one datasource.
	Datasource string
	// JoinsOn is the model's optional declaration, on a query_data action, that
	// this query filters on values observed in an earlier query against a
	// different datasource. nil when it declared none.
	JoinsOn *joinDeclaration

	LookupSchema []string // lookup_schema
	SearchTables string   // search_tables
	SearchTopK   int      // search_tables (optional)

	SearchInsights string // search_insights (query)
	InsightsLimit  int    // search_insights (optional)

	Chart json.RawMessage // render_chart (the raw ChartSpec input, validated later)

	Text string // answer / clarify / decline body
}

// rawAction mirrors every key the Q&A vocabulary understands. The loop's
// system prompt asks the model to emit exactly one JSON object using these
// keys; tool-use-trained models that wrap the call in {"name","input"} are
// normalised by normaliseToolEnvelope before dispatch.
type rawAction struct {
	Thinking string `json:"thinking"`

	Query      string          `json:"query"`
	Purpose    string          `json:"purpose"`
	Datasource string          `json:"datasource_id"`
	JoinsOn    json.RawMessage `json:"joins_on"`

	LookupSchema []string `json:"lookup_schema"`
	SearchTables string   `json:"search_tables"`
	SearchTopK   int      `json:"search_top_k"`

	SearchInsights string `json:"search_insights"`
	InsightsLimit  int    `json:"insights_limit"`

	RenderChart json.RawMessage `json:"render_chart"`

	Answer  string `json:"answer"`
	Clarify string `json:"clarify"`
	Decline string `json:"decline"`
}

// parseTurnAction extracts the model's action from a (possibly noisy) LLM
// response. It mirrors the agent exploration parser's robustness — fenced
// blocks and bare balanced objects are both considered, the last candidate
// carrying a recognised key wins, and a tool-use envelope is normalised —
// but over the Q&A vocabulary (adds answer/clarify/decline, drops the
// discovery-only "done" shape). Returns an error when no action can be
// parsed so the caller can re-prompt rather than silently terminate.
func parseTurnAction(response string) (*turnAction, error) {
	jsonStr := extractActionJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no action JSON object found in response")
	}

	var raw rawAction
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse action JSON: %w", err)
	}
	normaliseToolEnvelope(jsonStr, &raw)

	act := &turnAction{Thinking: strings.TrimSpace(raw.Thinking)}

	// A chart mixed with ANY other action in one payload is rejected rather than
	// silently dropped: the model must emit the chart in its own step (it charts a
	// PRIOR query result — mixing it with a fresh query would drop that query or
	// let the chart reference a stale step, and mixing it with a terminal would
	// finish before the chart is validated). This mirrors the native path's
	// same-batch refusal.
	if hasJSONPayload(raw.RenderChart) && rawHasNonChartAction(&raw) {
		return nil, fmt.Errorf("emit render_chart on its own — not together with a query, lookup, search, or answer/clarify/decline; render the chart in one step, then continue in the next")
	}

	switch {
	case hasJSONPayload(raw.RenderChart):
		act.Kind = actRenderChart
		act.Chart = raw.RenderChart
	case strings.TrimSpace(raw.Answer) != "":
		act.Kind = actAnswer
		act.Text = strings.TrimSpace(raw.Answer)
	case strings.TrimSpace(raw.Decline) != "":
		act.Kind = actDecline
		act.Text = strings.TrimSpace(raw.Decline)
	case strings.TrimSpace(raw.Clarify) != "":
		act.Kind = actClarify
		act.Text = strings.TrimSpace(raw.Clarify)
	case strings.TrimSpace(raw.Query) != "":
		act.Kind = actQuery
		act.Query = raw.Query
		act.Purpose = strings.TrimSpace(raw.Purpose)
		act.Datasource = strings.TrimSpace(raw.Datasource)
		joins, err := raw.joinDeclaration()
		if err != nil {
			return nil, err
		}
		act.JoinsOn = joins
	case len(raw.LookupSchema) > 0:
		act.Kind = actLookup
		act.LookupSchema = raw.LookupSchema
		act.Datasource = strings.TrimSpace(raw.Datasource)
	case strings.TrimSpace(raw.SearchTables) != "":
		act.Kind = actSearch
		act.SearchTables = strings.TrimSpace(raw.SearchTables)
		act.SearchTopK = raw.SearchTopK
	case strings.TrimSpace(raw.SearchInsights) != "":
		act.Kind = actSearchInsights
		act.SearchInsights = strings.TrimSpace(raw.SearchInsights)
		act.InsightsLimit = raw.InsightsLimit
	default:
		return nil, fmt.Errorf("action JSON has no answer, clarify, decline, query, lookup_schema, search_tables, search_insights, or render_chart")
	}
	return act, nil
}

// hasJSONPayload reports whether a raw JSON value carries an actual payload
// (not absent, not JSON null). Guards the parser against a `{"render_chart":null}`
// or `{"joins_on":null}` stub being treated as the thing itself.
func hasJSONPayload(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

// rawHasNonChartAction reports whether the payload carries any action key other
// than render_chart — used to reject a chart mixed with another action.
func rawHasNonChartAction(raw *rawAction) bool {
	return strings.TrimSpace(raw.Answer) != "" ||
		strings.TrimSpace(raw.Decline) != "" ||
		strings.TrimSpace(raw.Clarify) != "" ||
		strings.TrimSpace(raw.Query) != "" ||
		len(raw.LookupSchema) > 0 ||
		strings.TrimSpace(raw.SearchTables) != "" ||
		strings.TrimSpace(raw.SearchInsights) != ""
}

// normaliseToolEnvelope detects an Anthropic/OpenAI tool-use envelope
// (`{"name":"...","input":{...}}`) and rewrites the key-driven fields so the
// dispatch in parseTurnAction handles both shapes. Key-driven fields already
// set win on conflict, so a malformed envelope can't override a clean payload.
func normaliseToolEnvelope(jsonStr string, raw *rawAction) {
	var env struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &env); err != nil || env.Name == "" {
		return
	}
	switch actionKind(env.Name) {
	case actQuery:
		if raw.Query != "" {
			return
		}
		var in struct {
			Query        string          `json:"query"`
			Purpose      string          `json:"purpose"`
			DatasourceID string          `json:"datasource_id"`
			JoinsOn      json.RawMessage `json:"joins_on"`
		}
		if json.Unmarshal(env.Input, &in) == nil {
			raw.Query = in.Query
			if raw.Purpose == "" {
				raw.Purpose = in.Purpose
			}
			if raw.Datasource == "" {
				raw.Datasource = in.DatasourceID
			}
			if len(raw.JoinsOn) == 0 {
				raw.JoinsOn = in.JoinsOn
			}
		}
	case actLookup:
		if len(raw.LookupSchema) > 0 {
			return
		}
		var in struct {
			Tables       []string `json:"tables"`
			DatasourceID string   `json:"datasource_id"`
		}
		if json.Unmarshal(env.Input, &in) == nil {
			raw.LookupSchema = in.Tables
			if raw.Datasource == "" {
				raw.Datasource = in.DatasourceID
			}
		}
	case actSearch:
		if raw.SearchTables != "" {
			return
		}
		var in struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if json.Unmarshal(env.Input, &in) == nil {
			raw.SearchTables = in.Query
			if raw.SearchTopK == 0 {
				raw.SearchTopK = in.TopK
			}
		}
	case actSearchInsights:
		if raw.SearchInsights != "" {
			return
		}
		var in struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if json.Unmarshal(env.Input, &in) == nil {
			raw.SearchInsights = in.Query
			if raw.InsightsLimit == 0 {
				raw.InsightsLimit = in.Limit
			}
		}
	case actRenderChart:
		if hasJSONPayload(raw.RenderChart) {
			return
		}
		// The tool-use `input` IS the ChartSpec — capture it raw for validation.
		if len(env.Input) > 0 {
			raw.RenderChart = env.Input
		}
	case actAnswer, actClarify, actDecline:
		var in struct {
			Text   string `json:"text"`
			Answer string `json:"answer"`
		}
		_ = json.Unmarshal(env.Input, &in)
		text := in.Answer
		if text == "" {
			text = in.Text
		}
		switch actionKind(env.Name) {
		case actAnswer:
			if raw.Answer == "" {
				raw.Answer = text
			}
		case actClarify:
			if raw.Clarify == "" {
				raw.Clarify = text
			}
		case actDecline:
			if raw.Decline == "" {
				raw.Decline = text
			}
		}
	}
}

// extractActionJSON returns the most likely action object from the response:
// every fenced block whose body starts with '{' plus every balanced
// top-level object, with the last candidate carrying a recognised action key
// preferred (so a reasoning preamble can't hijack parsing). Falls back to the
// last balanced object overall.
func extractActionJSON(text string) string {
	var candidates []string
	candidates = append(candidates, fencedJSONBlocks(text)...)
	candidates = append(candidates, balancedJSONObjects(text)...)
	if len(candidates) == 0 {
		return ""
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if jsonHasActionKey(candidates[i]) {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

func fencedJSONBlocks(text string) []string {
	var out []string
	for rest := text; ; {
		idx := strings.Index(rest, "```")
		if idx < 0 {
			break
		}
		after := rest[idx+3:]
		if nl := strings.IndexByte(after, '\n'); nl >= 0 {
			lang := strings.TrimSpace(after[:nl])
			if lang == "" || strings.EqualFold(lang, "json") {
				after = after[nl+1:]
			}
		}
		end := strings.Index(after, "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(after[:end])
		if strings.HasPrefix(block, "{") {
			out = append(out, block)
		}
		rest = after[end+3:]
	}
	return out
}

// balancedJSONObjects returns every balanced top-level {...} substring,
// tracking string literals so braces inside a SQL string don't break the
// brace count.
func balancedJSONObjects(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		depth, inString, escaped := 0, false, false
		for j := i; j < len(text); j++ {
			c := text[j]
			if inString {
				switch {
				case escaped:
					escaped = false
				case c == '\\':
					escaped = true
				case c == '"':
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					out = append(out, text[i:j+1])
					i = j
					goto next
				}
			}
		}
		break
	next:
	}
	return out
}

func jsonHasActionKey(s string) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &probe) != nil {
		return false
	}
	for _, k := range []string{"answer", "clarify", "decline", "query", "lookup_schema", "search_tables", "search_insights", "render_chart", "name"} {
		if _, ok := probe[k]; ok {
			return true
		}
	}
	return false
}

// joinDeclaration reads the optional joins_on key of a JSON-text query action.
//
// It decodes to the same loose map the native tool path receives and hands it
// to the same parser, rather than to a typed struct. A struct silently drops
// keys it does not know, so a real attempt spelled `{"step":..,"column":..}`
// would arrive indistinguishable from no attempt at all: the model would get
// no correction, and the turn would be filed as an undeclared hop — which is
// the measurement deciding whether undeclared hops should eventually be
// refused. Misreading attempts as non-attempts is the one error that
// measurement cannot survive.
func (raw *rawAction) joinDeclaration() (*joinDeclaration, error) {
	if !hasJSONPayload(raw.JoinsOn) { // absent, or an explicit null
		return nil, nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw.JoinsOn, &obj); err != nil {
		return nil, fmt.Errorf("joins_on must be an object with %q and %q", "source_step", "field")
	}
	return joinsFromToolInput(obj)
}
