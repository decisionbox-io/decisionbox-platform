package discovery

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// questionsResponseFormatName is the schema identifier sent to providers that
// name their structured-output schema (OpenAI json_schema, the forced tool name
// on the Anthropic wire).
const questionsResponseFormatName = "clarifying_questions"

// questionsResponseFormat returns the structured-output request for the
// clarifying-questions phase. Attached to the LLM call on providers that
// support structured output (ChatWithFormat self-gates on
// SupportsStructuredOutput, so it is a safe no-op on vertex-ai / azure-foundry /
// ollama, where the tolerant parser is the always-on net).
//
// Strict is left false on purpose (mirrors recommendationResponseFormat): the
// shape is closed, but OpenAI strict mode requires every property to be
// `required` and forbids the open-ended objects we keep permissive. A typed
// schema plus the tolerant parser pins the shape where it can and nets the rest.
func questionsResponseFormat() *gollm.ResponseFormat {
	return &gollm.ResponseFormat{
		Name:   questionsResponseFormatName,
		Schema: questionsResponseSchema(),
		Strict: false,
	}
}

// questionsResponseSchema is the curated JSON Schema (draft 2020-12) for the
// clarifying-questions envelope. It describes only the input-contract subset the
// LLM produces — deliberately excluding server-assigned fields (id, project_id,
// run_id, discovery_id, status, answer*, normalized_key, timestamps) so the
// generation contract cannot drift into asking the model for them. Property
// names track models.DiscoveryQuestion / QuestionTarget / QuestionOption json
// tags via TestQuestionsSchema_MatchesStructTags.
func questionsResponseSchema() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}

	questionProps := map[string]interface{}{
		"question":  str("The clarifying question, grounded in a specific finding/table/column"),
		"rationale": str("One line: why we're asking — the uncertainty this resolves"),
		"linked_target": map[string]interface{}{
			"type":        "object",
			"description": "The finding this question is about, so the dashboard can link back to it",
			"properties": map[string]interface{}{
				"type": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"insight", "recommendation", "table", "area"},
					"description": "What the question targets",
				},
				"id": str("The insight/recommendation UUID (copied verbatim from the input), or the fully-qualified table name, or the analysis area id"),
			},
			"required": []interface{}{"type", "id"},
		},
		"answer_type": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"boolean", "single_choice", "multi_choice", "free_text"},
			"description": "The simplest sufficient answer format. Prefer boolean > single_choice/multi_choice > free_text. Use free_text only when the answer genuinely cannot be enumerated.",
		},
		"options": map[string]interface{}{
			"type":        "array",
			"description": "For single_choice / multi_choice only: the concrete, mutually-distinct options grounded in the data (max ~5). Do NOT include an 'Other' option — the server adds one automatically.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":    str("Short stable option id (e.g. a slug)"),
					"label": str("Human-readable option label"),
				},
				"required": []interface{}{"id", "label"},
			},
		},
	}

	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questions": map[string]interface{}{
				"type":        "array",
				"description": "The clarifying questions. Empty when nothing is genuinely uncertain.",
				"items": map[string]interface{}{
					"type":       "object",
					"properties": questionProps,
					"required":   []interface{}{"question", "rationale", "linked_target", "answer_type"},
				},
			},
		},
		"required": []interface{}{"questions"},
	}
}
