package verifier

import (
	"strings"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

func TestParseAction_LookupSchema(t *testing.T) {
	resp := `{"lookup_schema": ["demo.t1", "demo.t2"]}`
	a, err := ParseAction(resp, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.Kind != ActionLookupSchema {
		t.Fatalf("kind = %s, want %s", a.Kind, ActionLookupSchema)
	}
	if got := strings.Join(a.LookupSchemaRefs, ","); got != "demo.t1,demo.t2" {
		t.Errorf("refs = %q, want demo.t1,demo.t2", got)
	}
}

func TestParseAction_QueryWarehouse_String(t *testing.T) {
	resp := `{"query_warehouse": "SELECT 1"}`
	a, err := ParseAction(resp, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.Kind != ActionQueryWarehouse {
		t.Fatalf("kind = %s", a.Kind)
	}
	if a.Sql != "SELECT 1" {
		t.Errorf("sql = %q", a.Sql)
	}
}

// query_warehouse object form must be a parse error.
func TestParseAction_QueryWarehouse_ObjectFormRejected(t *testing.T) {
	resp := `{"query_warehouse": {"sql": "SELECT 1"}}`
	_, err := ParseAction(resp, nil)
	if err == nil {
		t.Fatalf("expected parse error for object-form query_warehouse")
	}
	if !strings.Contains(err.Error(), "BARE SQL") && !strings.Contains(err.Error(), "SQL string") {
		t.Errorf("error %q should mention the bare-SQL expectation", err)
	}
}

func TestParseAction_ReadStepRows(t *testing.T) {
	resp := `{"read_step_rows": {"step_id": 25, "offset": 0, "limit": 50}}`
	a, err := ParseAction(resp, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.Kind != ActionReadStepRows {
		t.Fatalf("kind = %s", a.Kind)
	}
	if a.StepRowsReq == nil || a.StepRowsReq.StepID != 25 || a.StepRowsReq.Limit != 50 {
		t.Errorf("req = %+v", a.StepRowsReq)
	}
}

func TestParseAction_SubmitVerdict(t *testing.T) {
	resp := `{"submit_verdict": {"doc_id":"x","doc_kind":"insight","mode":"verifier","claims_considered":["a"],"claim_verdicts":[{"claim_text":"a","is_headline":true,"status":"supported"}],"overall":"supported"}}`
	a, err := ParseAction(resp, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.Kind != ActionSubmitVerdict {
		t.Fatalf("kind = %s", a.Kind)
	}
	if a.Verdict == nil || a.Verdict.Overall != valmodels.StatusSupported {
		t.Errorf("verdict = %+v", a.Verdict)
	}
}

func TestParseAction_BareVerdictFallback(t *testing.T) {
	resp := `{"claims_considered":["a"],"claim_verdicts":[{"claim_text":"a","is_headline":true,"status":"supported"}],"overall":"supported"}`
	a, err := ParseAction(resp, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.Kind != ActionSubmitVerdict {
		t.Fatalf("kind = %s, want %s", a.Kind, ActionSubmitVerdict)
	}
}

// Plan v4.1 — multi-key envelope is a parse error, NOT first-key-wins.
func TestParseAction_MultiKeyIsError(t *testing.T) {
	resp := `{"query_warehouse": "SELECT 1", "lookup_schema": ["t"]}`
	_, err := ParseAction(resp, nil)
	if err == nil {
		t.Fatalf("expected parse error for multi-key envelope")
	}
	if !strings.Contains(err.Error(), "multiple action keys") {
		t.Errorf("error should mention multi-key violation; got %q", err)
	}
}

// Extra unknown top-level keys are a parse error.
func TestParseAction_ExtraTopLevelKeyIsError(t *testing.T) {
	resp := `{"thinking": "let me think...", "query_warehouse": "SELECT 1"}`
	_, err := ParseAction(resp, nil)
	if err == nil {
		t.Fatalf("expected parse error when extra top-level keys present")
	}
	if !strings.Contains(err.Error(), "extra top-level") && !strings.Contains(err.Error(), "thinking") {
		t.Errorf("error should name the offending unknown key; got %q", err)
	}
}

func TestParseAction_NotAllowedKind(t *testing.T) {
	resp := `{"query_warehouse": "SELECT 1"}`
	_, err := ParseAction(resp, []ActionKind{ActionSubmitVerdict})
	if err == nil {
		t.Fatalf("expected error when action not in allowed set")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error should mention not-allowed; got %q", err)
	}
}

func TestParseAction_NoJSON(t *testing.T) {
	if _, err := ParseAction("just some prose", nil); err == nil {
		t.Fatalf("expected error for no JSON in response")
	}
}

func TestParseAction_CodeFenceStripped(t *testing.T) {
	resp := "```json\n{\"query_warehouse\": \"SELECT 1\"}\n```"
	a, err := ParseAction(resp, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.Kind != ActionQueryWarehouse || a.Sql != "SELECT 1" {
		t.Errorf("got kind=%s sql=%q", a.Kind, a.Sql)
	}
}

// extractJSON must skip braces inside strings and respect escapes.
func TestExtractJSON_BracesInStrings(t *testing.T) {
	in := `prose {"sql": "SELECT '{}' FROM x WHERE c = \"d}\""} trailing`
	out := extractJSON(in)
	if !strings.HasPrefix(out, `{"sql"`) || !strings.HasSuffix(out, `}`) {
		t.Errorf("extractJSON returned %q", out)
	}
}
