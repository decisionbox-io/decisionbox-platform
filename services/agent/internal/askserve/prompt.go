package askserve

import (
	"fmt"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// buildSystemPrompt renders the Q&A system prompt for one turn on the JSON-text
// path (providers without native tool-calling). It is deliberately generic — an
// analyst data-question agent over a read-only warehouse — and describes the
// JSON action vocabulary the loop parses, the hard safety rules, and the
// result-summarization behaviour so the model reaches for aggregate SQL instead
// of expecting full result sets.
func buildSystemPrompt(rt *ProjectRuntime, routing turnRouting, cfg Config, chartsEnabled bool) string {
	var b strings.Builder

	// Every "SQL" in this prompt is branched on what the turn can actually
	// reach. A turn of only SQL datasources — every project that exists today —
	// renders byte-for-byte as it always has.
	shapes := routing.shapes()

	if shapes.anyCube {
		b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and running read-only queries against their data sources. Not every source is SQL — write each query in the language the source below states. Ground every claim in query results — never invent numbers.\n\n")
	} else {
		b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and running read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers.\n\n")
	}

	writeDataSection(&b, routing)

	b.WriteString("\nHOW TO RESPOND\n")
	b.WriteString("Respond with EXACTLY ONE JSON object and nothing else — no prose, no markdown fences. Pick one action per step:\n")
	// The `query` placeholder is a SELECT stub only while every reachable source
	// takes SQL. Left in place for a cube turn it would be the most concrete
	// instruction in the prompt, and concrete beats prose.
	switch {
	case routing.multi && shapes.anyCube:
		b.WriteString(`  {"thinking":"...","datasource_id":"<id>","query":"...","purpose":"what this answers"}` + "  — run one read-only query against one datasource, written in THAT datasource's query language\n")
		b.WriteString(`  {"thinking":"...","datasource_id":"<id>","lookup_schema":["dataset.table_a"]}` + "  — get columns + sample rows for tables in one datasource\n")
	case routing.multi:
		b.WriteString(`  {"thinking":"...","datasource_id":"<id>","query":"SELECT ...","purpose":"what this answers"}` + "  — run a read-only SQL query against one datasource\n")
		b.WriteString(`  {"thinking":"...","datasource_id":"<id>","lookup_schema":["dataset.table_a"]}` + "  — get columns + sample rows for tables in one datasource\n")
	case shapes.anyCube:
		b.WriteString(`  {"thinking":"...","query":"...","purpose":"what this answers"}` + "  — run a read-only query, written in this source's query language\n")
	default:
		b.WriteString(`  {"thinking":"...","query":"SELECT ...","purpose":"what this answers"}` + "  — run a read-only SQL query\n")
		b.WriteString(`  {"thinking":"...","lookup_schema":["dataset.table_a","dataset.table_b"]}` + "  — get columns + sample rows for tables\n")
	}
	if shapes.anyCube {
		// The same contradiction the tool description had: this is the one
		// discovery action that works against a source with no tables, and the
		// prompt has just sent the model here — describing it as a table search
		// argues against taking the only path that can work.
		b.WriteString(`  {"thinking":"...","search_tables":"keywords describing what you need"}` + "  — find relevant tables, metrics or dimensions semantically\n")
	} else {
		b.WriteString(`  {"thinking":"...","search_tables":"keywords describing what you need"}` + "  — find relevant tables semantically\n")
	}
	if rt.InsightsProvider != nil {
		b.WriteString(`  {"thinking":"...","search_insights":"keywords"}` + "  — search prior discovered insights & recommendations\n")
	}
	if chartsEnabled {
		b.WriteString(`  {"thinking":"...","render_chart":{"type":"bar","source_step_id":"q2","x":{"field":"month"},"y":[{"field":"revenue"}],"data":[...]}}` + "  — chart a prior query result\n")
	}
	b.WriteString(`  {"thinking":"...","answer":"final grounded answer for the user"}` + "  — when you can answer\n")
	b.WriteString(`  {"thinking":"...","clarify":"a single clarifying question"}` + "  — when the question is too ambiguous to answer\n")
	b.WriteString(`  {"thinking":"...","decline":"why this cannot be answered from the data"}` + "  — when it is unanswerable\n")

	writeResultHandling(&b, cfg, shapes.anyCube)
	if chartsEnabled {
		writeChartsSection(&b, cfg)
	}

	if shapes.anyCube {
		b.WriteString("\nGROUNDING (required): you MUST gather evidence and observe its result before you give an `answer`. Never state a table, metric, dimension, count, total, or specific value you have not seen in a result in this conversation — do not answer from prior knowledge or guesses. If you don't yet know what a datasource offers, your FIRST action must be a search_tables call — it also covers the metrics and dimensions of a source that has no tables. A discovery query (e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES`) and lookup_schema apply only to a SQL datasource. An answer with no evidence behind it will be rejected; only use clarify or decline if the question genuinely cannot be turned into any query.\n")
	} else {
		b.WriteString("\nGROUNDING (required): you MUST gather evidence and observe its result before you give an `answer`. Never state a table name, count, total, or specific value you have not seen in a result in this conversation — do not answer from prior knowledge or guesses. If you don't yet know the tables or columns, your FIRST action must be a discovery query — e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES` — or a search_tables / lookup_schema; do not invent table or column names. An answer with no evidence behind it will be rejected; only use clarify or decline if the question genuinely cannot be turned into any query.\n")
	}
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
func buildSystemPromptForTools(rt *ProjectRuntime, routing turnRouting, cfg Config, chartsEnabled bool) string {
	var b strings.Builder

	shapes := routing.shapes()

	if shapes.anyCube {
		b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and using the provided tools to run read-only queries against their data sources. Not every source is SQL — write each query in the language the source below states. Ground every claim in query results — never invent numbers, table names, or column names.\n\n")
	} else {
		b.WriteString("You are a data analyst agent. Answer the user's natural-language question about their data by reasoning step by step and using the provided tools to run read-only SQL against their data warehouse. Ground every claim in query results — never invent numbers, table names, or column names.\n\n")
	}

	writeDataSection(&b, routing)

	b.WriteString("\nTOOLS\n")
	switch {
	case routing.multi && shapes.anyCube:
		b.WriteString("- query_data: run one read-only query against ONE datasource (set datasource_id), written in that datasource's query language, and observe a summary of the result.\n")
		b.WriteString("- search_tables / lookup_schema: discover what each datasource offers (search_tables spans all datasources — tables, and the metrics and dimensions of a source that has no tables — and tags each hit with its datasource; lookup_schema returns columns, so it applies to tables only).\n")
	case routing.multi:
		b.WriteString("- query_data: run one read-only SQL query against ONE datasource (set datasource_id) and observe a summary of the result.\n")
		b.WriteString("- search_tables / lookup_schema: discover which datasource holds which tables and what columns they have (search_tables spans all datasources and tags each hit with its datasource).\n")
	case shapes.anyCube:
		b.WriteString("- query_data: run one read-only query, written in this source's query language, and observe a summary of the result.\n")
		b.WriteString("- search_tables: discover the metrics and dimensions this source offers. lookup_schema does not apply — this source has no tables.\n")
	default:
		b.WriteString("- query_data: run one read-only SQL query and observe a summary of the result.\n")
		b.WriteString("- search_tables / lookup_schema: discover which tables exist and what columns they have.\n")
	}
	if rt.InsightsProvider != nil {
		b.WriteString("- search_insights: search the project's prior discovered insights & recommendations; prefer it for \"what did we find\" / \"what do you recommend\" questions, and combine with query_data when a finding needs a fresh number.\n")
	}
	if chartsEnabled {
		b.WriteString("- render_chart: chart a prior query result (offered once a query has run). The chart data must be an exact projection of that query's preview.\n")
	}
	b.WriteString("- answer / clarify / decline: finish the turn.\n")

	writeResultHandling(&b, cfg, shapes.anyCube)
	if chartsEnabled {
		writeChartsSection(&b, cfg)
	}

	evidence := "query_data, search_tables, or lookup_schema"
	if rt.InsightsProvider != nil {
		evidence = "query_data, search_tables, lookup_schema, or search_insights"
	}
	discovery := "If you don't know the tables or columns, start with search_tables or a discovery query (e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES`); do not invent names."
	if shapes.anyCube {
		discovery = "If you don't know what a datasource offers, start with search_tables — it also covers the metrics and dimensions of a source that has no tables. A discovery query (e.g. `SELECT table_name FROM <dataset>.INFORMATION_SCHEMA.TABLES`) and lookup_schema apply only to a SQL datasource. Do not invent names."
	}
	fmt.Fprintf(&b, "\nGROUNDING (required): you MUST run at least one %s call and observe its result before you answer. Never state a table name, count, total, or value you have not seen in a result this turn — do not answer from prior knowledge or guesses. %s Only clarify when the request is genuinely too ambiguous to query, and prefer gathering evidence before you decline.\n", evidence, discovery)
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

// writeWarehouseSection renders the shared datasource block for one datasource
// on a single-datasource / pinned turn.
//
// It branches on the source's declared shape, because the two shapes need
// contradictory instructions. Telling a model "emit only SELECT/CTE queries"
// about a source that has no tables does not merely fail to help — the model
// resolves the contradiction by writing SQL, which the source then rejects,
// and the turn is spent on a query that could never have run.
func writeWarehouseSection(b *strings.Builder, d DatasourceInfo) {
	if isCube(d) {
		writeCubeSection(b, d)
		return
	}

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

// writeCubeSection renders the block for a source that has no tables.
//
// It states the absence first and the query language second, in that order on
// purpose: a model that has read "no tables" cannot then write a FROM clause
// by reflex, whereas one that reads a language name first tends to fill the
// gaps with SQL habits. It also names the tools that do NOT apply — a metric
// looks enough like a table name that lookup_schema is the natural next move,
// and it would fail every time.
func writeCubeSection(b *strings.Builder, d DatasourceInfo) {
	b.WriteString("DATASOURCE\n")
	b.WriteString("- This source has NO TABLES. There is nothing to SELECT from and no schema to look up.\n")
	b.WriteString("- It is a metric/dimension cube: you choose what to measure and what to break it down by, and the source computes the result.\n")
	// Only the language is named. Nothing here specifies the request format,
	// because nothing in the turn supplies one yet — search_tables returns the
	// source's metrics and dimensions, not its request envelope. Describing a
	// format the prompt cannot show would be worse than naming the language and
	// letting the model use what it knows about it.
	fmt.Fprintf(b, "- Queries are written as: %s — not SQL, which this source rejects.\n", d.Language())
	b.WriteString("- Use search_tables to find the metrics and dimensions this source offers — each result says whether it is a metric or a dimension. Do not call lookup_schema against this source; it has no columns.\n")
	b.WriteString("- The source is READ-ONLY.\n")
	writeTenantScope(b, "- ", d)
}

// writeDatasourcesSection renders the DATASOURCES catalog for a multi-datasource
// turn: one block per datasource (id, label, dialect, datasets, tenant scope,
// card) plus the one-warehouse-per-statement + bounded multi-hop rules.
func writeDatasourcesSection(b *strings.Builder, routing turnRouting) {
	hasCube := routing.shapes().anyCube

	b.WriteString("DATASOURCES\n")
	if hasCube {
		b.WriteString("This project has multiple datasources. Each query runs against exactly ONE datasource — pass its id as datasource_id. A single query cannot join across datasources.\n")
		// Said once, up front. A model told per-datasource that one of them is
		// not SQL, but told globally that queries are SQL, resolves the
		// conflict in favour of the global rule — and writes SQL at the source
		// that cannot run it.
		// Phrased so it stays true when the visible list is a narrowed subset:
		// the rule is per-datasource, and the listing is where the ones shown
		// state theirs. Claiming every reachable datasource is listed would be
		// false on a routed turn.
		b.WriteString("Datasources do not all speak the same query language — write each query in the language of the datasource you are targeting. Each datasource listed below states its own.\n")
	} else {
		b.WriteString("This project has multiple datasources. Each SQL query runs against exactly ONE datasource — pass its id as datasource_id. A single query cannot join across datasources.\n")
	}
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
		if isCube(d) {
			// Stated as an absence, not just a different language: "no tables"
			// is what stops a FROM clause being written, and it is the fact a
			// language name alone does not convey.
			fmt.Fprintf(b, "  NO TABLES — a metric/dimension cube. Query language: %s (not SQL). lookup_schema does not apply; search_tables lists its metrics and dimensions.\n", d.Language())
		} else if d.Dialect != "" {
			fmt.Fprintf(b, "  SQL dialect: %s\n", d.Dialect)
		}
		if len(d.Datasets) > 0 {
			fmt.Fprintf(b, "  datasets: %s\n", strings.Join(d.Datasets, ", "))
		}
		writeTenantScope(b, "  tenant scope: ", d)
	}
	if hasCube {
		b.WriteString("\n- Every datasource is READ-ONLY. Against a SQL datasource emit only SELECT/CTE queries; never INSERT, UPDATE, DELETE, MERGE, or DDL.\n")
		b.WriteString("- Pick the datasource whose contents match the question. Use search_tables (it spans all datasources and tags each result with its datasource) when you're unsure which one holds what.\n")
	} else {
		b.WriteString("\n- Every datasource is READ-ONLY. Emit only SELECT/CTE queries; never INSERT, UPDATE, DELETE, MERGE, or DDL.\n")
		b.WriteString("- Pick the datasource whose contents match the question. Use search_tables (it spans all datasources and tags each table with its datasource) when you're unsure which one holds what.\n")
	}
	b.WriteString("- To combine datasources, do it in HOPS: query one datasource, then use a SMALL set of the result values (e.g. the top-N ids you observed) as literal filters in a follow-up query on another datasource. Keep the crossed set small — only values you have actually observed in a result this turn. Do not attempt a cross-datasource join in a single query.\n")
}

// isCube reports whether a datasource has no tables to select from.
func isCube(d DatasourceInfo) bool { return d.EffectiveShape() == gowarehouse.ShapeCube }

// sourceShapes summarises the shapes of the datasources a turn can reach.
//
// Two facts, not one, because they answer different questions. anyCube decides
// whether a blanket "SQL" statement is still TRUE. allCube decides whether a
// table-shaped tool can work AT ALL — on a mixed turn lookup_schema is still
// the right tool for the SQL datasource, and withholding it there would break
// a path that works.
type sourceShapes struct {
	// anyCube: at least one reachable datasource has no tables.
	anyCube bool
	// allCube: every reachable datasource has no tables.
	allCube bool
}

// shapes summarises what this turn can reach. A turn that reaches nothing
// resolves to neither flag: it is not cube-shaped, and it must not have
// table-shaped tools withheld on the strength of an empty set.
func (r turnRouting) shapes() sourceShapes {
	reach := r.reachable()
	if len(reach) == 0 {
		return sourceShapes{}
	}
	s := sourceShapes{allCube: true}
	for _, d := range reach {
		if isCube(d) {
			s.anyCube = true
		} else {
			s.allCube = false
		}
	}
	return s
}

// reachable returns the datasources a query this turn can actually run
// against, which is neither the visible list nor always the project's.
//
// A pinned turn reaches its pin and nothing else: resolveQueryDatasource
// returns the pin and ignores any id the model names. So a turn pinned to a
// SQL warehouse is told SQL even when a cube sits beside it in the project —
// warning it about a source it cannot query is noise at best.
//
// An unpinned turn reaches the WHOLE PROJECT, which is deliberately wider than
// what the prompt lists. The router narrows the visible set, but
// resolveQueryDatasource validates a model-chosen datasource_id against every
// datasource on purpose (the router is a soft prior, not a hard gate), and
// search_tables spans all of them — so a model can discover a cube the router
// did not select and then target it. Reading the narrowed list here would
// promise SELECT-only to a turn that can still reach a source accepting no SQL
// at all.
func (r turnRouting) reachable() []DatasourceInfo {
	// Fall back to the visible set when the project list was not carried, so a
	// routing value assembled without it still resolves to something real.
	pool := r.all
	if len(pool) == 0 {
		pool = r.datasources
	}
	if r.pinned == "" {
		return pool
	}
	for _, d := range pool {
		if d.ID == r.pinned {
			return []DatasourceInfo{d}
		}
	}
	// A pin naming no known datasource cannot run a query at all. Describing
	// the project's other sources would be describing what it will never
	// touch.
	return nil
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
	fmt.Fprintf(b, "- If the result you want to chart is truncated, re-run it so the whole result fits in %d rows, and pick the fix that preserves the question: when the question is about totals or a breakdown, aggregate (GROUP BY); when it is about individual entities — one point per customer, product, or region, e.g. a scatter — do NOT aggregate, because that destroys the question. Add ORDER BY <the measure that matters> DESC LIMIT %d instead, and say so in the title or caption (e.g. \"Top %d customers by spend\") so the chart is not read as the whole population.\n", cfg.ChartableRowCap(), cfg.ChartableRowCap(), cfg.ChartableRowCap())
	b.WriteString("- Keep it readable: few series, clear labels. Types: bar, line, area, pie, scatter, kpi (a single headline figure). Render the chart in its own step, then answer in the next. If the data cannot honestly support a chart, don't force one.\n")
	fmt.Fprintf(b, "- Every title, caption, axis label and series label must be at most %d characters — a longer one is rejected, so write the caption as one short sentence rather than a paragraph. Save the fuller reading for your answer.\n", cfg.ChartCaps.MaxLabelLen)
	b.WriteString("- Set a measure's unit + format when you know what it is: currency values → format \"currency\" with the currency code as unit (e.g. USD, DKK); rates/shares → format \"percent\"; otherwise leave it plain. This only controls display (the renderer keeps the exact value) — it makes large figures read as e.g. $17.4B instead of 17392162956.\n")
}

// writeResultHandling renders the shared RESULT HANDLING block. Shared verbatim
// by both prompt builders.
func writeResultHandling(b *strings.Builder, cfg Config, hasCube bool) {
	b.WriteString("\nRESULT HANDLING\n")
	fmt.Fprintf(b, "- Query results are returned summary-only: row count, columns, and a preview of up to %d rows. Large result sets are truncated.\n", cfg.PreviewRows)
	if hasCube {
		b.WriteString("- For totals, counts, distributions, or \"how many\" questions, make the datasource aggregate — on a SQL datasource that means aggregate SQL (COUNT, SUM, AVG, GROUP BY) — do NOT page through raw rows.\n")
	} else {
		b.WriteString("- For totals, counts, distributions, or \"how many\" questions, write aggregate SQL (COUNT, SUM, AVG, GROUP BY) — do NOT page through raw rows.\n")
	}
	fmt.Fprintf(b, "- You may run at most %d queries and take at most %d steps this turn. Be economical; reuse results already in this conversation instead of re-querying.\n", cfg.MaxQueriesPerTurn, cfg.MaxRounds)
}
