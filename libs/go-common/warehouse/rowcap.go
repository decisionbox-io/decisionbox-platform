package warehouse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RowCapInspector is an optional interface for a source that can recognise a
// row cap — LIMIT, TOP, FETCH FIRST, ROWNUM — in a query written in its own
// language.
//
// It exists because a capped result is the one degradation the source cannot
// report. A warehouse answers `... GROUP BY x ORDER BY y DESC LIMIT 15`
// exactly as it was asked to and has nothing to declare, so
// QueryResult.Quality comes back nil — yet the rows are a top-15 view and
// read as the whole population. The cap is knowable only from the query text,
// and only the source knows its own syntax for one: T-SQL has no LIMIT at all
// and caps with TOP, Oracle with FETCH FIRST or a ROWNUM predicate.
//
// Recognition therefore belongs beside SampleQuery, on the provider that
// already renders a cap in its own dialect, rather than in a caller
// pattern-matching every dialect at once. A caller that matched only LIMIT
// would hand MSSQL and Oracle users a silent exemption from a check whose
// whole purpose is that silence is the failure mode.
type RowCapInspector interface {
	// RowCap reports the row cap this query applies, if any.
	//
	// ok is false when the query is uncapped, or when the cap is not a
	// literal the source can read (a parameter, an expression). False is the
	// safe answer: it produces no caveat, where a wrong true would caveat a
	// complete result and teach the model to discount rows that are sound.
	RowCap(query string) (n int, ok bool)
}

// A cap counts only when it governs the statement's final result. A LIMIT
// inside a subquery bounds an intermediate set, not the rows the caller sees,
// so every matcher below anchors to the position its dialect puts the
// governing cap in — trailing for LIMIT and FETCH FIRST, leading for TOP.
// Anchoring is what keeps a false positive out: an unanchored match would
// caveat `... WHERE id IN (SELECT id FROM t LIMIT 10)` on the strength of a
// bound that never reached the output.
var (
	reTrailingLimit = regexp.MustCompile(`(?is)\bLIMIT\s+(\d+)\s*(?:OFFSET\s+(\d+)\s*)?;?\s*$`)
	reLeadingTop    = regexp.MustCompile(`(?is)^\s*SELECT\s+(?:DISTINCT\s+|ALL\s+)?TOP\s*\(?\s*(\d+)\s*\)?\s*(\w+)?`)
	reTrailingFetch = regexp.MustCompile(`(?is)\bFETCH\s+(?:FIRST|NEXT)\s+(\d+)\s+ROWS?\s+ONLY\s*;?\s*$`)
	reRownum        = regexp.MustCompile(`(?is)\bROWNUM\s*(<=|<)\s*(\d+)\b`)
	reOffsetRows    = regexp.MustCompile(`(?is)\bOFFSET\s+(\d+)\s+ROWS?\s*(?:FETCH\s+(?:FIRST|NEXT)\s+\d+\s+ROWS?\s+ONLY\s*)?;?\s*$`)
)

// TrailingLimit matches the `LIMIT n [OFFSET m]` that closes a statement, the
// form Postgres, Redshift, BigQuery, Databricks, Snowflake and MySQL all use.
func TrailingLimit(query string) (int, bool) {
	m := reTrailingLimit.FindStringSubmatch(query)
	if m == nil {
		return 0, false
	}
	return atoiCap(m[1])
}

// RowOffsetInspector is the companion to RowCapInspector for the other half of a
// paginated statement.
//
// A cap is only half of what makes a result partial. `LIMIT 100 OFFSET 100`
// returning 17 rows never trips the cap check -- 17 is not 100 -- yet the rows are
// a page: a hundred were deliberately skipped, and the page reads exactly like a
// complete small result. The cap check alone therefore exempts every final page
// from the caveat whose whole purpose is that silence is the failure mode.
//
// Optional, like RowCapInspector, and for the same reason: a source that does not
// implement it yields no caveat, which is the answer it gave before this existed.
type RowOffsetInspector interface {
	// RowOffset reports the number of leading rows this query skips, if any.
	//
	// ok is false for an unpaginated query and for an offset the source cannot
	// read as a literal. False is the safe answer: no caveat, where a wrong true
	// would teach the model to discount a complete result.
	RowOffset(query string) (int, bool)
}

// TrailingOffset matches the `OFFSET m` of a trailing `LIMIT n OFFSET m`, the form
// Postgres, Redshift, BigQuery, Databricks, Snowflake and MySQL share.
//
// Read from the same match TrailingLimit already makes, rather than a second
// pattern: the offset was always being consumed by that pattern and thrown away.
func TrailingOffset(query string) (int, bool) {
	m := reTrailingLimit.FindStringSubmatch(query)
	if m == nil || m[2] == "" {
		return 0, false
	}
	return atoiCap(m[2])
}

// OffsetRows matches the `OFFSET m ROWS [FETCH NEXT n ROWS ONLY]` that CLOSES a
// T-SQL or Oracle statement.
//
// Separate from TrailingOffset because these dialects put the offset before the cap
// rather than after it, so the trailing-LIMIT pattern cannot see it.
// TrailingFetchFirst already reads the cap half.
//
// Anchored to the tail for the reason every matcher in this file is: an unanchored
// match caveats `SELECT COUNT(*) FROM (SELECT ... OFFSET 100 ROWS FETCH NEXT 100
// ROWS ONLY) s` on the strength of a bound that never reached the output -- the
// outer result is one complete aggregate row. A false caveat teaches the model to
// discount sound evidence, which is the failure this whole file exists to avoid
// causing.
func OffsetRows(query string) (int, bool) {
	m := reOffsetRows.FindStringSubmatch(query)
	if m == nil {
		return 0, false
	}
	return atoiCap(m[1])
}

// AnyRowOffset is AnyRowCap for the offset half: the first matcher that recognises
// a skipped-row count wins.
func AnyRowOffset(query string, matchers ...func(string) (int, bool)) (int, bool) {
	for _, m := range matchers {
		if n, ok := m(query); ok {
			return n, true
		}
	}
	return 0, false
}

// RowOffsetCaveat is what a paginated result carries.
//
// Worded around the rows that are missing rather than the ones present, because
// the failure it prevents is a page being described as a population -- and unlike
// a cap, an offset gives no hint of that in the rows themselves.
func RowOffsetCaveat(n int) QualityCaveat {
	return QualityCaveat{
		Kind: QualityWithheld,
		Detail: fmt.Sprintf(
			"the query skipped the first %d rows of its own ordering, so this result is a page "+
				"and not the whole population, however few rows came back: the %d rows before it are "+
				"absent and the rows after it may be too. State any count, total, share, rank or "+
				"\"only/largest/every\" claim about the rows present, never about the population",
			n, n),
	}
}

// LeadingTop matches T-SQL's `SELECT TOP n`, including the parenthesised
// `TOP (n)` form.
//
// `TOP n PERCENT` is deliberately not a cap: it bounds a proportion, so the
// row count it yields is a fact about the data rather than a ceiling the query
// imposed, and equality between it and the row count carries no signal.
func LeadingTop(query string) (int, bool) {
	m := reLeadingTop.FindStringSubmatch(query)
	if m == nil {
		return 0, false
	}
	if strings.EqualFold(m[2], "PERCENT") {
		return 0, false
	}
	return atoiCap(m[1])
}

// TrailingFetchFirst matches the ANSI `FETCH FIRST n ROWS ONLY` that closes a
// statement — Oracle 12c+, DB2, and accepted by Snowflake and Postgres.
//
// `WITH TIES` is not matched: it returns every row tied at the boundary, so
// the result may exceed n and equality with the row count means nothing.
func TrailingFetchFirst(query string) (int, bool) {
	m := reTrailingFetch.FindStringSubmatch(query)
	if m == nil {
		return 0, false
	}
	return atoiCap(m[1])
}

// RownumCap matches Oracle's pre-12c `ROWNUM <= n` predicate.
//
// Unlike the other forms this one lives in a WHERE clause rather than at a
// fixed end of the statement, so it cannot be position-anchored. `ROWNUM < n`
// caps at n-1 and is reported as such.
func RownumCap(query string) (int, bool) {
	m := reRownum.FindStringSubmatch(query)
	if m == nil {
		return 0, false
	}
	n, ok := atoiCap(m[2])
	if !ok {
		return 0, false
	}
	if m[1] == "<" {
		n--
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// AnyRowCap returns the first cap any of the given matchers recognises, so a
// provider whose dialect accepts several forms can name them in one line.
func AnyRowCap(query string, matchers ...func(string) (int, bool)) (int, bool) {
	for _, m := range matchers {
		if n, ok := m(query); ok {
			return n, true
		}
	}
	return 0, false
}

// atoiCap parses a cap literal, rejecting zero and anything that overflows an
// int. A cap of zero returns no rows, so it can never equal a non-empty row
// count and has nothing to caveat.
func atoiCap(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// RowCapCaveat states that the query's own row cap, rather than the data,
// decided how many rows came back.
//
// Only raised when the cap and the row count are equal, which is the case
// where the result is indistinguishable from a complete one: the rows are
// accurate, ordered, and stop exactly where the query told them to. A reader
// who is handed fifteen rows and a per-column "distinct: 15" has been given
// the same picture a genuinely fifteen-group population would produce.
//
// The wording names the claims that go wrong, because the observed failures
// were not readers doubting the rows — they were readers restating a top-N as
// a population count ("p_type has 15 values" over 150) or as a population
// superlative ("12 Products Each Loss-Making" over 302).
func RowCapCaveat(n int) QualityCaveat {
	return QualityCaveat{
		Kind: QualityTruncated,
		Detail: fmt.Sprintf(
			"the query capped this result at %d rows and exactly %d came back, so it is a top-%d view "+
				"and not the whole population: the true number of groups is unknown and may be far larger. "+
				"State any count, total, share, rank or \"only/largest/every\" claim about these %d rows "+
				"explicitly, never about the population",
			n, n, n, n),
	}
}
