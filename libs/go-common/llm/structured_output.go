package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
	// The name doubles as ToolChoice below, which providers interpret via
	// their tool-choice mapping. A name that collides with a reserved
	// tool-choice token ("auto"/"any"/"required"/"none") would be read as a
	// MODE (e.g. "none" → don't call any tool) and silently disable forced
	// tool use, so fall back to the default synthetic name in that case.
	name := rf.Name
	if name == "" || isReservedToolChoice(name) {
		name = defaultStructuredToolName
	}
	req.Tools = []ToolDefinition{{
		Name:        name,
		Description: "Return the response as a single JSON object that conforms to the input schema. Call this tool exactly once with the complete object.",
		InputSchema: rf.Schema,
	}}
	req.ToolChoice = name
	// The structured answer is a single object, so forbid parallel tool use
	// where the provider supports it (Anthropic) — otherwise Claude could
	// legally return several calls to the synthetic tool and the fold-back
	// would have to reject them.
	req.DisableParallelToolUse = true
	return req, true
}

// isReservedToolChoice reports whether s is one of the wire-neutral
// tool-choice mode tokens (see ChatRequest.ToolChoice). A tool named like
// one of these must not be used as a forced tool name.
func isReservedToolChoice(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "any", "required", "none":
		return true
	}
	return false
}

// NormalizeStructuredToolResponse folds a forced-tool reply back into
// ChatResponse.Content so callers parse output uniformly and never have
// to know a tool was used to satisfy their ResponseFormat.
//
// It only acts when injected is true (i.e. ApplyResponseFormatAsTool
// injected the synthetic tool). If the model called the tool exactly once,
// its Input map is marshalled to JSON and written to Content, ToolCalls is
// cleared, and the "tool_use" stop reason is normalised to a terminal turn.
// If the model returned text instead of calling the tool (rare under a
// forced tool_choice), Content is left as-is so the caller's own parser
// still sees whatever was produced.
//
// It returns an error if the model produced MORE than one tool call for the
// forced tool (possible on the Anthropic wire, where parallel tool use is
// on by default): folding only the first would silently drop the rest, so
// the caller must surface the failure and retry rather than persist a
// partial object.
func NormalizeStructuredToolResponse(resp *ChatResponse, injected bool) error {
	if !injected || resp == nil || len(resp.ToolCalls) == 0 {
		return nil
	}
	if len(resp.ToolCalls) > 1 {
		return fmt.Errorf("structured output: model returned %d tool calls for the forced schema tool; expected exactly one", len(resp.ToolCalls))
	}
	// The forced tool is the model's structured answer. Fold its input back
	// into Content even when it is an empty object ({} — valid for a schema
	// whose fields are all optional), so the caller never sees the
	// synthetic tool call and always gets a parseable JSON string. A nil
	// input map marshals to "null", so normalise it to an empty object.
	input := resp.ToolCalls[0].Input
	if input == nil {
		input = map[string]interface{}{}
	}
	b, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("structured output: marshal tool input: %w", err)
	}
	resp.Content = string(b)
	resp.ToolCalls = nil
	// The synthetic tool call is now hidden, so the upstream "tool_use"
	// stop reason would violate the invariant that tool_use implies there
	// are tool calls to execute. Present it as a normal terminal turn.
	resp.StopReason = "end_turn"
	return nil
}
