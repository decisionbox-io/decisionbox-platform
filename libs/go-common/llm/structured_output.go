package llm

import "encoding/json"

// defaultStructuredToolName is the synthetic tool name used when a
// ResponseFormat carries no Name. Providers on the Anthropic wire (which
// has no response_format field) satisfy ResponseFormat by forcing a
// single tool whose input schema is the requested schema; this is the
// tool's name when the caller did not supply one.
const defaultStructuredToolName = "json_response"

// ApplyResponseFormatAsTool rewrites req so that a ResponseFormat is
// expressed as a single forced tool. It exists for wires that cannot take
// a native response_format (the Anthropic Messages API): forcing a tool
// whose input_schema is the requested schema is Anthropic's supported way
// to get a guaranteed-shape JSON object out of the model.
//
// It is a no-op — returning the request unchanged and injected=false —
// when any of these hold, so it can be called unconditionally at the top
// of an Anthropic-wire Chat:
//   - req.ResponseFormat is nil (no structured output requested);
//   - req.Tools is non-empty (the caller's own tools win — ResponseFormat
//     must never disturb an existing tool-using flow);
//   - the schema is empty.
//
// When it does inject, it sets req.Tools to the single synthetic tool and
// req.ToolChoice to that tool's name (forcing the model to call it), and
// returns injected=true so the caller pairs it with
// NormalizeStructuredToolResponse on the reply.
func ApplyResponseFormatAsTool(req ChatRequest) (ChatRequest, bool) {
	rf := req.ResponseFormat
	if rf == nil || len(rf.Schema) == 0 || len(req.Tools) > 0 {
		return req, false
	}
	name := rf.Name
	if name == "" {
		name = defaultStructuredToolName
	}
	req.Tools = []ToolDefinition{{
		Name:        name,
		Description: "Return the response as a single JSON object that conforms to the input schema. Call this tool exactly once with the complete object.",
		InputSchema: rf.Schema,
	}}
	req.ToolChoice = name
	return req, true
}

// NormalizeStructuredToolResponse folds a forced-tool reply back into
// ChatResponse.Content so callers parse output uniformly and never have
// to know a tool was used to satisfy their ResponseFormat.
//
// It only acts when injected is true (i.e. ApplyResponseFormatAsTool
// injected the synthetic tool). If the model called the tool, its Input
// map is marshalled to JSON and written to Content, and ToolCalls is
// cleared. If the model returned text instead of calling the tool (rare
// under a forced tool_choice), Content is left as-is so the caller's own
// parser still sees whatever was produced.
func NormalizeStructuredToolResponse(resp *ChatResponse, injected bool) {
	if !injected || resp == nil {
		return
	}
	for _, tc := range resp.ToolCalls {
		if len(tc.Input) == 0 {
			continue
		}
		b, err := json.Marshal(tc.Input)
		if err != nil {
			continue
		}
		resp.Content = string(b)
		resp.ToolCalls = nil
		return
	}
}
