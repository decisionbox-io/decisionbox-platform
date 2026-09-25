package warehouse

import "testing"

const (
	pgOpen  = `"`
	pgClose = `"`
)

func TestRequoteIdentifiers(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		wantN int
	}{
		{
			"qualified reference becomes separately quoted parts",
			"SELECT * FROM `public.orders`",
			`SELECT * FROM "public"."orders"`, 1,
		},
		{
			"bare identifier",
			"SELECT `count` FROM t",
			`SELECT "count" FROM t`, 1,
		},
		{
			"three parts",
			"SELECT * FROM `proj.ds.tbl`",
			`SELECT * FROM "proj"."ds"."tbl"`, 1,
		},
		{
			"several references in one statement",
			"SELECT * FROM `public.a` JOIN `public.b` ON 1=1",
			`SELECT * FROM "public"."a" JOIN "public"."b" ON 1=1`, 2,
		},
		{
			"nothing to do",
			`SELECT * FROM "public"."orders"`,
			`SELECT * FROM "public"."orders"`, 0,
		},

		// The whole reason this needs a scanner rather than a regex.
		{
			"a backtick inside a string literal is data",
			"SELECT * FROM t WHERE c LIKE '%`%'",
			"SELECT * FROM t WHERE c LIKE '%`%'", 0,
		},
		{
			"an escaped quote does not end the literal early",
			"SELECT 'it''s `not` an identifier' FROM t",
			"SELECT 'it''s `not` an identifier' FROM t", 0,
		},
		{
			"a backtick in a line comment is left alone",
			"SELECT 1 -- see `public.orders`\nFROM t",
			"SELECT 1 -- see `public.orders`\nFROM t", 0,
		},
		{
			"a backtick in a block comment is left alone",
			"SELECT /* `public.orders` */ 1 FROM `public.t`",
			`SELECT /* ` + "`public.orders`" + ` */ 1 FROM "public"."t"`, 1,
		},
		{
			"nested block comments",
			"SELECT /* a /* `x` */ b */ 1 FROM `t`",
			`SELECT /* a /* ` + "`x`" + ` */ b */ 1 FROM "t"`, 1,
		},
		{
			"a backtick inside an already-quoted identifier is part of the name",
			`SELECT "we` + "`" + `ird" FROM t`,
			`SELECT "we` + "`" + `ird" FROM t`, 0,
		},
		{
			"a doubled close quote does not end the identifier early",
			`SELECT "a""b` + "`" + `c" FROM ` + "`t`",
			`SELECT "a""b` + "`" + `c" FROM "t"`, 1,
		},
		{
			"a backtick in a dollar-quoted string is data",
			"SELECT $$ `public.orders` $$ FROM `t`",
			`SELECT $$ ` + "`public.orders`" + ` $$ FROM "t"`, 1,
		},
		{
			"a tagged dollar quote too",
			"SELECT $tag$ `x` $tag$ FROM `t`",
			`SELECT $tag$ ` + "`x`" + ` $tag$ FROM "t"`, 1,
		},

		// Refusals: leaving the statement alone beats half-rewriting it.
		{
			"an unterminated backtick is left exactly as found",
			"SELECT * FROM `public.orders",
			"SELECT * FROM `public.orders", 0,
		},
		{
			"an unterminated backtick does not discard an earlier rewrite",
			"SELECT * FROM `a` JOIN `b",
			`SELECT * FROM "a" JOIN ` + "`b", 1,
		},

		// A name that is not a plain qualified reference stays one identifier,
		// because there the dot is more likely part of the name.
		{
			"a name holding a space is one identifier, not split on its dot",
			"SELECT * FROM `my table.v2`",
			`SELECT * FROM "my table.v2"`, 1,
		},
		{
			"a close delimiter inside the name is escaped by doubling",
			"SELECT `we\"ird` FROM t",
			`SELECT "we""ird" FROM t`, 1,
		},
		{
			"empty backticks",
			"SELECT `` FROM t",
			`SELECT "" FROM t`, 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := RequoteIdentifiers(tc.in, pgOpen, pgClose)
			if got != tc.want {
				t.Errorf("RequoteIdentifiers()\n got  %s\n want %s", got, tc.want)
			}
			if n != tc.wantN {
				t.Errorf("rewrote %d identifiers, want %d", n, tc.wantN)
			}
		})
	}
}

// A source that quotes with backticks has nothing to convert to, and converting
// anyway would rewrite valid SQL into valid-but-pointlessly-different SQL.
func TestRequoteIdentifiers_DeclinesWhenThereIsNothingToConvertTo(t *testing.T) {
	in := "SELECT * FROM `public.orders`"
	for _, d := range []struct{ open, close string }{
		{"`", "`"}, {"", ""}, {`"`, ""}, {"", `"`},
	} {
		got, n := RequoteIdentifiers(in, d.open, d.close)
		if got != in || n != 0 {
			t.Errorf("delimiters %q/%q: rewrote to %q (n=%d), want the input unchanged", d.open, d.close, got, n)
		}
	}
}

type quoter struct{ o, c string }

func (q quoter) QuoteRef(parts ...string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "."
		}
		out += q.o + p + q.c
	}
	return out
}

type noQuoter struct{}

func (noQuoter) QuoteRef(parts ...string) string { return parts[0] }

func TestIdentifierDelimiters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		q      interface{ QuoteRef(...string) string }
		wo, wc string
		ok     bool
	}{
		{"postgres style", quoter{`"`, `"`}, `"`, `"`, true},
		{"mssql style", quoter{"[", "]"}, "[", "]", true},
		{"bigquery style", quoter{"`", "`"}, "`", "`", true},
		{"a source that does not quote at all", noQuoter{}, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, c, ok := IdentifierDelimiters(tc.q)
			if o != tc.wo || c != tc.wc || ok != tc.ok {
				t.Errorf("IdentifierDelimiters() = (%q,%q,%v), want (%q,%q,%v)", o, c, ok, tc.wo, tc.wc, tc.ok)
			}
		})
	}
}
