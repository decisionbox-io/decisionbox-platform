package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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

// listTablesFailingMock fails ListTablesInDataset for the datasets named in
// failFor, and behaves like the shared mock for every other one. A refused
// connection reaches schema discovery exactly here: the provider cannot list,
// and the dataset is skipped.
type listTablesFailingMock struct {
	*testutil.MockWarehouseProvider
	failFor map[string]error
}

func (l *listTablesFailingMock) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	if err, bad := l.failFor[dataset]; bad {
		return nil, err
	}
	return l.MockWarehouseProvider.ListTablesInDataset(ctx, dataset)
}

func newSchemaDiscoveryOver(t *testing.T, wh gowarehouse.Provider, datasets ...string) *SchemaDiscovery {
	t.Helper()
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 0})
	return NewSchemaDiscovery(SchemaDiscoveryOptions{
		Warehouse: wh,
		Executor:  exec,
		ProjectID: "test-project",
		Datasets:  datasets,
	})
}

// TestDiscoverSchemas_EveryDatasetFailedIsReportedWithItsReason.
//
// The case this exists for is a real one: a project configured with
// `sslmode=require` against a Postgres without TLS. The provider's error says
// so exactly; discovery skipped the dataset, returned an empty map and a NIL
// error, and the index reported "no tables discovered — check datasets and
// warehouse permissions". The operator was sent to look at dataset names and
// grants while the product was holding the answer.
func TestDiscoverSchemas_EveryDatasetFailedIsReportedWithItsReason(t *testing.T) {
	refused := errors.New("postgres: list tables failed: pq: SSL is not enabled on the server")
	wh := &listTablesFailingMock{
		MockWarehouseProvider: testutil.NewMockWarehouseProvider("public"),
		failFor:               map[string]error{"public": refused},
	}

	schemas, err := newSchemaDiscoveryOver(t, wh, "public").DiscoverSchemas(context.Background())
	if err == nil {
		t.Fatalf("every dataset failed and DiscoverSchemas reported success with %d tables; "+
			"the caller can then only guess at datasets and permissions", len(schemas))
	}
	if !strings.Contains(err.Error(), "SSL is not enabled") {
		t.Errorf("the error does not carry the provider's own reason: %v", err)
	}
	if !strings.Contains(err.Error(), "public") {
		t.Errorf("the error does not say which dataset failed: %v", err)
	}
	if schemas != nil {
		t.Errorf("returned %d schemas alongside an error", len(schemas))
	}
}

// TestDiscoverSchemas_OneFailedDatasetDoesNotLoseTheOthers — the skip is
// deliberate when something else succeeds, and that behaviour is the reason
// the failures are collected rather than returned on the spot.
func TestDiscoverSchemas_OneFailedDatasetDoesNotLoseTheOthers(t *testing.T) {
	wh := &listTablesFailingMock{
		MockWarehouseProvider: testutil.NewMockWarehouseProvider("public"),
		failFor:               map[string]error{"locked": errors.New("permission denied for schema locked")},
	}

	schemas, err := newSchemaDiscoveryOver(t, wh, "locked", "public").DiscoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("a partial failure must still index what worked: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("the healthy dataset contributed no tables")
	}
	for name := range schemas {
		if strings.HasPrefix(name, "locked.") {
			t.Errorf("table %q came from the dataset that failed", name)
		}
	}
}

// TestDiscoverSchemas_TheDiagnosticIsBoundedBothWays. The error is carried
// back as a single stderr line that the subprocess runner reads with a 512 KiB
// cap, and a scanner that stops mid-line leaves the agent blocked on the write
// while the indexing worker waits for it. Neither a long list of datasets nor
// one provider error carrying a whole response body may get near it.
func TestDiscoverSchemas_TheDiagnosticIsBoundedBothWays(t *testing.T) {
	t.Run("by count, with the total still reported", func(t *testing.T) {
		var kept []string
		for i := 0; i < maxReportedDatasetFailures; i++ {
			kept = append(kept, fmt.Sprintf("ds%d: refused", i))
		}
		got := boundedFailures(kept, 50)
		if strings.Count(got, "refused") > maxReportedDatasetFailures {
			t.Errorf("carried more than %d messages: %q", maxReportedDatasetFailures, got)
		}
		if !strings.Contains(got, "47 more") {
			t.Errorf("did not say what it left out: %q", got)
		}
	})

	t.Run("by bytes, per message as it is collected", func(t *testing.T) {
		one := clipFailure("public", errors.New(strings.Repeat("a response body ", 5000)))
		if len(one) > maxFailureDiagnosticBytes/maxReportedDatasetFailures+8 {
			t.Errorf("a single provider message kept %d bytes; one failure must not spend the whole budget", len(one))
		}
	})

	// The record this lands on is served from /schema-index/runs, which a
	// viewer can read. A warehouse SDK's error is not a sentence written for
	// us — BigQuery's appends the raw HTTP response body — so only the line
	// that is actually actionable is persisted.
	t.Run("the upstream body does not travel with the reason", func(t *testing.T) {
		got := clipFailure("public", errors.New(
			"googleapi: Error 403: Access Denied\n{\"rows\":[{\"customer_email\":\"a@b.c\"}]}"))
		if !strings.Contains(got, "Access Denied") {
			t.Errorf("dropped the actionable part: %q", got)
		}
		if strings.Contains(got, "customer_email") {
			t.Errorf("carried the upstream response body into a durable record: %q", got)
		}
	})

	// A byte slice through a multibyte rune leaves invalid UTF-8, which Mongo
	// accepts and then reports the document as nonconformant BSON.
	t.Run("a cut never splits a character", func(t *testing.T) {
		got := clipRunes(strings.Repeat("é", 400), 51)
		if !utf8.ValidString(got) {
			t.Errorf("clip produced invalid UTF-8: %q", got)
		}
	})

	t.Run("by bytes, in total", func(t *testing.T) {
		huge := []string{"public: " + strings.Repeat("a response body ", 5000)}
		got := boundedFailures(huge, 1)
		if len(got) > maxFailureDiagnosticBytes+64 {
			t.Errorf("diagnostic is %d bytes, past the %d-byte bound", len(got), maxFailureDiagnosticBytes)
		}
		if !strings.Contains(got, "truncated") {
			t.Errorf("truncated silently: %q", got[len(got)-40:])
		}
	})
}

// TestDiscoverSchemas_AMixedFailureSaysHowManyFailed. One dataset cannot be
// listed and another lists fine but holds nothing: the run still ends with no
// schemas, and the honest report is one of two rather than "every". An
// operator told every dataset failed goes looking for a problem with the
// healthy one as well.
func TestDiscoverSchemas_AMixedFailureSaysHowManyFailed(t *testing.T) {
	mock := testutil.NewMockWarehouseProvider("public")
	mock.Tables = nil // the dataset that lists fine and is simply empty
	wh := &listTablesFailingMock{
		MockWarehouseProvider: mock,
		failFor:               map[string]error{"locked": errors.New("permission denied for schema locked")},
	}

	schemas, err := newSchemaDiscoveryOver(t, wh, "locked", "public").DiscoverSchemas(context.Background())
	if err == nil {
		t.Fatalf("a dataset that could not be listed must still be reported; got %d schemas", len(schemas))
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("the error does not say how many datasets failed: %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the error dropped the provider's reason: %v", err)
	}
}

// TestDiscoverSchemas_AnEmptyWarehouseIsNotAFailure — a warehouse that really
// holds no tables returns no error, so "no tables discovered — check datasets
// and warehouse permissions" stays the right message for the one case it
// describes.
func TestDiscoverSchemas_AnEmptyWarehouseIsNotAFailure(t *testing.T) {
	mock := testutil.NewMockWarehouseProvider("public")
	mock.Tables = nil

	schemas, err := newSchemaDiscoveryOver(t, mock, "public").DiscoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("an empty warehouse is not an error: %v", err)
	}
	if len(schemas) != 0 {
		t.Errorf("got %d schemas from a warehouse with no tables", len(schemas))
	}
}
