package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// SQLFixer uses LLM to fix SQL query errors.
type SQLFixer struct {
	client       *Client
	sqlFixPrompt string
	dataset      string
	filter       string
	schemaCtx    string
}

// SQLFixerOptions configures the SQL fixer.
type SQLFixerOptions struct {
	Client       *Client
	SQLFixPrompt string // from warehouse.Provider.SQLFixPrompt()
	Dataset      string
	Filter       string
}

// NewSQLFixer creates a new SQL fixer.
func NewSQLFixer(opts SQLFixerOptions) *SQLFixer {
	return &SQLFixer{
		client:       opts.Client,
		sqlFixPrompt: opts.SQLFixPrompt,
		dataset:      opts.Dataset,
		filter:       opts.Filter,
	}
}

// FixSQL attempts to fix a SQL query based on the error message. Per-call
// `opts` carry context that varies per request — currently the rendered
// VerificationContext that the validator wants the fixer to ground on.
// Exploration callers pass an empty FixOpts and the
// {{#VERIFICATION_CONTEXT}}…{{/VERIFICATION_CONTEXT}} section is stripped
// from the rendered prompt. Background:
// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §4.2.
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

	userMessage := fmt.Sprintf("Fix this SQL query (attempt %d). Return ONLY the corrected SQL.\n\nQuery:\n```sql\n%s\n```\n\nError:\n```\n%s\n```", attempt+1, query, errorMsg)

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

	// Cap output at whatever the active (provider, model) declares in
	// the central catalog. Without this lookup every model was capped
	// at the historical 4000, which left wider-window models leaving
	// budget on the table and tighter-window models occasionally being
	// asked for tokens they can't deliver. GetMaxOutputTokens falls back
	// to the global default (8192) when neither the catalog nor the
	// provider's DefaultMaxOutputTokens is set, so providers without a
	// fixed catalog (Ollama) still get a sensible cap.
	maxOutputTokens := gollm.GetMaxOutputTokens(f.client.ProviderName(), f.client.ModelName())

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
		}
	}

	// Raw text — check for JSON with fixed_sql first
	trimmed := strings.TrimSpace(text)
	if sql := extractSQLFromJSON(trimmed); sql != "" {
		return sql, nil
	}

	if !strings.Contains(strings.ToUpper(trimmed), "SELECT") {
		return "", fmt.Errorf("response does not appear to be SQL")
	}

	return trimmed, nil
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
