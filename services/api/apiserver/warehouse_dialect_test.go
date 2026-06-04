package apiserver

import (
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// maxDialectLabelLen bounds the short ProviderMeta.Dialect label so it stays a
// clean display label rather than drifting into the verbose, prompt-oriented
// form returned by Provider.SQLDialect() (which runs 50+ chars with a
// parenthetical extension list).
const maxDialectLabelLen = 40

// expectedDialects pins the exact short label each community warehouse provider
// declares. Iterating RegisteredProvidersMeta() proves every *registered*
// provider has a label; this map additionally catches a regression in a
// specific provider's value. Keyed by provider slug (ProviderMeta.ID).
var expectedDialects = map[string]string{
	"bigquery":   "BigQuery Standard SQL",
	"databricks": "Databricks SQL",
	"mssql":      "T-SQL",
	"postgres":   "PostgreSQL",
	"redshift":   "Amazon Redshift SQL",
	"snowflake":  "Snowflake SQL",
}

// TestWarehouseProvidersDeclareShortDialect asserts that every registered
// warehouse provider exposes a non-empty, short Dialect label via the registry
// metadata (no provider instantiation required). The apiserver package
// blank-imports all community warehouse providers, so RegisteredProvidersMeta()
// returns the full set here — any new provider added to that import block is
// covered automatically.
func TestWarehouseProvidersDeclareShortDialect(t *testing.T) {
	metas := gowarehouse.RegisteredProvidersMeta()
	if len(metas) < len(expectedDialects) {
		t.Fatalf("RegisteredProvidersMeta() returned %d providers, want >= %d", len(metas), len(expectedDialects))
	}

	seen := make(map[string]bool, len(metas))
	for _, m := range metas {
		seen[m.ID] = true

		// Every registered provider must declare a non-empty label.
		if strings.TrimSpace(m.Dialect) == "" {
			t.Errorf("provider %q has empty Dialect label", m.ID)
			continue
		}
		// Short and clean: trimmed, single line, bounded length, and free of
		// the parenthetical extension list that the verbose SQLDialect() carries.
		if m.Dialect != strings.TrimSpace(m.Dialect) {
			t.Errorf("provider %q Dialect %q has leading/trailing whitespace", m.ID, m.Dialect)
		}
		if strings.ContainsAny(m.Dialect, "\n\r") {
			t.Errorf("provider %q Dialect %q spans multiple lines", m.ID, m.Dialect)
		}
		if len(m.Dialect) > maxDialectLabelLen {
			t.Errorf("provider %q Dialect %q is %d chars, want <= %d", m.ID, m.Dialect, len(m.Dialect), maxDialectLabelLen)
		}
		if strings.Contains(m.Dialect, "(") {
			t.Errorf("provider %q Dialect %q looks verbose (contains '('); it should be a short label", m.ID, m.Dialect)
		}

		// GetProviderMeta(slug) must expose the same label without instantiation.
		got, ok := gowarehouse.GetProviderMeta(m.ID)
		if !ok {
			t.Errorf("GetProviderMeta(%q) returned false", m.ID)
			continue
		}
		if got.Dialect != m.Dialect {
			t.Errorf("GetProviderMeta(%q).Dialect = %q, want %q (matching RegisteredProvidersMeta)", m.ID, got.Dialect, m.Dialect)
		}

		// Pin the exact value for the known community providers.
		if want, known := expectedDialects[m.ID]; known && m.Dialect != want {
			t.Errorf("provider %q Dialect = %q, want %q", m.ID, m.Dialect, want)
		}
	}

	for slug := range expectedDialects {
		if !seen[slug] {
			t.Errorf("expected warehouse provider %q to be registered, but it was not", slug)
		}
	}
}
