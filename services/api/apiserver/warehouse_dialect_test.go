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
// metadata, and that its capability descriptor resolves to the right query
// language, shape and anchoring value (no provider instantiation required). The apiserver package
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
		// The length bound is the real "short" guard: every provider's verbose
		// SQLDialect() string exceeds it (the shortest, Redshift's, is 43 chars),
		// so this keeps the label from drifting into that prompt-oriented form
		// without forbidding a brief parenthetical qualifier a future short label
		// might legitimately carry (e.g. "T-SQL (Azure SQL)").
		if len(m.Dialect) > maxDialectLabelLen {
			t.Errorf("provider %q Dialect %q is %d chars, want <= %d", m.ID, m.Dialect, len(m.Dialect), maxDialectLabelLen)
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

		// The capability descriptor must resolve correctly for every provider,
		// declared or not. All three fields default, which is what lets a
		// provider — including an enterprise-only driver this repo cannot see —
		// work without declaring anything; it is equally what makes a wrong
		// value invisible. Assert the resolved values rather than the raw
		// fields, because the defaults are the contract.
		if m.Language() != m.Dialect {
			t.Errorf("provider %q Language() = %q, want its Dialect label %q", m.ID, m.Language(), m.Dialect)
		}
		if m.EffectiveShape() != gowarehouse.ShapeEntities {
			t.Errorf("provider %q EffectiveShape() = %q, want %q — every SQL warehouse is tables of rows",
				m.ID, m.EffectiveShape(), gowarehouse.ShapeEntities)
		}
		if !m.Anchors() {
			t.Errorf("provider %q Anchors() = false; a SQL warehouse is a system of record and must be able "+
				"to carry a project alone", m.ID)
		}
	}

	for slug := range expectedDialects {
		if !seen[slug] {
			t.Errorf("expected warehouse provider %q to be registered, but it was not", slug)
		}
	}
}
