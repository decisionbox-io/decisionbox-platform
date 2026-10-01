package models

import "testing"

// EffectiveQuery is what every evidence consumer reads, so the fallback matters
// more than the happy path: a step whose proposal ran unchanged stores nothing
// in QueryExecuted, and must still report its SQL.
func TestEffectiveQuery(t *testing.T) {
	cases := []struct {
		name string
		step ExplorationStep
		want string
	}{
		{
			"proposal ran unchanged",
			ExplorationStep{Query: "SELECT 1"},
			"SELECT 1",
		},
		{
			"repaired statement wins over the proposal",
			ExplorationStep{Query: "SELECT `a`.`b`", QueryExecuted: `SELECT "a"."b"`},
			`SELECT "a"."b"`,
		},
		{
			"no query at all",
			ExplorationStep{},
			"",
		},
		{
			"a lookup_schema step keeps reporting nothing",
			ExplorationStep{Action: "lookup_schema"},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.step.EffectiveQuery(); got != tc.want {
				t.Errorf("EffectiveQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The proposal must survive. It is the training signal -- a run where every
// statement needed repair is a fact about the model -- and overwriting it was
// the alternative design that would have destroyed it.
func TestQueryExecuted_DoesNotReplaceTheProposal(t *testing.T) {
	s := ExplorationStep{Query: "SELECT `a`", QueryExecuted: `SELECT "a"`}
	if s.Query != "SELECT `a`" {
		t.Errorf("proposal was mutated: %q", s.Query)
	}
	if s.EffectiveQuery() == s.Query {
		t.Error("EffectiveQuery() returned the proposal although a repaired statement was recorded")
	}
}
