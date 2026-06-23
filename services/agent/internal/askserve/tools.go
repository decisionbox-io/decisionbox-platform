package askserve

import (
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

func toolQueryData() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name: string(actQuery),
		Description: "Run one read-only SQL query (SELECT / CTE only) against the data warehouse and observe a summary of the result " +
			"(row count, columns, and a small row preview). This is how you gather evidence. For totals, counts, or distributions write " +
			"aggregate SQL (COUNT/SUM/AVG/GROUP BY) rather than paging raw rows. If you don't yet know the tables, start with a discovery " +
			"query against INFORMATION_SCHEMA.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":   map[string]interface{}{"type": "string", "description": "The read-only SQL to execute."},
				"purpose": map[string]interface{}{"type": "string", "description": "Short note on what this query answers (optional)."},
			},
			"required": []string{"query"},
		},
	}
}

func toolLookupSchema() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name:        string(actLookup),
		Description: "Fetch the columns (and types) for one or more known tables. Use this when you know which tables you need but not their exact columns.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"tables": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Fully-qualified table names (e.g. dataset.table).",
				},
			},
			"required": []string{"tables"},
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
// only when a schema provider is wired.
func toolsForPhase(grounded, hasSchema bool) []gollm.ToolDefinition {
	tools := []gollm.ToolDefinition{toolQueryData()}
	if hasSchema {
		tools = append(tools, toolLookupSchema(), toolSearchTables())
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
// emission) are reused unchanged. Returns nil for an unrecognised tool name.
func toolCallToAction(tc gollm.ToolCall) *turnAction {
	getStr := func(k string) string {
		if v, ok := tc.Input[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	act := &turnAction{}
	switch actionKind(tc.Name) {
	case actQuery:
		act.Kind = actQuery
		if v, ok := tc.Input["query"].(string); ok {
			act.Query = v // do not trim SQL
		}
		act.Purpose = getStr("purpose")
	case actLookup:
		act.Kind = actLookup
		act.LookupSchema = toStringSlice(tc.Input["tables"])
	case actSearch:
		act.Kind = actSearch
		act.SearchTables = getStr("query")
		act.SearchTopK = toInt(tc.Input["top_k"])
	case actAnswer:
		act.Kind = actAnswer
		act.Text = getStr("text")
	case actClarify:
		act.Kind = actClarify
		act.Text = getStr("question")
	case actDecline:
		act.Kind = actDecline
		act.Text = getStr("reason")
	default:
		return nil
	}
	return act
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
