package askserve

import (
	"fmt"
	"strings"
)

// buildSystemPrompt renders the Q&A system prompt for one turn on the JSON-text
// path (providers without native tool-calling). It is deliberately generic — an
// analyst data-question agent over a read-only warehouse — and describes the
// JSON action vocabulary the loop parses, the hard safety rules, and the
// result-summarization behaviour so the model reaches for aggregate SQL instead
// of expecting full result sets.
func buildSystemPrompt(rt *ProjectRuntime, cfg Config, chartsEnabled bool) string {
	var b strings.Builder

	b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and running read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers.\n\n")

	writeWarehouseSection(&b, rt)

	b.WriteString("\nHOW TO RESPOND\n")
	b.WriteString("Respond with EXACTLY ONE JSON object and nothing else — no prose, no markdown fences. Pick one action per step:\n")
	b.WriteString(`  {"thinking":"...","query":"SELECT ...","purpose":"what this answers"}` + "  — run a read-only SQL query\n")
	b.WriteString(`  {"thinking":"...","lookup_schema":["dataset.table_a","dataset.table_b"]}` + "  — get columns + sample rows for tables\n")
	b.WriteString(`  {"thinking":"...","search_tables":"keywords describing what you need"}` + "  — find relevant tables semantically\n")
	if rt.InsightsProvider != nil {
		b.WriteString(`  {"thinking":"...","search_insights":"keywords"}` + "  — search prior discovered insights & recommendations\n")
	}
	if chartsEnabled {
		b.WriteString(`  {"thinking":"...","render_chart":{"type":"bar","source_step_id":"q2","x":{"field":"month"},"y":[{"field":"revenue"}],"data":[...]}}` + "  — chart a prior query result\n")
	}
	b.WriteString(`  {"thinking":"...","answer":"final grounded answer for the user"}` + "  — when you can answer\n")
	b.WriteString(`  {"thinking":"...","clarify":"a single clarifying question"}` + "  — when the question is too ambiguous to answer\n")
	b.WriteString(`  {"thinking":"...","decline":"why this cannot be answered from the data"}` + "  — when it is unanswerable\n")

	writeResultHandling(&b, cfg)
	if chartsEnabled {
		writeChartsSection(&b, cfg)
	}

	b.WriteString("\nGROUNDING (required): you MUST gather evidence and observe its result before you give an `answer`. Never state a table name, count, total, or specific value you have not seen in a result in this conversation — do not answer from prior knowledge or guesses. If you don't yet know the tables or columns, your FIRST action must be a discovery query — e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES` — or a search_tables / lookup_schema; do not invent table or column names. An answer with no evidence behind it will be rejected; only use clarify or decline if the question genuinely cannot be turned into any query.\n")
	if rt.InsightsProvider != nil {
		b.WriteString("For questions about what prior analysis found or recommended, a search_insights result is sufficient grounding on its own — you do not need to run SQL.\n")
	}

	b.WriteString("\nFinish with an \"answer\", \"clarify\", or \"decline\" action. The answer should be concise, analyst-style prose that directly addresses the question and references the figures you found.")

	return b.String()
}

// buildSystemPromptForTools renders the Q&A system prompt for the native
// tool-calling path. It reuses the warehouse, security, and result-handling
// guidance but omits the JSON action contract — the tools ARE the contract, and
// the loop withholds the `answer` tool until at least one query/lookup/search
// has run, so grounding is enforced structurally rather than by prose. The
// prose here is guidance, not a hard gate.
func buildSystemPromptForTools(rt *ProjectRuntime, cfg Config, chartsEnabled bool) string {
	var b strings.Builder

	b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and using the provided tools to run read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers, table names, or column names.\n\n")

	writeWarehouseSection(&b, rt)

	b.WriteString("\nTOOLS\n")
	b.WriteString("- query_data: run one read-only SQL query and observe a summary of the result.\n")
	b.WriteString("- search_tables / lookup_schema: discover which tables exist and what columns they have.\n")
	if rt.InsightsProvider != nil {
		b.WriteString("- search_insights: search the project's prior discovered insights & recommendations; prefer it for \"what did we find\" / \"what do you recommend\" questions, and combine with query_data when a finding needs a fresh number.\n")
	}
	if chartsEnabled {
		b.WriteString("- render_chart: chart a prior query result (offered once a query has run). The chart data must be an exact projection of that query's preview.\n")
	}
	b.WriteString("- answer / clarify / decline: finish the turn.\n")

	writeResultHandling(&b, cfg)
	if chartsEnabled {
		writeChartsSection(&b, cfg)
	}

	evidence := "query_data, search_tables, or lookup_schema"
	if rt.InsightsProvider != nil {
		evidence = "query_data, search_tables, lookup_schema, or search_insights"
	}
	fmt.Fprintf(&b, "\nGROUNDING (required): you MUST run at least one %s call and observe its result before you answer. Never state a table name, count, total, or value you have not seen in a result this turn — do not answer from prior knowledge or guesses. If you don't know the tables or columns, start with search_tables or a discovery query (e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES`); do not invent names. Only clarify when the request is genuinely too ambiguous to query, and prefer gathering evidence before you decline.\n", evidence)
	if rt.InsightsProvider != nil {
		b.WriteString("For questions about what prior analysis found or recommended, a search_insights result is sufficient grounding on its own — you do not need to run SQL.\n")
	}

	b.WriteString("\nFinish by calling answer (concise, analyst-style prose referencing the figures you found), clarify, or decline.")

	return b.String()
}

// writeWarehouseSection renders the shared WAREHOUSE block (dialect, datasets,
// read-only rule, and the tenant-scope predicate when the dataset is
// multi-tenant). Shared verbatim by both prompt builders.
func writeWarehouseSection(b *strings.Builder, rt *ProjectRuntime) {
	b.WriteString("WAREHOUSE\n")
	if rt.Dialect != "" {
		fmt.Fprintf(b, "- SQL dialect: %s\n", rt.Dialect)
	}
	if len(rt.Datasets) > 0 {
		fmt.Fprintf(b, "- Datasets available: %s\n", strings.Join(rt.Datasets, ", "))
	}
	b.WriteString("- The warehouse is READ-ONLY. Emit only SELECT/CTE queries. Never attempt INSERT, UPDATE, DELETE, MERGE, or DDL.\n")
	if strings.TrimSpace(rt.FilterField) != "" {
		if strings.TrimSpace(rt.FilterValue) != "" {
			// Inject the exact tenant predicate so the model scopes every query
			// correctly. (Presence of the column is enforced server-side; do not
			// negate or broaden it.)
			fmt.Fprintf(b, "- SECURITY: this is a multi-tenant dataset. Every query MUST be scoped to this tenant with the predicate `%s = '%s'` (in the WHERE clause, or preserved through every join/CTE). Never negate, broaden, or omit it. A query missing the %q column is rejected.\n", rt.FilterField, rt.FilterValue, rt.FilterField)
		} else {
			fmt.Fprintf(b, "- SECURITY: every query MUST filter by %q (the tenant scope). A query missing this filter is rejected.\n", rt.FilterField)
		}
	}
}

// writeChartsSection renders the shared CHARTS guidance, included only when
// charting is enabled for the turn. It tells the model when a chart helps, how
// to keep it grounded (exact projection of a query preview — narrow the result
// in SQL, never compute chart numbers), and to decline to chart rather than
// invent. cfg supplies the preview cap the model must fit a charted result into.
func writeChartsSection(b *strings.Builder, cfg Config) {
	b.WriteString("\nCHARTS\n")
	b.WriteString("- When a trend, comparison, breakdown, or single headline figure communicates the answer better than prose, render a chart with render_chart. Otherwise just answer.\n")
	b.WriteString("- A chart's data MUST be an exact projection of a query result you already observed: copy the cells verbatim. Never compute, round, scale, or invent a chart number — if you need an aggregate or a smaller set of points, run the aggregation in SQL first, then chart that result.\n")
	b.WriteString("- Set source_step_id to the q<N> id shown in the result you are charting. You can only chart a query whose full result fit the preview (not a truncated one).\n")
	fmt.Fprintf(b, "- If the result you want to chart is truncated, re-run it so the whole result fits in %d rows, and pick the fix that preserves the question: when the question is about totals or a breakdown, aggregate (GROUP BY); when it is about individual entities — one point per customer, product, or region, e.g. a scatter — do NOT aggregate, because that destroys the question. Add ORDER BY <the measure that matters> DESC LIMIT %d instead, and say so in the title or caption (e.g. \"Top %d customers by spend\") so the chart is not read as the whole population.\n", cfg.PreviewRows, cfg.PreviewRows, cfg.PreviewRows)
	b.WriteString("- Keep it readable: few series, clear labels. Types: bar, line, area, pie, scatter, kpi (a single headline figure). Render the chart in its own step, then answer in the next. If the data cannot honestly support a chart, don't force one.\n")
	b.WriteString("- Set a measure's unit + format when you know what it is: currency values → format \"currency\" with the currency code as unit (e.g. USD, DKK); rates/shares → format \"percent\"; otherwise leave it plain. This only controls display (the renderer keeps the exact value) — it makes large figures read as e.g. $17.4B instead of 17392162956.\n")
}

// writeResultHandling renders the shared RESULT HANDLING block. Shared verbatim
// by both prompt builders.
func writeResultHandling(b *strings.Builder, cfg Config) {
	b.WriteString("\nRESULT HANDLING\n")
	fmt.Fprintf(b, "- Query results are returned summary-only: row count, columns, and a preview of up to %d rows. Large result sets are truncated.\n", cfg.PreviewRows)
	b.WriteString("- For totals, counts, distributions, or \"how many\" questions, write aggregate SQL (COUNT, SUM, AVG, GROUP BY) — do NOT page through raw rows.\n")
	fmt.Fprintf(b, "- You may run at most %d queries and take at most %d steps this turn. Be economical; reuse results already in this conversation instead of re-querying.\n", cfg.MaxQueriesPerTurn, cfg.MaxRounds)
}
