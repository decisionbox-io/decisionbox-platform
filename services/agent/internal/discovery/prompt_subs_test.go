package discovery

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// fakeProvider implements just the methods substituteDialectTokens
// touches: SQLDialect and QuoteRef. The rest of gowarehouse.Provider
// returns zero-values; this is sufficient because the helper does
// not call any other method.
type fakeProvider struct {
	dialect      string
	quoteOpen    string
	quoteClose   string
}

func (f *fakeProvider) Query(context.Context, string, map[string]interface{}) (*gowarehouse.QueryResult, error) {
	return nil, nil
}
func (f *fakeProvider) ListTables(context.Context) ([]string, error) { return nil, nil }
func (f *fakeProvider) ListTablesInDataset(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f *fakeProvider) GetTableSchema(context.Context, string) (*gowarehouse.TableSchema, error) {
	return nil, nil
}
func (f *fakeProvider) GetTableSchemaInDataset(context.Context, string, string) (*gowarehouse.TableSchema, error) {
	return nil, nil
}
func (f *fakeProvider) GetDataset() string             { return "" }
func (f *fakeProvider) SQLDialect() string             { return f.dialect }
func (f *fakeProvider) SQLFixPrompt() string           { return "" }
func (f *fakeProvider) ValidateReadOnly(context.Context) error { return nil }
func (f *fakeProvider) HealthCheck(context.Context) error      { return nil }
func (f *fakeProvider) Close() error                           { return nil }
func (f *fakeProvider) QuoteRef(parts ...string) string {
	return gowarehouse.QuotePartsWith(f.quoteOpen, f.quoteClose, parts)
}

func TestSubstituteDialectTokens(t *testing.T) {
	bq := &fakeProvider{dialect: "BigQuery Standard SQL", quoteOpen: "`", quoteClose: "`"}
	mssql := &fakeProvider{
		dialect:    "Microsoft SQL Server T-SQL",
		quoteOpen:  "[",
		quoteClose: "]",
	}
	pg := &fakeProvider{dialect: "PostgreSQL", quoteOpen: `"`, quoteClose: `"`}

	cases := []struct {
		name       string
		template   string
		provider   gowarehouse.Provider
		refDataset string
		want       string
	}{
		{
			name:       "dialect token substituted",
			template:   "SQL Dialect: {{DIALECT}}",
			provider:   bq,
			refDataset: "events_prod",
			want:       "SQL Dialect: BigQuery Standard SQL",
		},
		{
			name:       "single ref placeholder substituted for BQ",
			template:   "SELECT * FROM {{REF:sessions}}",
			provider:   bq,
			refDataset: "events_prod",
			want:       "SELECT * FROM `events_prod`.`sessions`",
		},
		{
			name:       "ref placeholder for MSSQL uses brackets",
			template:   "SELECT * FROM {{REF:Customers}}",
			provider:   mssql,
			refDataset: "dbo",
			want:       "SELECT * FROM [dbo].[Customers]",
		},
		{
			name:       "ref placeholder for postgres uses double quotes",
			template:   "FROM {{REF:users}} WHERE 1=1",
			provider:   pg,
			refDataset: "public",
			want:       `FROM "public"."users" WHERE 1=1`,
		},
		{
			name:       "multiple ref placeholders substituted",
			template:   "FROM {{REF:a}} JOIN {{REF:b}}",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM `ds`.`a` JOIN `ds`.`b`",
		},
		{
			name:       "both dialect and ref in same template",
			template:   "Dialect: {{DIALECT}}\nSELECT * FROM {{REF:t}}",
			provider:   mssql,
			refDataset: "dbo",
			want:       "Dialect: Microsoft SQL Server T-SQL\nSELECT * FROM [dbo].[t]",
		},
		{
			name:       "ref placeholder identifier with underscores",
			template:   "FROM {{REF:user_engagement_summary}}",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM `ds`.`user_engagement_summary`",
		},
		{
			name:       "ref placeholder identifier with digits is allowed",
			template:   "FROM {{REF:events_2026}}",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM `ds`.`events_2026`",
		},
		{
			name:       "ref placeholder starting with underscore is allowed",
			template:   "FROM {{REF:_internal}}",
			provider:   mssql,
			refDataset: "dbo",
			want:       "FROM [dbo].[_internal]",
		},
		{
			name:       "malformed ref placeholder (bare name) passes through",
			template:   "FROM {{REF:}} something",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM {{REF:}} something",
		},
		{
			name:       "matched placeholder always exposes its capture group — exercises the regex contract assumed in the impl",
			template:   "FROM {{REF:a_table_with_a_long_name_42}}",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM `ds`.`a_table_with_a_long_name_42`",
		},
		{
			name:       "ref placeholder with leading digit does not match — left intact",
			template:   "FROM {{REF:1events}}",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM {{REF:1events}}",
		},
		{
			name:       "ref placeholder with dot does not match — left intact (REF takes a single identifier, not dataset.table)",
			template:   "FROM {{REF:ds.sessions}}",
			provider:   bq,
			refDataset: "ds",
			want:       "FROM {{REF:ds.sessions}}",
		},
		{
			name:       "empty refDataset renders single-part quoted ident",
			template:   "FROM {{REF:sessions}}",
			provider:   bq,
			refDataset: "",
			want:       "FROM `sessions`",
		},
		{
			name:       "template with neither placeholder passes through unchanged",
			template:   "SELECT 1 AS literal",
			provider:   bq,
			refDataset: "ds",
			want:       "SELECT 1 AS literal",
		},
		{
			name:       "nil provider passes template through unchanged",
			template:   "SQL Dialect: {{DIALECT}} FROM {{REF:t}}",
			provider:   nil,
			refDataset: "ds",
			want:       "SQL Dialect: {{DIALECT}} FROM {{REF:t}}",
		},
		{
			name:       "repeated DIALECT and REF in same template",
			template:   "{{DIALECT}} ... {{REF:a}} ... {{DIALECT}} ... {{REF:b}}",
			provider:   pg,
			refDataset: "public",
			want:       `PostgreSQL ... "public"."a" ... PostgreSQL ... "public"."b"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := substituteDialectTokens(tc.template, tc.provider, tc.refDataset)
			if got != tc.want {
				t.Errorf("substituteDialectTokens(...) =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestSubstituteDialectTokens_DoesNotMatchMalformedDelimiters checks the
// safety property that almost-but-not-quite placeholders are left intact
// rather than mangled (e.g. someone writes `{REF:foo}` with single
// braces, or `{{REF:foo}` missing one closing brace).
func TestSubstituteDialectTokens_DoesNotMatchMalformedDelimiters(t *testing.T) {
	p := &fakeProvider{dialect: "BigQuery Standard SQL", quoteOpen: "`", quoteClose: "`"}

	malformed := []string{
		"{REF:foo}",
		"{{REF:foo}",
		"{{REF foo}}",
		"{{ REF:foo }}",
		"{{REFL:foo}}",
		"{{ref:foo}}",
	}
	for _, m := range malformed {
		got := substituteDialectTokens(m, p, "ds")
		if got != m {
			t.Errorf("malformed placeholder %q was modified to %q", m, got)
		}
	}

	// Sanity check: confirm a correctly-formed placeholder still
	// substitutes — the malformed cases above are interesting only by
	// contrast.
	got := substituteDialectTokens("{{REF:foo}}", p, "ds")
	if !strings.Contains(got, "`ds`.`foo`") {
		t.Fatalf("well-formed placeholder did not substitute: %q", got)
	}
}
