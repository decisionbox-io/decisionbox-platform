package discovery

import (
	"errors"
	"os"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Providers registered only for these tests, so language resolution runs
// against the real registry — the thing the running process reads.
//
// Registered from init() rather than from a test body: the registry is
// process-global and panics on a duplicate slug, so a registration inside a
// test passes once and takes the package down under `go test -count=2`.
const (
	testSQLSlug        = "discovery-lang-sql"
	testCubeNamedSlug  = "discovery-lang-cube-named"
	testNativeRowsSlug = "discovery-lang-native-rows"
)

func init() {
	factory := func(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		return nil, errors.New("registered for its metadata only")
	}
	// An ordinary SQL warehouse: a display dialect and no language of its own.
	gowarehouse.RegisterWithMeta(testSQLSlug, factory, gowarehouse.ProviderMeta{
		Name: "Lang SQL", Dialect: "PostgreSQL",
	})
	// A cube that names its language outright, the way GA4 does.
	gowarehouse.RegisterWithMeta(testCubeNamedSlug, factory, gowarehouse.ProviderMeta{
		Name:    "Lang cube",
		Dialect: "Report Request (JSON)",
		Capability: gowarehouse.Capability{
			QueryLanguage: "Report Request (JSON)",
			Shape:         gowarehouse.ShapeCube,
			CanAnchor:     gowarehouse.Anchoring(false),
		},
	})
	// A source with TABLES and its own query language — a record system with
	// an API instead of a SQL port. Shape says nothing is unusual about it;
	// only the language does.
	gowarehouse.RegisterWithMeta(testNativeRowsSlug, factory, gowarehouse.ProviderMeta{
		Name:       "Lang native rows",
		Capability: gowarehouse.Capability{QueryLanguage: "Object Query Language"},
	})
}

// sqlOnlyContext is the all-SQL multi-datasource run: the shape every
// multi-warehouse project has today. It exercises every field the routing
// contract renders — label, description, card, per-datasource pack areas — so
// the golden below covers the whole section rather than its opening.
func sqlOnlyContext() *datasourceContext {
	return &datasourceContext{descriptors: []datasourceDescriptor{
		{
			id: "default", label: "Sales PG", provider: "postgres", tableCount: 4,
			description: "orders and invoices",
			card: &models.WarehouseCard{
				SubjectAreas: []string{"sales", "billing"},
				KeyEntities:  []string{"invoice", "customer"},
				KeyMetrics:   []string{"revenue", "arpu"},
			},
		},
		{
			id: "wh_oracle", label: "Catalog", provider: "oracle", tableCount: 7,
			description: "music catalog",
			prompts: &models.ProjectPrompts{AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"g": {Name: "Genre Performance", Enabled: true},
			}},
		},
	}}
}

// withDatasource returns dc plus one more descriptor, leaving dc alone.
func withDatasource(dc *datasourceContext, d datasourceDescriptor) *datasourceContext {
	out := &datasourceContext{descriptors: append([]datasourceDescriptor{}, dc.descriptors...)}
	out.descriptors = append(out.descriptors, d)
	return out
}

// cubeDescriptor is a connected cube as buildDatasourceContext produces one:
// indexed via its catalog, and therefore carrying no tables.
func cubeDescriptor() datasourceDescriptor {
	return datasourceDescriptor{
		id: "wh_analytics", label: "Web Analytics", provider: testCubeNamedSlug,
		description: "site traffic", tableCount: 0,
	}
}

// TestBuildDatasourcesPromptSection_AllSQLIsUnchanged pins the text every
// existing multi-warehouse project receives, byte for byte.
//
// The golden was captured from the tree before the language split existed, so
// a diff against it is a diff against what shipped. It is a fixture, not an
// output: regenerating it to make a failure go away deletes the only evidence
// that a run with no non-SQL datasource still reads exactly as it did.
func TestBuildDatasourcesPromptSection_AllSQLIsUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/datasources_prompt_all_sql.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := buildDatasourcesPromptSection(sqlOnlyContext()); got != string(want) {
		t.Errorf("all-SQL routing contract changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestBuildDatasourcesPromptSection_OneNonSQLDatasourceRewritesTheContract is
// the switch: connecting a source that does not speak SQL is what changes the
// text, and nothing else does.
func TestBuildDatasourcesPromptSection_OneNonSQLDatasourceRewritesTheContract(t *testing.T) {
	sql := buildDatasourcesPromptSection(sqlOnlyContext())
	if !strings.Contains(sql, "This project has multiple SQL datasources.") {
		t.Fatalf("all-SQL run lost its opening:\n%s", sql)
	}

	mixed := buildDatasourcesPromptSection(withDatasource(sqlOnlyContext(), cubeDescriptor()))
	if strings.Contains(mixed, "multiple SQL datasources") {
		t.Errorf("a run carrying a non-SQL datasource still calls them all SQL:\n%s", mixed)
	}
	if strings.Contains(mixed, "Each SQL statement runs against") {
		t.Errorf("a run carrying a non-SQL datasource still routes by SQL statement:\n%s", mixed)
	}
	if !strings.Contains(mixed, "NOT all queried in the same language") {
		t.Errorf("mixed opening missing:\n%s", mixed)
	}
}

// TestBuildDatasourcesPromptSection_MixedNamesEveryLanguage checks the promise
// the mixed opening makes. "One of these is different" is not actionable — the
// model has to be able to tell which language goes with which datasource_id,
// so every datasource is named, the SQL ones included.
func TestBuildDatasourcesPromptSection_MixedNamesEveryLanguage(t *testing.T) {
	dc := &datasourceContext{descriptors: []datasourceDescriptor{
		{id: "default", provider: testSQLSlug, tableCount: 4},
		cubeDescriptor(),
	}}
	got := buildDatasourcesPromptSection(dc)

	if !strings.Contains(got, "Query language: PostgreSQL\n") {
		t.Errorf("the SQL datasource's language is not named:\n%s", got)
	}
	if !strings.Contains(got, "Query language: Report Request (JSON) — not SQL, which this datasource rejects\n") {
		t.Errorf("the cube's language is not named, or does not rule SQL out:\n%s", got)
	}
	if n := strings.Count(got, "Query language:"); n != 2 {
		t.Errorf("expected one language line per datasource, got %d:\n%s", n, got)
	}
}

// TestBuildDatasourcesPromptSection_ACubeDoesNotReportZeroTables covers the
// descriptor line for a source with nothing to count. "0 tables" is true and
// useless: it reads as an empty or broken warehouse, and it invites the one
// action that cannot work against this source.
func TestBuildDatasourcesPromptSection_ACubeDoesNotReportZeroTables(t *testing.T) {
	got := buildDatasourcesPromptSection(withDatasource(sqlOnlyContext(), cubeDescriptor()))

	if strings.Contains(got, "0 tables") {
		t.Errorf("a source with no tables is reported as having zero of them:\n%s", got)
	}
	if !strings.Contains(got, "NO TABLES") {
		t.Errorf("the absence of tables is not stated:\n%s", got)
	}
	if !strings.Contains(got, "`lookup_schema` does not apply to it") {
		t.Errorf("lookup_schema is not ruled out for the cube:\n%s", got)
	}
	// The SQL datasources keep their counts — the line is per datasource, not
	// per run.
	if !strings.Contains(got, "postgres, 4 tables") || !strings.Contains(got, "oracle, 7 tables") {
		t.Errorf("a SQL datasource lost its table count:\n%s", got)
	}
}

// TestBuildDatasourcesPromptSection_ACubeCarryingTablesKeepsTheOrdinaryLine
// guards the claim rather than the shape. "NO TABLES" is a statement about
// this run, so it is read off this run: if a cube-shaped source somehow
// arrives with tables, the model can see them and the prompt must not deny
// they exist.
func TestBuildDatasourcesPromptSection_ACubeCarryingTablesKeepsTheOrdinaryLine(t *testing.T) {
	d := cubeDescriptor()
	d.tableCount = 3
	got := buildDatasourcesPromptSection(withDatasource(sqlOnlyContext(), d))

	if strings.Contains(got, "NO TABLES") {
		t.Errorf("tables the run found are denied:\n%s", got)
	}
	if !strings.Contains(got, "3 tables") {
		t.Errorf("the table count is missing:\n%s", got)
	}
	// It is still not SQL, whatever it is carrying.
	if !strings.Contains(got, "not SQL, which this datasource rejects") {
		t.Errorf("the language is no longer ruled out:\n%s", got)
	}
}

// TestBuildDatasourcesPromptSection_TheHopRuleSurvivesTwoLanguages covers the
// second half of the contract. The hop is taught by example, and the example
// is SQL on both sides; on a mixed run the rule has to be stated in terms of
// the values passed between steps, or it teaches `WHERE ... IN` as the only
// way to receive them.
func TestBuildDatasourcesPromptSection_TheHopRuleSurvivesTwoLanguages(t *testing.T) {
	got := buildDatasourcesPromptSection(withDatasource(sqlOnlyContext(), cubeDescriptor()))

	if !strings.Contains(got, "written in B's OWN query language") {
		t.Errorf("step 2 does not say which language its filter is written in:\n%s", got)
	}
	if !strings.Contains(got, "hops between two SQL datasources") {
		t.Errorf("the worked example is not labelled as SQL-to-SQL:\n%s", got)
	}
	// The rule itself is unchanged, and must be.
	for _, want := range []string{"NO cross-datasource join", "bounded value-passing", "BOUNDED set of key values"} {
		if !strings.Contains(got, want) {
			t.Errorf("mixed contract lost %q:\n%s", want, got)
		}
	}
}

// TestRunHasNonSQLDatasource_ReadsTheLanguageNotTheShape is why the split is
// on the query language. A record system with tables and its own query API is
// entity-shaped — shape says nothing is unusual about it — and telling the
// model it is SQL is just as wrong there as it is for a cube.
func TestRunHasNonSQLDatasource_ReadsTheLanguageNotTheShape(t *testing.T) {
	cases := map[string]struct {
		providers []string
		want      bool
	}{
		"all SQL":                    {[]string{testSQLSlug, testSQLSlug}, false},
		"unregistered slugs are SQL": {[]string{"postgres", "oracle"}, false},
		"a cube":                     {[]string{testSQLSlug, testCubeNamedSlug}, true},
		"a bare cube, no language":   {[]string{testSQLSlug, testCubeSlug}, true},
		"tables, but not SQL":        {[]string{testSQLSlug, testNativeRowsSlug}, true},
	}
	for name, tc := range cases {
		dc := &datasourceContext{}
		for i, p := range tc.providers {
			dc.descriptors = append(dc.descriptors, datasourceDescriptor{id: string(rune('a' + i)), provider: p})
		}
		if got := runHasNonSQLDatasource(dc); got != tc.want {
			t.Errorf("%s: runHasNonSQLDatasource = %v, want %v", name, got, tc.want)
		}
	}
}

// TestDatasourceQueryLanguage covers the name each datasource is given, and
// the one answer that must never come back: a source that declared it is not
// SQL being labelled SQL because it declared it the other way.
func TestDatasourceQueryLanguage(t *testing.T) {
	cases := map[string]struct {
		slug string
		want string
	}{
		"a SQL warehouse gets its dialect": {testSQLSlug, "PostgreSQL"},
		"an unregistered slug is SQL":      {"not-registered-anywhere", "SQL"},
		"a cube that names its language":   {testCubeNamedSlug, "Report Request (JSON) — not SQL, which this datasource rejects"},
		"a source with tables but not SQL": {testNativeRowsSlug, "Object Query Language — not SQL, which this datasource rejects"},
	}
	for name, tc := range cases {
		if got := datasourceQueryLanguage(tc.slug); got != tc.want {
			t.Errorf("%s: datasourceQueryLanguage(%q) = %q, want %q", name, tc.slug, got, tc.want)
		}
	}

	// A cube that names its language NOWHERE — no QueryLanguage, no display
	// dialect. ProviderMeta.Language() answers "SQL" for it, which is the one
	// answer this line exists to prevent; the shape has already said there is
	// no SQL to write.
	got := datasourceQueryLanguage(testCubeSlug)
	if got == "SQL" || !strings.Contains(got, "not SQL") {
		t.Errorf("a cube declaring no language was labelled %q", got)
	}
}
