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
