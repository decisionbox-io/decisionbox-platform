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
	// discovery emits the legacy BigQuery-style query
	// (`SELECT * FROM `ds.tab` <filter> LIMIT 5`). This keeps behaviour
	// identical to main for any provider that hasn't been updated yet —
	// the SQL fixer will rewrite it on first use if the dialect differs.
	wh := testutil.NewMockWarehouseProvider("dbo")
	wh.Tables = []string{"orders"}

	sd := newSchemaDiscoveryForTest(t, wh)
	if _, err := sd.DiscoverSchemas(context.Background()); err != nil {
		t.Fatalf("DiscoverSchemas: %v", err)
	}

	got := lastSampleQuery(wh)
	if !strings.Contains(got, "`dbo.orders`") {
		t.Errorf("fallback query should use backtick quoting; got %q", got)
	}
	if !strings.Contains(got, "LIMIT 5") {
		t.Errorf("fallback query should use LIMIT 5; got %q", got)
	}
}
