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
func buildSystemPrompt(rt *ProjectRuntime, routing turnRouting, cfg Config) string {
	var b strings.Builder

	b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and running read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers.\n\n")

	writeDataSection(&b, routing)

	b.WriteString("\nHOW TO RESPOND\n")
	b.WriteString("Respond with EXACTLY ONE JSON object and nothing else — no prose, no markdown fences. Pick one action per step:\n")
	if routing.multi {
		b.WriteString(`  {"thinking":"...","datasource_id":"<id>","query":"SELECT ...","purpose":"what this answers"}` + "  — run a read-only SQL query against one datasource\n")
		b.WriteString(`  {"thinking":"...","datasource_id":"<id>","lookup_schema":["dataset.table_a"]}` + "  — get columns + sample rows for tables in one datasource\n")
	} else {
		b.WriteString(`  {"thinking":"...","query":"SELECT ...","purpose":"what this answers"}` + "  — run a read-only SQL query\n")
		b.WriteString(`  {"thinking":"...","lookup_schema":["dataset.table_a","dataset.table_b"]}` + "  — get columns + sample rows for tables\n")
	}
	b.WriteString(`  {"thinking":"...","search_tables":"keywords describing what you need"}` + "  — find relevant tables semantically\n")
	if rt.InsightsProvider != nil {
		b.WriteString(`  {"thinking":"...","search_insights":"keywords"}` + "  — search prior discovered insights & recommendations\n")
	}
	b.WriteString(`  {"thinking":"...","answer":"final grounded answer for the user"}` + "  — when you can answer\n")
	b.WriteString(`  {"thinking":"...","clarify":"a single clarifying question"}` + "  — when the question is too ambiguous to answer\n")
	b.WriteString(`  {"thinking":"...","decline":"why this cannot be answered from the data"}` + "  — when it is unanswerable\n")

	writeResultHandling(&b, cfg)

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
func buildSystemPromptForTools(rt *ProjectRuntime, routing turnRouting, cfg Config) string {
	var b strings.Builder

	b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and using the provided tools to run read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers, table names, or column names.\n\n")

	writeDataSection(&b, routing)

	b.WriteString("\nTOOLS\n")
	if routing.multi {
		b.WriteString("- query_data: run one read-only SQL query against ONE datasource (set datasource_id) and observe a summary of the result.\n")
		b.WriteString("- search_tables / lookup_schema: discover which datasource holds which tables and what columns they have (search_tables spans all datasources and tags each hit with its datasource).\n")
	} else {
		b.WriteString("- query_data: run one read-only SQL query and observe a summary of the result.\n")
		b.WriteString("- search_tables / lookup_schema: discover which tables exist and what columns they have.\n")
	}
	if rt.InsightsProvider != nil {
		b.WriteString("- search_insights: search the project's prior discovered insights & recommendations; prefer it for \"what did we find\" / \"what do you recommend\" questions, and combine with query_data when a finding needs a fresh number.\n")
	}
	b.WriteString("- answer / clarify / decline: finish the turn.\n")

	writeResultHandling(&b, cfg)

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

// writeDataSection renders the warehouse/datasources block: a single WAREHOUSE
// block on a single-datasource or pinned turn (identical to the historical
// prompt), or a DATASOURCES catalog + multi-hop guidance when the model chooses
// datasources.
func writeDataSection(b *strings.Builder, routing turnRouting) {
	if routing.multi {
		writeDatasourcesSection(b, routing)
		return
	}
	// Single datasource visible (single-warehouse project, or an explicit pin).
	var d DatasourceInfo
	if len(routing.datasources) > 0 {
		d = routing.datasources[0]
	}
	writeWarehouseSection(b, d)
}

// writeWarehouseSection renders the shared WAREHOUSE block for one datasource
// (dialect, datasets, read-only rule, and the tenant-scope predicate when the
// dataset is multi-tenant). Shared verbatim by both prompt builders on a
// single-datasource / pinned turn.
func writeWarehouseSection(b *strings.Builder, d DatasourceInfo) {
	b.WriteString("WAREHOUSE\n")
	if d.Dialect != "" {
		fmt.Fprintf(b, "- SQL dialect: %s\n", d.Dialect)
	}
	if len(d.Datasets) > 0 {
		fmt.Fprintf(b, "- Datasets available: %s\n", strings.Join(d.Datasets, ", "))
	}
	b.WriteString("- The warehouse is READ-ONLY. Emit only SELECT/CTE queries. Never attempt INSERT, UPDATE, DELETE, MERGE, or DDL.\n")
	writeTenantScope(b, "- ", d)
}

// writeDatasourcesSection renders the DATASOURCES catalog for a multi-datasource
// turn: one block per datasource (id, label, dialect, datasets, tenant scope,
// card) plus the one-warehouse-per-statement + bounded multi-hop rules.
func writeDatasourcesSection(b *strings.Builder, routing turnRouting) {
	b.WriteString("DATASOURCES\n")
	b.WriteString("This project has multiple datasources. Each SQL query runs against exactly ONE datasource — pass its id as datasource_id. A single query cannot join across datasources.\n")
	for _, d := range routing.datasources {
		primary := ""
		if d.ID == routing.primary {
			primary = " (primary — the default when no datasource_id is given)"
		}
		fmt.Fprintf(b, "\n• datasource_id: %s%s\n", d.ID, primary)
		if d.Label != "" {
			fmt.Fprintf(b, "  name: %s\n", d.Label)
		}
		if d.Description != "" {
			fmt.Fprintf(b, "  holds: %s\n", d.Description)
		}
		if d.Card != nil {
			if len(d.Card.SubjectAreas) > 0 {
				fmt.Fprintf(b, "  subject areas: %s\n", strings.Join(d.Card.SubjectAreas, ", "))
			}
			if len(d.Card.KeyEntities) > 0 {
				fmt.Fprintf(b, "  key entities: %s\n", strings.Join(d.Card.KeyEntities, ", "))
			}
			if len(d.Card.KeyMetrics) > 0 {
				fmt.Fprintf(b, "  key metrics: %s\n", strings.Join(d.Card.KeyMetrics, ", "))
			}
		}
		if d.Dialect != "" {
			fmt.Fprintf(b, "  SQL dialect: %s\n", d.Dialect)
		}
		if len(d.Datasets) > 0 {
			fmt.Fprintf(b, "  datasets: %s\n", strings.Join(d.Datasets, ", "))
		}
		writeTenantScope(b, "  tenant scope: ", d)
	}
	b.WriteString("\n- Every datasource is READ-ONLY. Emit only SELECT/CTE queries; never INSERT, UPDATE, DELETE, MERGE, or DDL.\n")
	b.WriteString("- Pick the datasource whose contents match the question. Use search_tables (it spans all datasources and tags each table with its datasource) when you're unsure which one holds what.\n")
	b.WriteString("- To combine datasources, do it in HOPS: query one datasource, then use a SMALL set of the result values (e.g. the top-N ids you observed) as literal filters in a follow-up query on another datasource. Keep the crossed set small — only values you have actually observed in a result this turn. Do not attempt a cross-datasource join in a single query.\n")
}

// writeTenantScope renders the multi-tenant predicate line for a datasource, or
// nothing when the datasource is single-tenant. prefix leads the line so the
// same rule renders under both the single-warehouse ("- ") and per-datasource
// ("  tenant scope: ") layouts.
func writeTenantScope(b *strings.Builder, prefix string, d DatasourceInfo) {
	if strings.TrimSpace(d.FilterField) == "" {
		return
	}
	if strings.TrimSpace(d.FilterValue) != "" {
		// Inject the exact tenant predicate so the model scopes every query
		// correctly. (Presence of the column is enforced server-side; do not
		// negate or broaden it.)
		fmt.Fprintf(b, "%sSECURITY: this is a multi-tenant dataset. Every query MUST be scoped to this tenant with the predicate `%s = '%s'` (in the WHERE clause, or preserved through every join/CTE). Never negate, broaden, or omit it. A query missing the %q column is rejected.\n", prefix, d.FilterField, d.FilterValue, d.FilterField)
	} else {
		fmt.Fprintf(b, "%sSECURITY: every query MUST filter by %q (the tenant scope). A query missing this filter is rejected.\n", prefix, d.FilterField)
	}
}

// writeResultHandling renders the shared RESULT HANDLING block. Shared verbatim
// by both prompt builders.
func writeResultHandling(b *strings.Builder, cfg Config) {
	b.WriteString("\nRESULT HANDLING\n")
	fmt.Fprintf(b, "- Query results are returned summary-only: row count, columns, and a preview of up to %d rows. Large result sets are truncated.\n", cfg.PreviewRows)
	b.WriteString("- For totals, counts, distributions, or \"how many\" questions, write aggregate SQL (COUNT, SUM, AVG, GROUP BY) — do NOT page through raw rows.\n")
	fmt.Fprintf(b, "- You may run at most %d queries and take at most %d steps this turn. Be economical; reuse results already in this conversation instead of re-querying.\n", cfg.MaxQueriesPerTurn, cfg.MaxRounds)
}
