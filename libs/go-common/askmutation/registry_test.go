package askmutation

import (
	"context"
	"testing"
)

func okExec(context.Context, Request) (Result, error) { return Result{}, nil }

func TestRegisterAndTools(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	RegisterTool(Tool{Name: "save_note", Description: "note", Run: okExec})
	RegisterTool(Tool{Name: "apply_scope", Description: "scope", Run: okExec})

	tools := Tools()
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	// Deterministic (sorted) order.
	if tools[0].Name != "apply_scope" || tools[1].Name != "save_note" {
		t.Fatalf("tools not in sorted order: %v", []string{tools[0].Name, tools[1].Name})
	}
}

func TestTools_EmptyWhenNoneRegistered(t *testing.T) {
	ResetForTest()
	if got := Tools(); len(got) != 0 {
		t.Fatalf("expected no tools, got %d", len(got))
	}
}

func TestRegisterTool_Panics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	cases := map[string]Tool{
		"empty name": {Name: "", Run: okExec},
		"nil run":    {Name: "x", Run: nil},
	}
	for name, tool := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected panic for %s", name)
				}
			}()
			RegisterTool(tool)
		})
	}

	// Duplicate registration panics.
	t.Run("duplicate", func(t *testing.T) {
		ResetForTest()
		RegisterTool(Tool{Name: "dup", Run: okExec})
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on duplicate registration")
			}
		}()
		RegisterTool(Tool{Name: "dup", Run: okExec})
	})
}

// A tool reaches its re-read through the request the host built, and a host
// that binds none leaves it nil — which a tool has to cope with rather than
// assume away, so both halves are pinned here.
func TestRequestQuery(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	var got QueryRequest
	bound := func(_ context.Context, q QueryRequest) (*QueryResult, error) {
		got = q
		return &QueryResult{
			Columns:  []string{"id"},
			Rows:     []map[string]any{{"id": 1}, {"id": 2}},
			RowCount: 4000,
		}, nil
	}

	RegisterTool(Tool{Name: "needs_rows", Run: func(ctx context.Context, req Request) (Result, error) {
		if req.Query == nil {
			return Result{Output: "no re-read available"}, nil
		}
		res, err := req.Query(ctx, QueryRequest{DatasourceID: "ds1", SQL: "SELECT id FROM orders", MaxRows: 1000})
		if err != nil {
			return Result{}, err
		}
		return Result{Output: res}, nil
	}})
	tool := Tools()[0]

	res, err := tool.Run(context.Background(), Request{ProjectID: "p1", Query: bound})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got.DatasourceID != "ds1" || got.SQL != "SELECT id FROM orders" || got.MaxRows != 1000 {
		t.Errorf("the request did not reach the bound func intact: %+v", got)
	}
	out, ok := res.Output.(*QueryResult)
	if !ok {
		t.Fatalf("output = %T, want *QueryResult", res.Output)
	}
	// RowCount is the source's count, not the capped row slice — that is what
	// lets a caller say "showing 2 of 4000".
	if len(out.Rows) != 2 || out.RowCount != 4000 {
		t.Errorf("rows=%d rowCount=%d, want 2 and 4000", len(out.Rows), out.RowCount)
	}

	// No re-read bound: the tool still runs.
	res, err = tool.Run(context.Background(), Request{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("Run with no Query error: %v", err)
	}
	if res.Output != "no re-read available" {
		t.Errorf("a tool must be able to see that no re-read was bound, got %v", res.Output)
	}
}
