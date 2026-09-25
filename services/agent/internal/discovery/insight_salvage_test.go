package discovery

import "testing"

// quantifier_claims is optional and advisory: it buys an evidence check. The
// prompt asks the model to author it, so a slightly off shape is reachable — and
// a strict per-item decode would discard the name, body, metrics and indicators
// of an otherwise sound finding to protect a field none of them depend on.
func TestParseInsights_KeepsTheInsightWhenOnlyItsDeclarationWillNotDecode(t *testing.T) {
	o := &Orchestrator{}
	cases := map[string]string{
		"step is a string":          `"step":"4"`,
		"claims is a single object": `"quantifier_claims":{"claim":"x","kind":"only","step":4}`,
		"count is a string":         `"count":"three"`,
		"claims is a bare string":   `"quantifier_claims":"Tables is the only one"`,
	}
	for name, frag := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"insights":[{"name":"Tables drags the top ten",
				"description":"Tables runs a loss.","severity":"high","source_steps":[4],`
			if frag[0] == '"' && frag[1] == 'q' {
				body += frag + `}]}`
			} else {
				body += `"quantifier_claims":[{"claim":"x","kind":"only",` + frag + `}]}]}`
			}
			insights, dropped, err := o.parseInsights(body, "profitability")
			if err != nil {
				t.Fatalf("parseInsights() error = %v", err)
			}
			if len(insights) != 1 {
				t.Fatalf("got %d insights, want the finding kept", len(insights))
			}
			if dropped != 0 {
				t.Errorf("dropped = %d, want 0: the finding was salvaged, not discarded", dropped)
			}
			if insights[0].Name != "Tables drags the top ten" {
				t.Errorf("name = %q, want the authored name preserved", insights[0].Name)
			}
			if insights[0].Description == "" {
				t.Error("description was lost")
			}
			// The declaration is dropped, not coerced: a predicate nobody wrote,
			// evaluated over real rows, would be a verdict worse than none.
			if len(insights[0].QuantifierClaims) != 0 {
				t.Errorf("claims = %+v, want the unparseable declaration dropped", insights[0].QuantifierClaims)
			}
		})
	}
}

// An insight that is broken beyond its declaration is still dropped — the
// salvage must not turn every malformed object into a shipped finding.
func TestParseInsights_StillDropsAnInsightBrokenBeyondItsDeclaration(t *testing.T) {
	o := &Orchestrator{}
	// severity is an object where a string belongs, and there is no
	// quantifier_claims key at all, so there is nothing to strip.
	body := `{"insights":[{"name":"x","severity":{"level":"high"}}]}`
	insights, dropped, err := o.parseInsights(body, "profitability")
	if err != nil {
		t.Fatalf("parseInsights() error = %v", err)
	}
	if len(insights) != 0 || dropped != 1 {
		t.Errorf("insights=%d dropped=%d, want 0 kept and 1 dropped", len(insights), dropped)
	}
}

// A well-formed declaration must be untouched, or the salvage path would be
// silently discarding checks on every clean run.
func TestParseInsights_KeepsAWellFormedDeclaration(t *testing.T) {
	o := &Orchestrator{}
	body := `{"insights":[{"name":"x","description":"y.","severity":"high","source_steps":[4],
		"quantifier_claims":[{"claim":"c","kind":"only","step":4,"filter":"profit < 0"}]}]}`
	insights, dropped, err := o.parseInsights(body, "profitability")
	if err != nil || len(insights) != 1 || dropped != 0 {
		t.Fatalf("insights=%d dropped=%d err=%v", len(insights), dropped, err)
	}
	if len(insights[0].QuantifierClaims) != 1 {
		t.Errorf("claims = %+v, want the declaration kept", insights[0].QuantifierClaims)
	}
}
