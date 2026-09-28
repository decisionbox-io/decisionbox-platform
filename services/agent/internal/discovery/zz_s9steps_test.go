package discovery

import (
	"os"
	"testing"
)

func TestS9StepsDecode(t *testing.T) {
	for _, c := range []string{"s7", "s8"} {
		raw, err := os.ReadFile("/home/abacigil/tpch-lab/" + c + "/evidence/steps.json")
		if err != nil {
			t.Skip(err)
		}
		steps, err := decodeFrozenSteps(raw)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		var q, withRows, withCompact, withQuality, withExec, rows int
		for _, s := range steps {
			if s.Action != "query_data" {
				continue
			}
			q++
			if len(s.QueryResult) > 0 {
				withRows++
			}
			if s.CompactResult != nil {
				withCompact++
			}
			if len(s.Quality) > 0 {
				withQuality++
			}
			if s.QueryExecuted != "" {
				withExec++
			}
			rows += s.RowCount
		}
		t.Logf("%s: steps=%d query=%d with_rows=%d compact=%d quality=%d query_executed=%d total_rows=%d",
			c, len(steps), q, withRows, withCompact, withQuality, withExec, rows)
		if withRows == 0 {
			t.Errorf("%s: no step carries rows; the quantifier evaluator would have nothing to read", c)
		}
	}
}
