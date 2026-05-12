package warehouse

import "testing"

// TestQuotePartsWith covers every reasonable shape of input that
// Provider.QuoteRef implementations forward to the helper: empty,
// single-part, multi-part, mixed empty / whitespace parts, and the
// three quote-pair styles used by the shipped providers.
func TestQuotePartsWith(t *testing.T) {
	cases := []struct {
		name  string
		open  string
		close string
		parts []string
		want  string
	}{
		{
			name:  "bigquery two parts",
			open:  "`",
			close: "`",
			parts: []string{"events_prod", "sessions"},
			want:  "`events_prod`.`sessions`",
		},
		{
			name:  "bigquery three parts (catalog.schema.table)",
			open:  "`",
			close: "`",
			parts: []string{"main", "events_prod", "sessions"},
			want:  "`main`.`events_prod`.`sessions`",
		},
		{
			name:  "postgres double-quoted",
			open:  `"`,
			close: `"`,
			parts: []string{"public", "users"},
			want:  `"public"."users"`,
		},
		{
			name:  "mssql bracketed",
			open:  "[",
			close: "]",
			parts: []string{"dbo", "Customers"},
			want:  "[dbo].[Customers]",
		},
		{
			name:  "single part returns single quoted ident",
			open:  "`",
			close: "`",
			parts: []string{"sessions"},
			want:  "`sessions`",
		},
		{
			name:  "empty parts list returns empty string",
			open:  "`",
			close: "`",
			parts: []string{},
			want:  "",
		},
		{
			name:  "nil parts returns empty string",
			open:  "`",
			close: "`",
			parts: nil,
			want:  "",
		},
		{
			name:  "single empty part returns empty string",
			open:  "[",
			close: "]",
			parts: []string{""},
			want:  "",
		},
		{
			name:  "all empty / whitespace parts return empty string",
			open:  "[",
			close: "]",
			parts: []string{"", "  ", "\t"},
			want:  "",
		},
		{
			name:  "empty parts in the middle are skipped",
			open:  `"`,
			close: `"`,
			parts: []string{"public", "", "users"},
			want:  `"public"."users"`,
		},
		{
			name:  "trailing empty part is skipped",
			open:  "`",
			close: "`",
			parts: []string{"events", ""},
			want:  "`events`",
		},
		{
			name:  "whitespace-only parts are treated as empty",
			open:  "`",
			close: "`",
			parts: []string{"events", "   "},
			want:  "`events`",
		},
		{
			name:  "delimiters can differ (asymmetric MSSQL brackets)",
			open:  "[",
			close: "]",
			parts: []string{"a", "b", "c"},
			want:  "[a].[b].[c]",
		},
		{
			name:  "parts with embedded dots are NOT split — they pass through",
			open:  "`",
			close: "`",
			parts: []string{"events.prod"},
			want:  "`events.prod`",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QuotePartsWith(tc.open, tc.close, tc.parts)
			if got != tc.want {
				t.Fatalf("QuotePartsWith(%q, %q, %#v) = %q, want %q", tc.open, tc.close, tc.parts, got, tc.want)
			}
		})
	}
}
