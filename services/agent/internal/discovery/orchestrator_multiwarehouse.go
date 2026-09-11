package discovery

// Multi-warehouse (multi-hop) discovery support. A project can attach
// several datasources — not necessarily all queried in the same language;
// the exploration agent sees every datasource's catalog, targets one
// datasource per query via `datasource_id`, and hops between them across
// steps (bounded value-passing — never a cross-engine join). This file builds the per-datasource execution
// context the orchestrator hands the exploration engine; the single-
// warehouse path in orchestrator.go is unchanged and taken whenever a run
// has one datasource. Design: docs/design/multi-warehouse.md §5.5, §5.9.

import (
	"context"
	"fmt"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// datasourceContext is the per-datasource execution context built for a
// multi-warehouse run: one query executor per datasource, the merged
// catalog + table→datasource index the cross-warehouse schema provider
// serves from, and the ordered descriptors the prompt + grouped catalog
// render from.
type datasourceContext struct {
	executors      map[string]*queryexec.QueryExecutor // keyed by normalised datasource id
	mergedSchemas  map[string]models.TableSchema       // union of every datasource's tables
	tableWarehouse map[string]string                   // canonical table → owning datasource id
	// catalogRefs holds each catalog-shaped datasource's items, keyed by
	// datasource id. A datasource with no tables contributes nothing to
	// mergedSchemas, so without this it has no authority in the
	// cross-datasource search and every one of its hits is dropped — it stays
	// invisible to discovery even after indexing successfully.
	catalogRefs map[string][]string
	schemasByDS map[string]map[string]models.TableSchema
	descriptors []datasourceDescriptor // ordered, primary first
}

// datasourceDescriptor is the prompt/catalog-facing view of one datasource.
type datasourceDescriptor struct {
	id          string
	label       string
	description string
	provider    string
	domain      string
	card        *models.WarehouseCard
	prompts     *models.ProjectPrompts
	datasets    []string
	tableCount  int
}

// isMultiWarehouse reports whether this run spans more than one datasource,
// i.e. the caller wired providers + configs for secondaries. A single-
// warehouse run (or a legacy project) takes the unchanged primary-only
// path in RunDiscovery.
func (o *Orchestrator) isMultiWarehouse() bool {
	return len(o.warehouseProviders) > 1 && len(o.warehouses) > 1
}

// buildDatasourceContext assembles the per-datasource execution context for
// a multi-warehouse run: a query executor per datasource (its own dialect,
// datasets and filter), each datasource's cached schemas merged into one
// catalog with a table→datasource index, and the descriptors the prompt +
// grouped catalog render from. A secondary whose provider or cached schema
// is missing is skipped with a warning so the run degrades to the
// datasources that ARE ready rather than failing outright; the primary is
// required.
func (o *Orchestrator) buildDatasourceContext(ctx context.Context) (*datasourceContext, error) {
	primaryID := normDatasourceID(o.warehouseID)
	dc := &datasourceContext{
		executors:      make(map[string]*queryexec.QueryExecutor, len(o.warehouses)),
		mergedSchemas:  make(map[string]models.TableSchema),
		tableWarehouse: make(map[string]string),
		schemasByDS:    make(map[string]map[string]models.TableSchema),
		catalogRefs:    make(map[string][]string),
	}

	for _, wh := range orderWarehousesPrimaryFirst(o.warehouses, primaryID) {
		id := normDatasourceID(wh.ID)

		provider := o.warehouseProviders[id]
		if provider == nil {
			if id == primaryID {
				return nil, fmt.Errorf("multi-warehouse discovery: primary datasource %q has no warehouse provider", id)
			}
			applog.WithField("datasource_id", id).Warn("multi-warehouse discovery: no provider for secondary datasource; skipping it")
			continue
		}

		schemas, err := o.schemaCache.Find(ctx, o.projectID, id, WarehouseConfigHash(wh))
		if err != nil {
			if id == primaryID {
				return nil, fmt.Errorf("multi-warehouse discovery: schema cache lookup for primary datasource %q: %w", id, err)
			}
			applog.WithError(err).WithField("datasource_id", id).Warn("multi-warehouse discovery: schema cache miss for secondary datasource; skipping it")
			continue
		}
		// Load whatever catalog this datasource has. Without it a catalog
		// source contributes nothing to the merged tables AND has no entry in
		// the search authority, so it is dropped from every cross-datasource
		// result — indexed, present in the prompt, and permanently silent.
		refs, refErr := CatalogRefsFor(ctx, o.schemaCache, o.projectID, id, WarehouseConfigHash(wh))
		if refErr != nil {
			applog.WithError(refErr).WithField("datasource_id", id).
				Warn("multi-warehouse discovery: catalog cache lookup failed; this datasource's catalog items will not be searchable")
		}

		// Distinguish "indexed, and it legitimately has no tables" from "not
		// indexed at all". Only the catalog tells them apart, and conflating
		// them is the worse direction: the datasource would be wired in and
		// described to the model while every hit it produced was filtered
		// away, so it would look available and answer nothing. A datasource
		// with neither tables nor catalog has simply never been indexed, and
		// is treated exactly as any other cache miss here.
		if !datasourceIsIndexed(schemas, refs) {
			if id == primaryID {
				return nil, fmt.Errorf("multi-warehouse discovery: primary datasource %q is not indexed — re-index required (POST /api/v1/projects/%s/reindex)", id, o.projectID)
			}
			applog.WithField("datasource_id", id).
				Warn("multi-warehouse discovery: secondary datasource is not indexed (no tables and no catalog); skipping it")
			continue
		}

		if schemas == nil {
			// A catalog source legitimately has no tables, and by here we know
			// it is indexed. An empty map says "indexed, and it has none";
			// leaving it nil would read as unindexed further down.
			schemas = map[string]models.TableSchema{}
		}
		if len(refs) > 0 {
			dc.catalogRefs[id] = refs
		}

		// Per-datasource executor: its own dialect, datasets and filter, so
		// each statement is dialect-correct and governed by that datasource.
		datasetsStr := strings.Join(wh.GetDatasets(), ", ")
		fixer := o.newQueryFixer(provider, datasetsStr, filterClause(wh.FilterField, wh.FilterValue))
		fixer.SetSchemaContext((&SchemaContextBuilder{Schemas: schemas}).BuildCatalog(nil).Catalog)
		dc.executors[id] = queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
			Warehouse:    provider,
			ProviderSlug: wh.Provider,
			SQLFixer:     fixer,
			// Per-datasource debug logger so every warehouse-query debug row
			// (exploration AND validation, which reuses these executors) is
			// stamped with THIS datasource's provider + id, not the primary.
			DebugLogger: o.debugLogger.ForWarehouse(id, wh.Provider),
			MaxRetries:  5,
			FilterField: wh.FilterField,
			FilterValue: wh.FilterValue,
		})

		// Merge into the cross-warehouse catalog + table→datasource index.
		// Primary-first order means that on a cross-datasource name
		// collision (identical dataset.table — the documented Risk R1 edge)
		// the primary wins the merged entry; cross-warehouse search still
		// attributes each hit from its own index payload, so routing stays
		// correct for the non-colliding majority.
		schemasCopy := make(map[string]models.TableSchema, len(schemas))
		for name, ts := range schemas {
			schemasCopy[name] = ts
			if _, exists := dc.mergedSchemas[name]; !exists {
				dc.mergedSchemas[name] = ts
				dc.tableWarehouse[name] = id
			}
		}
		dc.schemasByDS[id] = schemasCopy

		dc.descriptors = append(dc.descriptors, datasourceDescriptor{
			id:          id,
			label:       wh.Label,
			description: wh.Description,
			provider:    wh.Provider,
			domain:      wh.Domain,
			card:        wh.Card,
			prompts:     wh.Prompts,
			datasets:    wh.GetDatasets(),
			tableCount:  len(schemas),
		})
	}

	if len(dc.executors) == 0 {
		return nil, fmt.Errorf("multi-warehouse discovery: no datasource could be initialised")
	}
	return dc, nil
}

// buildGroupedCatalog renders the cross-warehouse catalog the LLM sees in
// its system prompt: one section per datasource, each headed with the
// datasource id (so the model learns which datasource_id owns each table)
// followed by that datasource's one-line-per-table catalog.
func (o *Orchestrator) buildGroupedCatalog(dc *datasourceContext, keywords []string) *Rendered {
	var b strings.Builder
	tokens, dropped := 0, 0
	for _, d := range dc.descriptors {
		schemas := dc.schemasByDS[d.id]
		if len(schemas) == 0 {
			continue
		}
		rr := (&SchemaContextBuilder{Schemas: schemas}).BuildCatalog(keywords)
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "### Datasource `%s`%s — %d tables (dialect: %s)\n",
			d.id, labelSuffix(d.label), d.tableCount, d.provider)
		fmt.Fprintf(&b, "Set `datasource_id: \"%s\"` on query_data / lookup_schema for these tables.\n", d.id)
		b.WriteString(rr.Catalog)
		tokens += rr.CatalogTokens
		dropped += rr.CatalogDropped
	}
	return &Rendered{Catalog: b.String(), CatalogTokens: tokens, CatalogDropped: dropped}
}

// buildDatasourcesPromptSection renders the routing contract the model
// follows on a multi-warehouse run: one datasource per query, hop across
// datasources between steps, never join across them, plus each
// datasource's routing card + its own domain-pack focus areas so the agent
// applies the right playbook when it explores that datasource.
//
// The contract comes in two forms. A run whose datasources are all SQL gets
// the text it has always had, unchanged. A run carrying a datasource that is
// not queried in SQL gets one that names each datasource's language instead
// of assuming a single one — see runHasNonSQLDatasource for why the split is
// on the language rather than on the shape.
func buildDatasourcesPromptSection(dc *datasourceContext) string {
	mixed := runHasNonSQLDatasource(dc)
	var b strings.Builder
	b.WriteString("\n\n## Datasources (multi-warehouse)\n\n")
	if mixed {
		writeMixedLanguageRouting(&b)
	} else {
		writeSQLRouting(&b)
	}
	b.WriteString("\nAvailable datasources:\n")
	for _, d := range dc.descriptors {
		writeDatasourceHeadline(&b, d, mixed)
		if d.description != "" {
			fmt.Fprintf(&b, "  - %s\n", d.description)
		}
		if d.card != nil {
			if len(d.card.SubjectAreas) > 0 {
				fmt.Fprintf(&b, "  - Subject areas: %s\n", strings.Join(d.card.SubjectAreas, ", "))
			}
			if len(d.card.KeyEntities) > 0 {
				fmt.Fprintf(&b, "  - Key entities: %s\n", strings.Join(d.card.KeyEntities, ", "))
			}
			if len(d.card.KeyMetrics) > 0 {
				fmt.Fprintf(&b, "  - Key metrics: %s\n", strings.Join(d.card.KeyMetrics, ", "))
			}
		}
		if areas := datasourceFocusAreas(d.prompts); areas != "" {
			fmt.Fprintf(&b, "  - Focus areas (this datasource's domain pack): %s\n", areas)
		}
	}
	return b.String()
}

// runHasNonSQLDatasource reports whether any datasource on this run is queried
// in something other than SQL — i.e. whether a blanket "these are all SQL
// datasources" is still a true thing to tell the model.
//
// The split is on the LANGUAGE, not on the shape, because that is the
// statement being made: "these are all SQL datasources" is false the moment
// one of them accepts something else, whether or not it has tables. Shape and
// language are separate declarations and a source may differ in either — a
// record-shaped source with its own query API has tables and no SQL, and
// reading its shape would call it SQL.
//
// The answer comes from the registry rather than from a live provider,
// because a provider reaches this run through middleware and a wrapper that
// does not re-expose the query seam erases the declaration. A registration
// cannot be wrapped.
func runHasNonSQLDatasource(dc *datasourceContext) bool {
	for _, d := range dc.descriptors {
		if gowarehouse.NonSQLLanguageOf(d.provider) != "" {
			return true
		}
	}
	return false
}

// datasourceQueryLanguage renders the language line's value for one
// datasource: the name of the language its queries are written in, and, when
// that is not SQL, the fact that SQL will be rejected.
//
// NonSQLLanguageOf answers first because ProviderMeta.Language() falls back to
// "SQL" for a source that declares a shape and no language — which is exactly
// the source this section exists to stop mislabelling. An unregistered slug
// resolves to SQL: what every provider was before the descriptor existed.
func datasourceQueryLanguage(slug string) string {
	if lang := gowarehouse.NonSQLLanguageOf(slug); lang != "" {
		return lang + " — not SQL, which this datasource rejects"
	}
	if meta, ok := gowarehouse.GetProviderMeta(slug); ok {
		return meta.Language()
	}
	return "SQL"
}

// writeSQLRouting states the routing contract for a run whose datasources are
// all SQL — every multi-warehouse project that exists today. The text is
// unchanged and must stay that way: it is true for these runs, and a golden
// test holds it to the byte.
func writeSQLRouting(b *strings.Builder) {
	b.WriteString("This project has multiple SQL datasources. Each SQL statement runs against exactly ONE datasource: set `datasource_id` on every `query_data` and `lookup_schema` action to the datasource that owns the tables you use (omitting it targets the primary). `search_tables` spans ALL datasources and tags each result with its `datasource`.\n")
	b.WriteString("\nA single `query_data` statement may reference ONLY tables that live in its `datasource_id`. There is NO cross-engine federation and NO cross-datasource join. To correlate data across datasources, HOP across steps with bounded value-passing:\n")
	b.WriteString("  1. Query datasource A for a BOUNDED set of key values (top-N ids / keys / an aggregate).\n")
	b.WriteString("  2. In the next step, query datasource B with those values inlined as literal filters (e.g. `WHERE id IN (1,2,3)`).\n")
	writeHopExample(b)
}

// writeMixedLanguageRouting states the same contract for a run carrying a
// datasource that is not queried in SQL.
//
// Four things change, and only these four. The opening no longer calls every
// datasource SQL. `lookup_schema` is scoped to the datasources that have
// tables, because a metric name is close enough to a table name that looking
// one up is the natural next move and it fails every time. The hop's second
// step is stated as a filter written in the target's own language rather than
// as a `WHERE ... IN` clause. And the section claims precedence over the
// query examples above it.
//
// That last one is the smallest fix to a contradiction this section does not
// own. What precedes it is the PROJECT's exploration prompt — its primary
// datasource's domain pack, since a source that cannot anchor is never the
// primary — and that pack teaches one action contract, with worked examples,
// for whatever language it was written for. A non-primary datasource's own
// pack contributes only its analysis-area names to a run; its prompts are
// never rendered. So on a mixed run the model reads a full action contract in
// one language and then a list of datasources that do not all speak it.
//
// Stating which one wins is what this section can honestly do from where it
// sits, and it is stated in terms of precedence rather than of SQL so it stays
// true whichever language the project's pack was written in. Rewriting or
// suppressing that pack's action contract per datasource is a larger change
// to how a run assembles its prompt, and it is tracked separately.
//
// The worked example stays SQL-to-SQL, and says so. Every non-SQL source
// today is one that cannot anchor, so it is never a project's only datasource
// and such a run always has a SQL side for the example to be live for. It is
// labelled rather than assumed because that is what keeps it honest if that
// stops being true: an example marked SQL-to-SQL, next to a rule stated in
// terms of each datasource's own language, teaches the rule either way.
// Writing a second example in some specific non-SQL request format would
// teach a syntax this package has no provider for and no way to keep true.
func writeMixedLanguageRouting(b *strings.Builder) {
	b.WriteString("This project has multiple datasources and they are NOT all queried in the same language — each one's language is named below, and a query written in the wrong one is rejected. Each query runs against exactly ONE datasource: set `datasource_id` on every `query_data` and `lookup_schema` action to the datasource you mean (omitting it targets the primary). `lookup_schema` applies only to a datasource that has tables. `search_tables` spans ALL datasources and tags each result with its `datasource`.\n")
	b.WriteString("Where the query examples earlier in this prompt disagree with a datasource's language named here, THIS section wins: the action and its `datasource_id` are unchanged, but the `query` field carries whatever that datasource accepts.\n")
	b.WriteString("\nA single `query_data` request may reference ONLY what lives in its `datasource_id`. There is NO cross-engine federation and NO cross-datasource join. To correlate data across datasources, HOP across steps with bounded value-passing:\n")
	b.WriteString("  1. Query datasource A for a BOUNDED set of key values (top-N ids / keys / an aggregate).\n")
	b.WriteString("  2. In the next step, query datasource B for those values, inlined as literal filters written in B's OWN query language — `WHERE id IN (1,2,3)` where that language is SQL, the equivalent restriction in its own request format where it is not.\n")
	b.WriteString("  (The worked example below hops between two SQL datasources. The rule is the same whatever the two languages are; only the filter's syntax changes.)\n")
	writeHopExample(b)
}

// writeHopExample shows the hop as one wrong statement and the right two
// steps. Shared by both contracts so they cannot drift apart on the part
// neither of them disputes.
func writeHopExample(b *strings.Builder) {
	b.WriteString("  WRONG (fails — invoice_line is not in the Oracle datasource):\n")
	b.WriteString("    {\"datasource_id\":\"wh_oracle\",\"query\":\"SELECT g.name, SUM(il.unit_price) FROM CHINOOK.TRACK t JOIN public.invoice_line il ON ... JOIN CHINOOK.GENRE g ...\"}\n")
	b.WriteString("  RIGHT (two steps): step 1 `{\"datasource_id\":\"default\",\"query\":\"SELECT track_id, SUM(unit_price*quantity) AS rev FROM public.invoice_line GROUP BY track_id ORDER BY rev DESC LIMIT 20\"}`; step 2 `{\"datasource_id\":\"wh_oracle\",\"query\":\"SELECT t.track_id, g.name FROM CHINOOK.TRACK t JOIN CHINOOK.GENRE g ON t.genre_id=g.genre_id WHERE t.track_id IN (<the 20 ids from step 1>)\"}`.\n")
}

// writeDatasourceHeadline renders one datasource's opening line.
//
// On an all-SQL run this is the line it has always been: id, label, provider,
// table count. On a mixed run every datasource also names the language its
// queries are written in — including the SQL ones, since "the other one is
// different" is not something a model can act on — and a datasource that has
// no tables says so rather than reporting "0 tables", a count that reads as
// an empty or broken warehouse when the truth is that it has none to count.
//
// The no-tables line is read off the run as well as the registry: a cube that
// somehow arrived carrying tables gets the ordinary line, because whatever
// its shape says, the model can see them.
func writeDatasourceHeadline(b *strings.Builder, d datasourceDescriptor, named bool) {
	if named && d.tableCount == 0 && providerIsCube(d.provider) {
		fmt.Fprintf(b, "\n- **`%s`**%s — %s, NO TABLES: a metric/dimension cube. `lookup_schema` does not apply to it; `search_tables` lists its metrics and dimensions.\n",
			d.id, labelSuffix(d.label), d.provider)
	} else {
		fmt.Fprintf(b, "\n- **`%s`**%s — %s, %d tables\n", d.id, labelSuffix(d.label), d.provider, d.tableCount)
	}
	if named {
		fmt.Fprintf(b, "  - Query language: %s\n", datasourceQueryLanguage(d.provider))
	}
}

// datasourceFocusAreas lists the enabled analysis-area names from a
// datasource's own domain-pack prompts, so the agent knows what THIS
// datasource is meant to surface. Empty when the datasource has no
// per-warehouse prompts (it then inherits the project-level areas).
func datasourceFocusAreas(prompts *models.ProjectPrompts) string {
	if prompts == nil || len(prompts.AnalysisAreas) == 0 {
		return ""
	}
	names := make([]string, 0, len(prompts.AnalysisAreas))
	for _, a := range prompts.AnalysisAreas {
		if a.Enabled && a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return strings.Join(names, ", ")
}

// buildValidationRouting builds one verifier WarehouseInfo + Executor per
// datasource for a multi-warehouse run, so the validation phase verifies
// each insight / recommendation against the datasource it is about (its own
// dialect + connection) rather than always the primary. The schema provider
// is the shared cross-warehouse one (the verifier can look up any
// datasource's tables); only the QueryExec + dialect are per datasource.
// Returns (nil, nil) on a single-warehouse run (dc nil) so the validation
// phase keeps its primary-only fallback.
func (o *Orchestrator) buildValidationRouting(dc *datasourceContext, schemaProvider ai.SchemaProvider, stepByID map[int]*models.ExplorationStep, cfg verifier.Config, caps verifier.RunCaps) (map[string]verifier.WarehouseInfo, map[string]verifier.Executor) {
	if dc == nil {
		return nil, nil
	}
	whByDS := make(map[string]verifier.WarehouseInfo, len(dc.executors))
	exByDS := make(map[string]verifier.Executor, len(dc.executors))
	for _, wh := range o.warehouses {
		id := normDatasourceID(wh.ID)
		prov := o.warehouseProviders[id]
		ex := dc.executors[id]
		if prov == nil || ex == nil {
			continue
		}
		whByDS[id] = verifier.WarehouseInfo{
			Dialect:     prov.SQLDialect(),
			Dataset:     prov.GetDataset(),
			FilterField: wh.FilterField,
			FilterValue: wh.FilterValue,
		}
		exByDS[id] = &verifier.DefaultExecutor{
			SchemaProvider:      schemaProvider,
			QueryExec:           ex,
			StepByID:            stepByID,
			Cfg:                 cfg.Bundle,
			MaxReadStepRowsCall: caps.MaxReadStepRowsCall,
		}
	}
	return whByDS, exByDS
}

// normDatasourceID maps the empty warehouse id to the reserved default so
// an omitted id, a legacy single-warehouse project, and a literal "default"
// all resolve to the same executor / catalog section. Mirrors the ai
// package's helper of the same name (kept local to avoid coupling the
// discovery orchestrator to the exploration engine's internals).
// datasourceIsIndexed reports whether a datasource has anything indexed at
// all — tables, or a catalog.
//
// It exists because "no tables" is ambiguous on its own: a catalog source has
// none by nature, and an unindexed source has none because nothing ran. Only
// the catalog separates them, and conflating them is the worse direction. An
// unindexed datasource accepted as "indexed and empty" is wired in and
// described to the model while every hit it produces is filtered away — it
// looks available and answers nothing, which is far harder to diagnose than
// being told to re-index.
func datasourceIsIndexed(schemas map[string]models.TableSchema, catalogRefs []string) bool {
	return len(schemas) > 0 || len(catalogRefs) > 0
}

func normDatasourceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.DefaultWarehouseID
	}
	return id
}

// filterClause mirrors Orchestrator.buildFilterClause for an arbitrary
// datasource's filter field/value (the method is bound to the primary).
func filterClause(field, value string) string {
	if field == "" || value == "" {
		return ""
	}
	return fmt.Sprintf("WHERE %s = '%s'", field, value)
}

// labelSuffix renders " (label)" when a datasource has a human label, else
// "" — used to annotate the datasource id in prompts without hard-coding a
// separator at each call site.
func labelSuffix(label string) string {
	if strings.TrimSpace(label) == "" {
		return ""
	}
	return " (" + label + ")"
}

// orderWarehousesPrimaryFirst returns the configs with the primary first,
// preserving the relative order of the rest, so descriptors + the catalog
// lead with the primary (the default target + exec-summary anchor).
func orderWarehousesPrimaryFirst(warehouses []models.WarehouseConfig, primaryID string) []models.WarehouseConfig {
	out := make([]models.WarehouseConfig, 0, len(warehouses))
	for _, wh := range warehouses {
		if normDatasourceID(wh.ID) == primaryID {
			out = append(out, wh)
		}
	}
	for _, wh := range warehouses {
		if normDatasourceID(wh.ID) != primaryID {
			out = append(out, wh)
		}
	}
	return out
}

// runReachesCube reports whether exploration on this run can query a
// cube-shaped datasource.
//
// Routable, not merely configured. A secondary whose provider failed to open,
// or whose schema was never indexed, is dropped before the engine is wired —
// the run carries the configuration but cannot query it. Reading the project
// instead would put a run that can only reach tables onto the rule chosen for
// cubes and drop its step floor, in exactly the degraded case where a run
// least deserves a behaviour change.
//
// It decides which stopping rule the exploration engine uses: on a run that
// can only reach tables, a step count remains a passable proxy for coverage
// and the existing floor is left exactly as it was, so every SQL-only run
// behaves identically. A run that can reach a cube gets the marginal-value
// rule instead, because a cube offers a combinatorial number of slices and
// any step count can be satisfied without covering anything.
//
// Shape comes from the provider registry by slug, which is where a provider
// declares it. The primary's slug is consulted as well as the configured
// datasources: a legacy single-warehouse run carries no warehouses[] entries
// at all, and reading only those would report every such run as table-shaped
// without ever looking.
//
// An unregistered slug is table-shaped, matching the descriptor's own
// default. That is the conservative direction here: it keeps the floor, which
// is today's behaviour, rather than switching a run to a rule chosen for a
// source nothing can confirm is a cube.
func runReachesCube(routable map[string]*queryexec.QueryExecutor, warehouses []models.WarehouseConfig, primarySlug string) bool {
	// No per-datasource executors is the single-warehouse wiring: the engine
	// falls back to one executor and the primary is the only thing it can
	// query. It is also the legacy shape, which carries no warehouses[]
	// entries at all — reading only those would report every such run as
	// table-shaped without ever looking.
	if len(routable) == 0 {
		return providerIsCube(primarySlug)
	}
	for _, wh := range warehouses {
		if _, wired := routable[normDatasourceID(wh.ID)]; !wired {
			continue
		}
		if providerIsCube(wh.Provider) {
			return true
		}
	}
	return false
}

// providerIsCube resolves one provider slug's declared shape.
func providerIsCube(slug string) bool {
	if slug == "" {
		return false
	}
	meta, ok := gowarehouse.GetProviderMeta(slug)
	if !ok {
		return false
	}
	return meta.EffectiveShape() == gowarehouse.ShapeCube
}
