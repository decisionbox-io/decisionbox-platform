package askserve

import (
	"context"
	"fmt"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// KnowledgeProvider backs the search_knowledge tool: a semantic search over the
// project's knowledge base — uploaded documents and operator-authored notes. It
// mirrors ai.InsightsProvider: the runtime holds at most one per project, and a
// nil provider simply means the tool is not offered. The agentserver wiring
// builds it over the go-common sources registry (the enterprise sources plugin
// fills that registry; a community-only build leaves it a no-op, so the provider
// is nil and the tool is absent).
type KnowledgeProvider interface {
	// RetrieveKnowledge returns the passages most relevant to the query, scoped
	// to the project. Implementations MUST scope to their project.
	RetrieveKnowledge(ctx context.Context, query string, k int) ([]KnowledgeChunk, error)
}

// KnowledgeChunk is one retrieved passage of a knowledge source (document or
// note). It is the agent-facing projection — enough for the model to read the
// content and name where it came from.
type KnowledgeChunk struct {
	// SourceName is the originating document / note title, for the model to cite.
	SourceName string
	// SourceType is the source kind ("pdf", "docx", "note", …). Optional.
	SourceType string
	// Text is the passage content, already trimmed and ready to read.
	Text string
	// Score is the similarity score in [0, 1].
	Score float64
}

// knowledgeTextPreviewCap bounds how much chunk text is persisted on the tool
// event (the model still receives the full passage in its observation). The
// full bodies are large and reconstructible from the source, so storing only a
// preview keeps the session document from growing on every knowledge search.
const knowledgeTextPreviewCap = 200

func toolSearchKnowledge() gollm.ToolDefinition {
	return gollm.ToolDefinition{
		Name: string(actSearchKnowledge),
		Description: "Semantic search over the project's knowledge base — uploaded documents (PDFs, spreadsheets, docs) and operator-authored notes. " +
			"Use it for definitions, business rules, policies, glossary terms, or context that live in the project's documents/notes rather than the warehouse tables. Returns the most relevant passages.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Keywords or a question describing what you're looking for."},
				"limit": map[string]interface{}{"type": "integer", "description": "Max passages to return (optional)."},
			},
			"required": []string{"query"},
		},
	}
}

func (r *runner) execSearchKnowledge(ctx context.Context, rt *ProjectRuntime, st *turnState, act *turnAction) string {
	ev := commonmodels.ToolEvent{
		Round: st.round,
		Name:  string(actSearchKnowledge),
		Args:  map[string]any{"query": act.SearchKnowledge, "limit": act.KnowledgeLimit},
	}
	if rt.KnowledgeProvider == nil {
		ev.Error = "knowledge provider not configured"
		r.emit(ctx, st, ev)
		return "Knowledge search unavailable. Answer from a query or an insight search instead, or decline."
	}
	start := time.Now()
	hits, err := rt.KnowledgeProvider.RetrieveKnowledge(ctx, act.SearchKnowledge, act.KnowledgeLimit)
	ev.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
		r.emit(ctx, st, ev)
		return fmt.Sprintf("Knowledge search failed: %s", err.Error())
	}
	ev.Output = knowledgeSummary(hits)
	r.emit(ctx, st, ev)
	return formatKnowledge(act.SearchKnowledge, hits)
}

// formatKnowledge renders the observation text the model sees for a knowledge
// search (full passage bodies, so the model can quote them).
func formatKnowledge(query string, hits []KnowledgeChunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Knowledge search results for %q:\n", query)
	if len(hits) == 0 {
		b.WriteString("(no matching knowledge-base documents or notes)")
	}
	for i, h := range hits {
		name := strings.TrimSpace(h.SourceName)
		if name == "" {
			name = "source"
		}
		typ := strings.TrimSpace(h.SourceType)
		if typ == "" {
			typ = "doc"
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, typ, name)
		if t := strings.TrimSpace(h.Text); t != "" {
			fmt.Fprintf(&b, " — %s", t)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// knowledgeSummary is the compact, lowercase-keyed record persisted as the
// search_knowledge tool event's output. The passage text is previewed (not
// stored in full) so a long session's document doesn't accumulate whole chunk
// bodies.
func knowledgeSummary(hits []KnowledgeChunk) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"source_name": h.SourceName,
			"source_type": h.SourceType,
			"score":       h.Score,
			"text":        previewText(h.Text, knowledgeTextPreviewCap),
		})
	}
	return out
}

// previewText returns a rune-safe, length-capped preview of s with an ellipsis
// when truncated.
func previewText(s string, cap int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > cap {
		return strings.TrimSpace(string(r[:cap])) + "…"
	}
	return s
}
