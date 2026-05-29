package discovery

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// sampleQueryBuilderMock wraps MockWarehouseProvider and adds a
// SampleQueryBuilder implementation. Used to verify that schema_discovery
// routes sample-data queries through the provider's own dialect when one
// is available, rather than falling back to the BigQuery-style legacy.
type sampleQueryBuilderMock struct {
	*testutil.MockWarehouseProvider
	// lastBuilt records what SampleQuery returned on the most recent call —
	// the test asserts the same string surfaced in warehouse.Query.
	lastBuilt string
}

func (s *sampleQueryBuilderMock) SampleQuery(dataset, table, filterClause string, limit int) string {
	// Signature is distinctive so we can recognise it in the recorded
	// query stream. Matches the shape providers use (native-dialect
	// T-SQL / Postgres / Snowflake / etc. — the actual text isn't
	// important for this test, just that the custom builder was used).
	s.lastBuilt = "/*builder*/ SELECT * FROM " + dataset + "." + table + " " + filterClause + " /*limit=" + itoa(limit) + "*/"
	return s.lastBuilt
}

func itoa(n int) string {
	// A tiny formatter avoids pulling strconv into a test that's already
	// exercising string inspection.
	if n == 0 {
		return "0"
	}
	digits := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = string(rune('0'+(n%10))) + digits
		n /= 10
	}
	if neg {
		return "-" + digits
	}
	return digits
}

var _ gowarehouse.SampleQueryBuilder = (*sampleQueryBuilderMock)(nil)

// newSchemaDiscoveryForTest wires a SchemaDiscovery with a minimal query
// executor pointing at the supplied warehouse. maxRetries=0 so a single
// failure aborts — important so tests don't silently swallow SQL errors.
func newSchemaDiscoveryForTest(t *testing.T, wh gowarehouse.Provider) *SchemaDiscovery {
	t.Helper()
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 0,
	})
	return NewSchemaDiscovery(SchemaDiscoveryOptions{
		Warehouse: wh,
		Executor:  exec,
		ProjectID: "test-project",
		Datasets:  []string{"dbo"},
	})
}

// lastSampleQuery returns the SQL of the most recent Query call recorded
// by MockWarehouseProvider — the sample-data query emitted by
// schema_discovery for the last table it processed.
func lastSampleQuery(m *testutil.MockWarehouseProvider) string {
	for i := len(m.Calls) - 1; i >= 0; i-- {
		if m.Calls[i].Method == "Query" {
			return m.Calls[i].Query
		}
	}
	return ""
}

func TestSchemaDiscovery_SampleQuery_UsesBuilderWhenProviderImplements(t *testing.T) {
	// Given a provider that implements SampleQueryBuilder, the sample-data
	// query emitted during schema discovery must be the one returned by
	// the builder — NOT the generic BigQuery-style fallback. Without this
	// every non-BigQuery provider pays a per-table LLM round-trip to fix
	// the dialect before its first sample-data query can succeed.
	base := testutil.NewMockWarehouseProvider("dbo")
	base.Tables = []string{"orders"}
	wh := &sampleQueryBuilderMock{MockWarehouseProvider: base}

	sd := newSchemaDiscoveryForTest(t, wh)
	if _, err := sd.DiscoverSchemas(context.Background()); err != nil {
		t.Fatalf("DiscoverSchemas: %v", err)
	}

	got := lastSampleQuery(base)
	if !strings.Contains(got, "/*builder*/") {
		t.Errorf("sample query did not route through builder; got %q", got)
	}
	if !strings.Contains(got, "dbo.orders") {
		t.Errorf("sample query missing qualified name; got %q", got)
	}
	if !strings.Contains(got, "/*limit=5*/") {
		t.Errorf("sample query missing limit; got %q", got)
	}
}

func TestSchemaDiscovery_SampleQuery_FallsBackWhenProviderDoesNotImplement(t *testing.T) {
	// When the provider does NOT implement SampleQueryBuilder, schema
	// discovery emits a generic SELECT … LIMIT N. The qualified table
	// ref is rendered via the provider's QuoteRef so the fallback uses
	// the dialect's identifier-quoting convention (per-part quoting
	// joined by dots) rather than BigQuery's single-backtick form.
	// The shipped MockWarehouseProvider returns backtick-per-part
	// quoting (matching BQ / Databricks); other dialects render with
	// square brackets or double quotes.
	wh := testutil.NewMockWarehouseProvider("dbo")
	wh.Tables = []string{"orders"}

	sd := newSchemaDiscoveryForTest(t, wh)
	if _, err := sd.DiscoverSchemas(context.Background()); err != nil {
		t.Fatalf("DiscoverSchemas: %v", err)
	}

	got := lastSampleQuery(wh)
	if !strings.Contains(got, "`dbo`.`orders`") {
		t.Errorf("fallback query should use dialect-quoted qualified name; got %q", got)
	}
	if !strings.Contains(got, "LIMIT 5") {
		t.Errorf("fallback query should use LIMIT 5; got %q", got)
	}
}

// refQualifierMock wraps MockWarehouseProvider and adds a RefQualifier
// implementation that prepends a data project — the BigQuery cross-project
// shape. Used to verify schema discovery keys the schema map (and the
// schema-cache key, since Save uses the same map key) on the provider's
// fully-qualified three-part name instead of the plain dataset.table form.
type refQualifierMock struct {
	*testutil.MockWarehouseProvider
}

func (r *refQualifierMock) QualifiedName(dataset, table string) string {
	return "data-proj." + dataset + "." + table
}

var _ gowarehouse.RefQualifier = (*refQualifierMock)(nil)

func TestSchemaDiscovery_QualifiedName_UsesRefQualifierWhenProviderImplements(t *testing.T) {
	// A provider implementing RefQualifier (BigQuery cross-project reads)
	// must have its three-part name used as BOTH the schema-map key and the
	// TableName, so the catalog the LLM sees and the cache key resolve
	// against the data project. A two-part dataset.table would 404 on
	// BigQuery cross-project reads.
	base := testutil.NewMockWarehouseProvider("dbo")
	base.Tables = []string{"orders"}
	wh := &refQualifierMock{MockWarehouseProvider: base}

	sd := newSchemaDiscoveryForTest(t, wh)
	schemas, err := sd.DiscoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSchemas: %v", err)
	}

	const want = "data-proj.dbo.orders"
	sch, ok := schemas[want]
	if !ok {
		keys := make([]string, 0, len(schemas))
		for k := range schemas {
			keys = append(keys, k)
		}
		t.Fatalf("schema map not keyed on qualified name %q; got keys %v", want, keys)
	}
	if sch.TableName != want {
		t.Errorf("TableName = %q, want %q", sch.TableName, want)
	}
}

func TestSchemaDiscovery_QualifiedName_FallsBackToTwoPartWhenNotImplemented(t *testing.T) {
	// Providers that don't implement RefQualifier (every single-project
	// warehouse) keep the plain dataset.table key — byte-for-byte
	// pre-feature behaviour.
	wh := testutil.NewMockWarehouseProvider("dbo")
	wh.Tables = []string{"orders"}

	sd := newSchemaDiscoveryForTest(t, wh)
	schemas, err := sd.DiscoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSchemas: %v", err)
	}

	if _, ok := schemas["dbo.orders"]; !ok {
		t.Errorf("expected plain two-part key %q in %v", "dbo.orders", schemas)
	}
}
