package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// sqlFixMinOutputTokens is the floor the SQL-fix output budget never drops
// below, so a large fix prompt against a tight window still gets room for a
// corrected query. A fixed statement is small, so this is modest.
const sqlFixMinOutputTokens = 512

// SQLFixer uses LLM to fix SQL query errors.
type SQLFixer struct {
	client       *Client
	sqlFixPrompt string
	dataset      string
	filter       string
	schemaCtx    string

	// language names what the model is being asked to write, when that is not
	// SQL. Empty means SQL — what every warehouse caller passes, and what
	// keeps their instruction and their fence byte-identical.
	language string

	// window / outputCap are the model's resolved context window and output
	// cap (#347 chain: operator override → live auto-detect → catalog →
	// default). Used to budget the fix call's max_tokens so it can't exceed a
	// small model's real limit — an uncatalogued model resolves to the 64K
	// default cap otherwise, which a 32K/8K deployment rejects with a hard 400.
	// 0 (tests / non-discovery callers) falls back to today's catalog lookup.
	window    int
	outputCap int
}

// SQLFixerOptions configures the SQL fixer.
type SQLFixerOptions struct {
	Client       *Client
	SQLFixPrompt string // from warehouse.QueryRunner.QueryFixPrompt()
	Dataset      string
	Filter       string

	// QueryLanguage names the language the corrected query must be written in,
	// for a source whose queries are not SQL (warehouse.QueryRunner's
	// QueryLanguage). Leave empty for a SQL warehouse. This is not decoration:
	// the repair instruction is the LAST thing the model reads before writing,
	// and telling a source that accepts no SQL to "fix this SQL query" inside a
	// ```sql fence contradicts the repair prompt directly above it.
	QueryLanguage string

	// Window / OutputCap are the resolved model context window and output cap
	// (see SQLFixer). Optional — 0 preserves the pre-#347 catalog-cap behaviour.
	Window    int
	OutputCap int
}

// NewQueryFixerFor builds the repair-loop fixer for one source, filling in the
// two options that must come from the source itself: its repair template and,
// when its queries are not SQL, its query language.
//
// Both are read through the query seam rather than off the SQL surface. For a
// warehouse that is the same template it always got and an empty language, so
// nothing about its repair changes. For a source that supplies the seam
// itself it is that source's own template, and naming the language is what
// stops the repair instruction demanding SQL of something that accepts none.
//
// They are set together, here, because setting one without the other is worse
// than setting neither: a source's own repair template printed above an
// instruction that says "fix this SQL query" is a contradiction the model
// resolves in favour of the instruction, which is the more recent and more
// concrete of the two.
func NewQueryFixerFor(p gowarehouse.Provider, opts SQLFixerOptions) *SQLFixer {
	opts.SQLFixPrompt = gowarehouse.AsQueryRunner(p).QueryFixPrompt()
	opts.QueryLanguage = gowarehouse.NonSQLLanguage(p)
	return NewSQLFixer(opts)
}

// NewSQLFixer creates a new SQL fixer.
func NewSQLFixer(opts SQLFixerOptions) *SQLFixer {
	return &SQLFixer{
		client:       opts.Client,
		sqlFixPrompt: opts.SQLFixPrompt,
		dataset:      opts.Dataset,
		filter:       opts.Filter,
		language:     opts.QueryLanguage,
		window:       opts.Window,
		outputCap:    opts.OutputCap,
	}
}

// FixSQL attempts to fix a SQL query based on the error message. Per-call
// `opts` carry context that varies per request — currently the rendered
// VerificationContext that the validator wants the fixer to ground on.
// Exploration callers pass an empty FixOpts and the
// {{#VERIFICATION_CONTEXT}}…{{/VERIFICATION_CONTEXT}} section is stripped
// from the rendered prompt.
func (f *SQLFixer) FixSQL(ctx context.Context, query string, errorMsg string, attempt int, opts queryexec.FixOpts) (queryexec.FixResult, error) {
	logger.WithFields(logger.Fields{
		"attempt": attempt,
		"error":   errorMsg,
	}).Info("Attempting to fix SQL query")

	systemPrompt := f.sqlFixPrompt
	systemPrompt = applySection(systemPrompt, "VERIFICATION_CONTEXT", opts.VerificationContext)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{DATASET}}", f.dataset)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{FILTER}}", f.filter)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{SCHEMA_INFO}}", f.schemaCtx)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{ORIGINAL_SQL}}", query)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{ERROR_MESSAGE}}", errorMsg)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{VERIFICATION_CONTEXT}}", opts.VerificationContext)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{CONVERSATION_HISTORY}}", "")

	userMessage := fixInstruction(f.language, query, errorMsg, attempt)

	conversation := NewConversation(ConversationOptions{
		SystemPrompt: systemPrompt,
		MaxMessages:  10,
	})
	conversation.AddUserMessage(userMessage)

	// renderedPrompt is what we report back to the caller as PromptIn:
	// the system instruction followed by the user message, formatted so
	// a reader can see the dialog as the LLM did. Build it before the
	// call so the prompt is recorded even when the LLM call errors out.
	renderedPrompt := renderFixPrompt(conversation.GetSystemPrompt(), userMessage)

	// Budget the fix output against the resolved window + output cap so the
	// request stays inside a small model's real limit. Historically this used
	// the raw catalog cap, which for an uncatalogued model resolves to the 64K
	// global default — larger than a 32K/8K deployment can accept, so the
	// provider rejected the fix with a hard 400 and the query (and its
	// exploration step) failed (observed on qwen3.5-27b via a vLLM/LiteLLM
	// gateway). budgetFixOutput reduces to today's catalog cap for large
	// models with a known-wide window, so their behaviour is unchanged.
	maxOutputTokens := f.budgetFixOutput(conversation)

	start := time.Now()
	response, err := f.client.CreateMessage(ctx, conversation.GetMessages(), conversation.GetSystemPrompt(), maxOutputTokens)
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		return queryexec.FixResult{Prompt: renderedPrompt, DurationMs: durationMs}, fmt.Errorf("failed to get SQL fix: %w", err)
	}

	rawResponse := ""
	inputTokens := 0
	outputTokens := 0
	if response != nil {
		rawResponse = response.Content
		inputTokens = response.Usage.InputTokens
		outputTokens = response.Usage.OutputTokens
	}

	fixedSQL, extractErr := extractFixedSQL(response)
	if extractErr != nil {
		return queryexec.FixResult{
			Prompt:       renderedPrompt,
			Response:     rawResponse,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			DurationMs:   durationMs,
		}, fmt.Errorf("failed to extract fixed SQL: %w", extractErr)
	}

	logger.WithField("attempt", attempt).Info("SQL query fixed")

	return queryexec.FixResult{
		FixedSQL:     fixedSQL,
		Prompt:       renderedPrompt,
		Response:     rawResponse,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		DurationMs:   durationMs,
	}, nil
}

// budgetFixOutput returns the max_tokens the SQL-fix call should request so it
// never exceeds the model's real output cap or window. It resolves the output
// cap (falling back to the catalog lookup when the resolved cap is unset, which
// preserves the pre-#347 behaviour for tests / non-discovery callers), then —
// when a window is known — budgets it against the measured fix prompt so
// input + output stays inside the window. For a large model with a wide window
// this returns the full catalog cap unchanged (the fix prompt is tiny relative
// to the window), so big models are unaffected; it only bites on a tight-window
// model where the raw 64K default would 400.
func (f *SQLFixer) budgetFixOutput(conv *Conversation) int {
	outCap := f.outputCap
	if outCap <= 0 {
		outCap = gollm.GetMaxOutputTokens(f.client.ProviderName(), f.client.ModelName())
	}
	if f.window <= 0 {
		// No known window (tests / non-discovery): today's behaviour.
		return outCap
	}
	avail := gollm.NewBudget(f.window, 0, explorationReservedSystemTokens, false).Available() - conversationInputEst(conv)
	if outCap > avail {
		outCap = avail
	}
	if outCap < sqlFixMinOutputTokens {
		outCap = sqlFixMinOutputTokens
	}
	return outCap
}

// renderFixPrompt formats the system+user pair into a single text blob
// that mirrors how the LLM saw the dialog. The fixer always sends two
// messages (system + a single user turn) so flattening them produces a
// faithful record without ambiguity. Keep the markers short and stable
// — downstream tooling parses this back.
func renderFixPrompt(system, user string) string {
	var b strings.Builder
	if system != "" {
		b.WriteString("[system]\n")
		b.WriteString(system)
		b.WriteString("\n")
	}
	b.WriteString("[user]\n")
	b.WriteString(user)
	return b.String()
}

// SetSchemaContext updates the schema context.
func (f *SQLFixer) SetSchemaContext(schemaJSON string) {
	f.schemaCtx = schemaJSON
}

// applySection processes a mustache-style {{#NAME}}…{{/NAME}} conditional
// section in the template. When `value` is empty (after trimming whitespace)
// the entire block — markers and inner content — is removed, so prompt
// templates can declare a header + reusable framing for an optional section
// without leaving an empty header in the rendered output. When `value` is
// non-empty the markers are stripped but the inner content is kept verbatim;
// the inner `{{NAME}}` placeholder is then substituted by the surrounding
// caller via strings.ReplaceAll.
//
// Handles multiple occurrences and nested-by-different-name sections; nested
// sections of the SAME name are not supported (we don't have a use case for
// them and the simpler scanner is easier to reason about).
func applySection(template, name, value string) string {
	open := "{{#" + name + "}}"
	close := "{{/" + name + "}}"
	keep := strings.TrimSpace(value) != ""

	for {
		oi := strings.Index(template, open)
		if oi == -1 {
			return template
		}
		ci := strings.Index(template[oi:], close)
		if ci == -1 {
			// Unterminated block — leave the rest of the template alone so
			// the caller can spot the malformed marker in their prompt.
			return template
		}
		ci += oi
		end := ci + len(close)
		if keep {
			inner := template[oi+len(open) : ci]
			template = template[:oi] + inner + template[end:]
		} else {
			template = template[:oi] + template[end:]
		}
	}
}

// extractFixedSQL pulls the corrected query out of the model's reply.
//
// It accepts a structured request — a JSON object that IS the query — as well
// as SQL, because a source whose language is a request format answers the
// repair prompt with one, and a reply the extractor discards means that source
// has no repair path at all: its query fails on the first error with the
// attempt recorded as a parse failure rather than the source's own diagnosis.
//
// The SQL paths are unchanged and still take precedence. The one behaviour
// that differs for a SQL warehouse is a reply that is a bare JSON object and
// not a fixed_sql envelope: it used to be refused here and is now handed on,
// where the warehouse rejects it as a syntax error. Both outcomes are a failed
// attempt inside the same retry loop; only the recorded error differs.
func extractFixedSQL(response *gollm.ChatResponse) (string, error) {
	if response == nil || response.Content == "" {
		return "", fmt.Errorf("empty response")
	}

	text := response.Content

	// Try ```sql code block first
	if strings.Contains(text, "```sql") {
		if sql := extractCodeBlock(text, "sql"); sql != "" {
			return strings.TrimSpace(sql), nil
		}
	}

	// Try any code block (language tag is stripped by extractCodeBlock)
	if strings.Contains(text, "```") {
		if block := extractCodeBlock(text, ""); block != "" {
			block = strings.TrimSpace(block)
			// If the block looks like JSON with a fixed_sql field, extract it
			if sql := extractSQLFromJSON(block); sql != "" {
				return sql, nil
			}
			if strings.Contains(strings.ToUpper(block), "SELECT") {
				return block, nil
			}
			if isStructuredQuery(block) {
				return block, nil
			}
		}
	}

	// Raw text — check for JSON with fixed_sql first
	trimmed := strings.TrimSpace(text)
	if sql := extractSQLFromJSON(trimmed); sql != "" {
		return sql, nil
	}

	if strings.Contains(strings.ToUpper(trimmed), "SELECT") {
		return trimmed, nil
	}
	if isStructuredQuery(trimmed) {
		return trimmed, nil
	}

	return "", fmt.Errorf("response does not appear to be SQL")
}

// isStructuredQuery reports whether text is a JSON object that IS the query,
// rather than a wrapper around one.
//
// A {"fixed_sql": …} envelope is excluded on purpose. Its CONTENTS are the
// query, and extraction above has already had its chance at them — so if we
// reach here with an envelope, the reply carried no usable query, and handing
// the wrapper on would submit the model's packaging to the source as if it
// were the query itself. An empty object is excluded for the same reason: it
// asks for nothing.
func isStructuredQuery(text string) bool {
	// A cheap reject before parsing. Not the guard that rejects an array or a
	// bare literal — decoding into a map already does that — just a way to
	// avoid handing a whole prose reply to the JSON decoder.
	if text == "" || text[0] != '{' {
		return false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return false
	}
	if _, wrapped := probe["fixed_sql"]; wrapped {
		return false
	}
	return len(probe) > 0
}

// extractSQLFromJSON extracts the fixed_sql field from a JSON response.
// Returns empty string if the text is not valid JSON or lacks fixed_sql.
func extractSQLFromJSON(text string) string {
	if len(text) == 0 || text[0] != '{' {
		return ""
	}
	var parsed struct {
		FixedSQL string `json:"fixed_sql"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return ""
	}
	sql := strings.TrimSpace(parsed.FixedSQL)
	if sql == "" || !strings.Contains(strings.ToUpper(sql), "SELECT") {
		return ""
	}
	return sql
}

func extractCodeBlock(text string, language string) string {
	marker := "```"
	if language != "" {
		marker = "```" + language
	}

	startIdx := strings.Index(text, marker)
	if startIdx == -1 {
		return ""
	}

	startIdx += len(marker)

	// Strip language tag on the same line (e.g., "json", "sql" after ```)
	// This handles cases where we search for generic ``` and find ```json
	if language == "" {
		for startIdx < len(text) && text[startIdx] != '\n' && text[startIdx] != '\r' {
			startIdx++
		}
	}

	for startIdx < len(text) && (text[startIdx] == '\n' || text[startIdx] == '\r') {
		startIdx++
	}

	endIdx := strings.Index(text[startIdx:], "```")
	if endIdx == -1 {
		return ""
	}

	return text[startIdx : startIdx+endIdx]
}

// fixInstruction builds the user message that carries the failed query and its
// error to the model.
//
// It is the last thing the model reads before writing a replacement, and it
// arrives after the source's own repair template — so when the two disagree,
// this one wins. For a source that accepts no SQL, "Fix this SQL query" inside
// a ```sql fence tells the model to do exactly what the template above it just
// forbade.
//
// An empty language is SQL, and renders the historic instruction unchanged.
func fixInstruction(language, query, errorMsg string, attempt int) string {
	if language == "" {
		return fmt.Sprintf("Fix this SQL query (attempt %d). Return ONLY the corrected SQL.\n\nQuery:\n```sql\n%s\n```\n\nError:\n```\n%s\n```", attempt+1, query, errorMsg)
	}
	// No language tag on the fence: the tag names a syntax highlighter, and
	// guessing one for an arbitrary request format would put a second, wrong
	// claim about the language next to the right one.
	return fmt.Sprintf("Fix this query (attempt %d). It is written in %s, not SQL. Return ONLY the corrected query.\n\nQuery:\n```\n%s\n```\n\nError:\n```\n%s\n```", attempt+1, language, query, errorMsg)
}
