package askserve

import "testing"

func TestIsReadOnlyQuery(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select * from t",
		"  WITH x AS (SELECT 1) SELECT * FROM x",
		"-- a comment\nSELECT 1",
		"/* block */ SELECT 1",
		"(SELECT 1)",
		"SELECT 1;",       // trailing semicolon allowed
		"SELECT ';' AS x", // semicolon inside a string literal
		"SELECT '-- not a comment' AS x",
	}
	for _, q := range ok {
		if !isReadOnlyQuery(q) {
			t.Errorf("isReadOnlyQuery(%q) = false, want true", q)
		}
	}
	bad := []string{
		"DELETE FROM t",
		"UPDATE t SET x=1",
		"INSERT INTO t VALUES (1)",
		"DROP TABLE t",
		"TRUNCATE t",
		"MERGE INTO t ...",
		"SELECT 1; DROP TABLE t",   // smuggled second statement
		"SELECT 1; DELETE FROM t;", // smuggled second statement
		"",
		"   ",
	}
	for _, q := range bad {
		if isReadOnlyQuery(q) {
			t.Errorf("isReadOnlyQuery(%q) = true, want false", q)
		}
	}
}

func TestHasTenantPredicate(t *testing.T) {
	cases := []struct {
		sql, field, value string
		want              bool
	}{
		{"SELECT * FROM t WHERE country = 'TR'", "country", "TR", true},
		{"SELECT * FROM t WHERE country='TR'", "country", "TR", true},
		{"SELECT * FROM t WHERE COUNTRY = 'TR'", "country", "TR", true},     // case-insensitive field
		{"SELECT * FROM t WHERE tenant_id = 123", "tenant_id", "123", true}, // numeric, unquoted
		{"SELECT * FROM t WHERE country <> 'TR'", "country", "TR", false},   // negated
		{"SELECT * FROM t WHERE country IN ('TR','US')", "country", "TR", false},
		{"SELECT country FROM t", "country", "TR", false},                // bare select of the column
		{"SELECT * FROM t WHERE country = 'US'", "country", "TR", false}, // wrong value
	}
	for _, c := range cases {
		if got := hasTenantPredicate(c.sql, c.field, c.value); got != c.want {
			t.Errorf("hasTenantPredicate(%q,%q,%q) = %v, want %v", c.sql, c.field, c.value, got, c.want)
		}
	}
}

func TestValidateAskSQL(t *testing.T) {
	// Single-tenant project (no filter): any read-only query passes.
	if err := validateAskSQL("SELECT count(*) FROM t", "", ""); err != nil {
		t.Errorf("single-tenant read-only query rejected: %v", err)
	}
	// Write rejected regardless of tenant.
	if err := validateAskSQL("DELETE FROM t", "", ""); err == nil {
		t.Error("write query should be rejected")
	}
	// Multi-tenant: scoped query passes, unscoped rejected.
	if err := validateAskSQL("SELECT * FROM t WHERE country = 'TR'", "country", "TR"); err != nil {
		t.Errorf("scoped query rejected: %v", err)
	}
	if err := validateAskSQL("SELECT * FROM t", "country", "TR"); err == nil {
		t.Error("unscoped multi-tenant query should be rejected")
	}
}
