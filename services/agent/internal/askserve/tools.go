package askserve

import (
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// This file defines the native tool-calling vocabulary for the Q&A loop. The
// tool names match the actionKind wire strings so the tool path, the JSON-text
// fallback, and the persisted ToolEvents all share one vocabulary. On providers
// whose ProviderMeta.SupportsTools is true (bedrock / claude / openai) the loop
// drives the model with these definitions instead of asking it to emit JSON in
// free text — the provider then *guarantees* the model either calls a tool or
// finishes, which is what makes grounding structural rather than coaxed.

// toolQueryData defines query_data. On a multi-datasource project (multi=true)
// it exposes a datasource_id argument so each statement names the datasource it
// runs against — one warehouse per statement, chained across a turn. On a
// single-datasource / pinned turn the argument is omitted so behaviour is
// identical to the single-warehouse path.
func toolQueryData(multi bool) gollm.ToolDefinition {
	desc := "Run one read-only SQL query (SELECT / CTE only) against the data warehouse and observe a summary of the result " +
		"(row count, columns, and a small row preview). This is how you gather evidence. For totals, counts, or distributions write " +
		"aggregate SQL (COUNT/SUM/AVG/GROUP BY) rather than paging raw rows. If you don't yet know the tables, start with a discovery " +
		"query against INFORMATION_SCHEMA."
	props := map[string]interface{}{
		"query":   map[string]interface{}{"type": "string", "description": "The read-only SQL to execute."},
		"purpose": map[string]interface{}{"type": "string", "description": "Short note on what this query answers (optional)."},
	}
	if multi {
		desc += " This project has multiple datasources — set datasource_id to the one this query runs against (a single query cannot span datasources; " +
			"to combine datasources, query one, then use its result values as literal filters in a follow-up query on another)."
		props["datasource_id"] = map[string]interface{}{"type": "string", "description": "The datasource this query runs against (see the DATASOURCES list). Defaults to the primary if omitted."}
	}
	return gollm.ToolDefinition{
		Name:        string(actQuery),
		Description: desc,
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   []string{"query"},
		},
	}
}

func toolLookupSchema(multi bool) gollm.ToolDefinition {
	desc := "Fetch the columns (and types) for one or more known tables. Use this when you know which tables you need but not their exact columns."
	props := map[string]interface{}{
		"tables": map[string]interface{}{
			"type":        "array",
			"items":       map[string]interface{}{"type": "string"},
			"description": "Fully-qualified table names (e.g. dataset.table).",
		},
	}
	if multi {
		desc += " Set datasource_id to the datasource that owns these tables (from a search_tables result)."
		props["datasource_id"] = map[string]interface{}{"type": "string", "description": "The datasource that owns the tables. Defaults to the primary if omitted."}
	}
	return gollm.ToolDefinition{
		Name:        string(actLookup),
		Description: desc,
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   []string{"tables"},
		},
	}
}

func toolSearchTables() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name:        string(actSearch),
		Description: "Semantically search the indexed schema for tables relevant to a description. Use this first when you don't know which tables hold what you need.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Keywords describing the data you're looking for."},
				"top_k": map[string]interface{}{"type": "integer", "description": "Max number of tables to return (optional)."},
			},
			"required": []string{"query"},
		},
	}
}

func toolSearchInsights() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name: string(actSearchInsights),
		Description: "Semantic search over the project's already-discovered insights and recommendations (what prior analysis found and advised). " +
			"Prefer this for questions like \"what did we find\", \"what are the risks\", or \"what do you recommend\" — it returns grounded findings without running SQL. " +
			"Use query_data instead for fresh ad-hoc numbers; combine both when a finding needs a current figure.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Keywords describing the finding or recommendation you're looking for."},
				"limit": map[string]interface{}{"type": "integer", "description": "Max hits to return (optional; default 5, capped at 20)."},
			},
			"required": []string{"query"},
		},
	}
}

func toolAnswer() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name:        string(actAnswer),
		Description: "Give the final, grounded answer to the user. Only available after you have run at least one query or schema lookup — every figure must come from a result you observed this turn.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"text": map[string]interface{}{"type": "string", "description": "The final answer, concise analyst-style prose referencing the figures you found."},
			},
			"required": []string{"text"},
		},
	}
}

func toolClarify() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name:        string(actClarify),
		Description: "Ask the user a single clarifying question when the request is too ambiguous to turn into a query.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"question": map[string]interface{}{"type": "string", "description": "One clarifying question."},
			},
			"required": []string{"question"},
		},
	}
}

func toolDecline() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name:        string(actDecline),
		Description: "Decline when the question genuinely cannot be answered from this warehouse. Prefer a discovery query before declining.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "Why this cannot be answered from the data."},
			},
			"required": []string{"reason"},
		},
	}
}

// toolsForPhase returns the tool set offered for the current step. Until the
// turn is grounded (no tool event has run yet) the `answer` tool is deliberately
// withheld so the model cannot finish with an ungrounded answer — paired with
// toolChoiceForPhase("any") this makes fabrication impossible by construction.
// clarify / decline stay available so a genuinely ambiguous or unanswerable
// question can still terminate without inventing data. Schema tools are offered
// only when a schema provider is wired; search_insights only when an insights
// provider is wired.
func toolsForPhase(grounded, hasSchema, hasInsights, multi bool) []gollm.ToolDefinition {
	tools := []gollm.ToolDefinition{toolQueryData(multi)}
	if hasSchema {
		tools = append(tools, toolLookupSchema(multi), toolSearchTables())
	}
	if hasInsights {
		tools = append(tools, toolSearchInsights())
	}
	if grounded {
		tools = append(tools, toolAnswer())
	}
	tools = append(tools, toolClarify(), toolDecline())
	return tools
}

// toolChoiceForPhase forces a tool call while ungrounded ("any") so the model
// must gather evidence (or explicitly clarify/decline) before it can answer;
// once grounded it relaxes to "auto" so the model may keep querying or finish.
func toolChoiceForPhase(grounded bool) string {
	if grounded {
		return "auto"
	}
	return "any"
}

// toolCallToAction maps a native tool call to the loop's internal turnAction so
// the existing execQuery / execLookup / execSearch handlers (and their ToolEvent
// emission) are reused unchanged. It validates required arguments and returns an
// error for an unknown tool or a missing/empty required field — the caller feeds
// that back as an error tool_result WITHOUT running the tool, so a malformed call
// never emits a (spuriously grounding) event. Mirrors the text parser, which
// likewise rejects an empty query.
func toolCallToAction(tc gollm.ToolCall) (*turnAction, error) {
	getStr := func(k string) string {
		if v, ok := tc.Input[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	switch actionKind(tc.Name) {
	case actQuery:
		q := ""
		if v, ok := tc.Input["query"].(string); ok {
			q = v // do not trim SQL
		}
		if strings.TrimSpace(q) == "" {
			return nil, fmt.Errorf("query_data requires a non-empty %q argument", "query")
		}
		return &turnAction{Kind: actQuery, Query: q, Purpose: getStr("purpose"), Datasource: getStr("datasource_id")}, nil
	case actLookup:
		tables := toStringSlice(tc.Input["tables"])
		if len(tables) == 0 {
			return nil, fmt.Errorf("lookup_schema requires a non-empty %q array", "tables")
		}
		return &turnAction{Kind: actLookup, LookupSchema: tables, Datasource: getStr("datasource_id")}, nil
	case actSearch:
		q := getStr("query")
		if q == "" {
			return nil, fmt.Errorf("search_tables requires a non-empty %q argument", "query")
		}
		return &turnAction{Kind: actSearch, SearchTables: q, SearchTopK: toInt(tc.Input["top_k"])}, nil
	case actSearchInsights:
		q := getStr("query")
		if q == "" {
			return nil, fmt.Errorf("search_insights requires a non-empty %q argument", "query")
		}
		return &turnAction{Kind: actSearchInsights, SearchInsights: q, InsightsLimit: toInt(tc.Input["limit"])}, nil
	case actAnswer:
		txt := getStr("text")
		if txt == "" {
			return nil, fmt.Errorf("answer requires a non-empty %q argument", "text")
		}
		return &turnAction{Kind: actAnswer, Text: txt}, nil
	case actClarify:
		txt := getStr("question")
		if txt == "" {
			return nil, fmt.Errorf("clarify requires a non-empty %q argument", "question")
		}
		return &turnAction{Kind: actClarify, Text: txt}, nil
	case actDecline:
		// A reason is helpful but not strictly required — decline is a valid
		// terminal even with an empty body.
		return &turnAction{Kind: actDecline, Text: getStr("reason")}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", tc.Name)
	}
}

func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
