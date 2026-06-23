package askserve

import (
	"fmt"
	"strings"
)

// buildSystemPrompt renders the Q&A system prompt for one turn. It is
// deliberately generic — an analyst data-question agent over a read-only
// warehouse — and describes the JSON action vocabulary the loop parses, the
// hard safety rules, and the result-summarization behaviour so the model
// reaches for aggregate SQL instead of expecting full result sets.
func buildSystemPrompt(rt *ProjectRuntime, cfg Config, filterField string) string {
	var b strings.Builder

	b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and running read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers.\n\n")

	b.WriteString("WAREHOUSE\n")
	if rt.Dialect != "" {
		fmt.Fprintf(&b, "- SQL dialect: %s\n", rt.Dialect)
	}
	if len(rt.Datasets) > 0 {
		fmt.Fprintf(&b, "- Datasets available: %s\n", strings.Join(rt.Datasets, ", "))
	}
	b.WriteString("- The warehouse is READ-ONLY. Emit only SELECT/CTE queries. Never attempt INSERT, UPDATE, DELETE, MERGE, or DDL.\n")
	if strings.TrimSpace(filterField) != "" {
		fmt.Fprintf(&b, "- SECURITY: every query MUST filter by %q (the tenant scope). A query missing this filter is rejected.\n", filterField)
	}

	b.WriteString("\nHOW TO RESPOND\n")
	b.WriteString("Respond with EXACTLY ONE JSON object and nothing else — no prose, no markdown fences. Pick one action per step:\n")
	b.WriteString(`  {"thinking":"...","query":"SELECT ...","purpose":"what this answers"}` + "  — run a read-only SQL query\n")
	b.WriteString(`  {"thinking":"...","lookup_schema":["dataset.table_a","dataset.table_b"]}` + "  — get columns + sample rows for tables\n")
	b.WriteString(`  {"thinking":"...","search_tables":"keywords describing what you need"}` + "  — find relevant tables semantically\n")
	b.WriteString(`  {"thinking":"...","answer":"final grounded answer for the user"}` + "  — when you can answer\n")
	b.WriteString(`  {"thinking":"...","clarify":"a single clarifying question"}` + "  — when the question is too ambiguous to answer\n")
	b.WriteString(`  {"thinking":"...","decline":"why this cannot be answered from the data"}` + "  — when it is unanswerable\n")

	b.WriteString("\nRESULT HANDLING\n")
	fmt.Fprintf(&b, "- Query results are returned summary-only: row count, columns, and a preview of up to %d rows. Large result sets are truncated.\n", cfg.PreviewRows)
	b.WriteString("- For totals, counts, distributions, or \"how many\" questions, write aggregate SQL (COUNT, SUM, AVG, GROUP BY) — do NOT page through raw rows.\n")
	fmt.Fprintf(&b, "- You may run at most %d queries and take at most %d steps this turn. Be economical; reuse results already in this conversation instead of re-querying.\n", cfg.MaxQueriesPerTurn, cfg.MaxRounds)

	b.WriteString("\nFinish with an \"answer\", \"clarify\", or \"decline\" action. The answer should be concise, analyst-style prose that directly addresses the question and references the figures you found.")

	return b.String()
}
