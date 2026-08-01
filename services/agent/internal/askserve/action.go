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
	actAnswer         actionKind = "answer"
	actClarify        actionKind = "clarify"
	actDecline        actionKind = "decline"
)

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

	LookupSchema []string // lookup_schema
	SearchTables string   // search_tables
	SearchTopK   int      // search_tables (optional)

	SearchInsights string // search_insights (query)
	InsightsLimit  int    // search_insights (optional)

	Text string // answer / clarify / decline body
}

// rawAction mirrors every key the Q&A vocabulary understands. The loop's
// system prompt asks the model to emit exactly one JSON object using these
// keys; tool-use-trained models that wrap the call in {"name","input"} are
// normalised by normaliseToolEnvelope before dispatch.
type rawAction struct {
	Thinking string `json:"thinking"`

	Query      string `json:"query"`
	Purpose    string `json:"purpose"`
	Datasource string `json:"datasource_id"`

	LookupSchema []string `json:"lookup_schema"`
	SearchTables string   `json:"search_tables"`
	SearchTopK   int      `json:"search_top_k"`

	SearchInsights string `json:"search_insights"`
	InsightsLimit  int    `json:"insights_limit"`

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

	switch {
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
		return nil, fmt.Errorf("action JSON has no answer, clarify, decline, query, lookup_schema, search_tables, or search_insights")
	}
	return act, nil
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
			Query        string `json:"query"`
			Purpose      string `json:"purpose"`
			DatasourceID string `json:"datasource_id"`
		}
		if json.Unmarshal(env.Input, &in) == nil {
			raw.Query = in.Query
			if raw.Purpose == "" {
				raw.Purpose = in.Purpose
			}
			if raw.Datasource == "" {
				raw.Datasource = in.DatasourceID
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
	for _, k := range []string{"answer", "clarify", "decline", "query", "lookup_schema", "search_tables", "search_insights", "name"} {
		if _, ok := probe[k]; ok {
			return true
		}
	}
	return false
}
