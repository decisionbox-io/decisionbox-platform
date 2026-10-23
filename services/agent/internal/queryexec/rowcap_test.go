package queryexec

import (
	"context"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// limitRunner recognises a trailing LIMIT, standing in for any SQL provider
// reached through the runner seam.
type limitRunner struct{}

func (limitRunner) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	return &gowarehouse.QueryResult{}, nil
}
func (limitRunner) QueryLanguage() string       { return "SQL" }
func (limitRunner) QueryFixPrompt() string      { return "" }
func (limitRunner) RowCap(q string) (int, bool) { return gowarehouse.TrailingLimit(q) }

// blindRunner cannot recognise a cap — a non-SQL source, or a provider that
// has not implemented the interface.
type blindRunner struct{}

func (blindRunner) RunQuery(context.Context, gowarehouse.NativeQuery) (*gowarehouse.QueryResult, error) {
	return &gowarehouse.QueryResult{}, nil
}
func (blindRunner) QueryLanguage() string  { return "SQL" }
func (blindRunner) QueryFixPrompt() string { return "" }

func TestAppendRowCapCaveat(t *testing.T) {
	tests := []struct {
		name     string
		runner   gowarehouse.QueryRunner
		query    string
		rowCount int
		existing []gowarehouse.QualityCaveat
		wantLen  int
		wantKind gowarehouse.QualityKind
	}{
		{
			// The defect this exists for: the cap bound, and the result is
			// indistinguishable from a complete one.
			name:     "cap equals row count raises the caveat",
			runner:   limitRunner{},
			query:    "SELECT p_type, SUM(rev) FROM part GROUP BY p_type ORDER BY 2 DESC LIMIT 15",
			rowCount: 15,
			wantLen:  1,
			wantKind: gowarehouse.QualityTruncated,
		},
		{
			// The cap never bound, so the rows ARE the population and a
			// caveat here would teach the model to discount a sound result.
			name:     "fewer rows than the cap raises nothing",
			runner:   limitRunner{},
			query:    "SELECT a FROM t LIMIT 100",
			rowCount: 17,
			wantLen:  0,
		},
		{
			name:     "uncapped query raises nothing",
			runner:   limitRunner{},
			query:    "SELECT a FROM t GROUP BY a",
			rowCount: 40,
			wantLen:  0,
		},
		{
			name:     "a runner that cannot recognise a cap raises nothing",
			runner:   blindRunner{},
			query:    "SELECT a FROM t LIMIT 15",
			rowCount: 15,
			wantLen:  0,
		},
		{
			// A source-reported caveat and a derived one describe different
			// shortfalls; the derived one must not replace what the source said.
			name:     "a derived caveat is added beside a source-reported one",
			runner:   limitRunner{},
			query:    "SELECT a FROM t LIMIT 15",
			rowCount: 15,
			existing: []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualityWithheld, Detail: "3 rows withheld"}},
			wantLen:  2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := appendRowCapCaveat(tc.existing, tc.runner, tc.query, tc.rowCount)
			if len(got) != tc.wantLen {
				t.Fatalf("got %d caveats %v, want %d", len(got), got, tc.wantLen)
			}
			if tc.wantKind != "" && got[len(got)-1].Kind != tc.wantKind {
				t.Errorf("last caveat kind = %q, want %q", got[len(got)-1].Kind, tc.wantKind)
			}
		})
	}
}

// The provider's own slice must not be mutated: appending in place would write
// a derived caveat into a result the provider still owns.
func TestAppendRowCapCaveatDoesNotMutateInput(t *testing.T) {
	src := make([]gowarehouse.QualityCaveat, 1, 4)
	src[0] = gowarehouse.QualityCaveat{Kind: gowarehouse.QualityWithheld}
	got := appendRowCapCaveat(src, limitRunner{}, "SELECT a FROM t LIMIT 5", 5)
	if len(src) != 1 {
		t.Errorf("input slice grew to %d, want it left at 1", len(src))
	}
	if len(got) != 2 {
		t.Fatalf("got %d caveats, want 2", len(got))
	}
	if &src[0] == &got[0] {
		t.Error("returned slice shares a backing array with the provider's")
	}
}
