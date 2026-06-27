package askserve

import "testing"

func TestParseTurnAction_EachMode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want actionKind
		text string
	}{
		{"query", `{"thinking":"t","query":"SELECT 1"}`, actQuery, ""},
		{"lookup", `{"lookup_schema":["ds.a","ds.b"]}`, actLookup, ""},
		{"search", `{"search_tables":"users by region"}`, actSearch, ""},
		{"answer", `{"thinking":"t","answer":"42 sessions"}`, actAnswer, "42 sessions"},
		{"clarify", `{"clarify":"which month?"}`, actClarify, "which month?"},
		{"decline", `{"decline":"no such data"}`, actDecline, "no such data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			act, err := parseTurnAction(c.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if act.Kind != c.want {
				t.Fatalf("kind = %q, want %q", act.Kind, c.want)
			}
			if c.text != "" && act.Text != c.text {
				t.Fatalf("text = %q, want %q", act.Text, c.text)
			}
		})
	}
}

func TestParseTurnAction_TerminalBeatsTool(t *testing.T) {
	// A response carrying both an answer and a stray query resolves to answer.
	act, err := parseTurnAction(`{"answer":"done","query":"SELECT 1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != actAnswer {
		t.Fatalf("kind = %q, want answer", act.Kind)
	}
}

func TestParseTurnAction_PrefersTrailingActionAfterReasoning(t *testing.T) {
	// Reasoning models emit a planning blob first, then the real action.
	resp := "Let me think.\n```json\n{\"plan\":\"first count\"}\n```\nHere is my action:\n{\"query\":\"SELECT count(*) FROM ds.t\"}"
	act, err := parseTurnAction(resp)
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != actQuery || act.Query == "" {
		t.Fatalf("got %+v, want a query action", act)
	}
}

func TestParseTurnAction_ToolUseEnvelope(t *testing.T) {
	cases := map[string]actionKind{
		`{"name":"query_data","input":{"query":"SELECT 1","purpose":"p"}}`: actQuery,
		`{"name":"lookup_schema","input":{"tables":["ds.a"]}}`:             actLookup,
		`{"name":"search_tables","input":{"query":"orders","top_k":5}}`:    actSearch,
		`{"name":"answer","input":{"text":"the answer"}}`:                  actAnswer,
		`{"name":"decline","input":{"text":"cannot"}}`:                     actDecline,
	}
	for in, want := range cases {
		act, err := parseTurnAction(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if act.Kind != want {
			t.Fatalf("%s: kind = %q, want %q", in, act.Kind, want)
		}
	}
}

func TestParseTurnAction_BracesInsideSQLString(t *testing.T) {
	// A JSON string value containing braces must not break the balanced scan.
	act, err := parseTurnAction(`{"query":"SELECT '{not json}' AS x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != actQuery {
		t.Fatalf("kind = %q, want query", act.Kind)
	}
}

func TestParseTurnAction_Unparseable(t *testing.T) {
	for _, in := range []string{"", "just prose, no json", `{"thinking":"only thinking"}`} {
		if _, err := parseTurnAction(in); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestParseTurnAction_SearchTopK(t *testing.T) {
	act, err := parseTurnAction(`{"search_tables":"x","search_top_k":7}`)
	if err != nil {
		t.Fatal(err)
	}
	if act.SearchTopK != 7 {
		t.Fatalf("top_k = %d, want 7", act.SearchTopK)
	}
}
