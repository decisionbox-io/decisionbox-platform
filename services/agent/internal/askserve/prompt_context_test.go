package askserve

import (
	"strings"
	"testing"
)

func TestWriteWarehouseSection_RendersCardSingleWarehouse(t *testing.T) {
	var b strings.Builder
	d := DatasourceInfo{
		Description: "sales and refunds",
		Dialect:     "bigquery",
		Datasets:    []string{"sales"},
		Card: &DatasourceCard{
			SubjectAreas: []string{"orders", "refunds"},
			KeyEntities:  []string{"customer", "order"},
			KeyMetrics:   []string{"revenue", "refund_rate"},
		},
	}
	writeWarehouseSection(&b, d)
	out := b.String()
	for _, want := range []string{
		"Holds: sales and refunds",
		"Subject areas: orders, refunds",
		"Key entities: customer, order",
		"Key metrics: revenue, refund_rate",
		"READ-ONLY",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("single-warehouse section missing %q:\n%s", want, out)
		}
	}
}

func TestWriteWarehouseSection_NoCardNoCardLines(t *testing.T) {
	var b strings.Builder
	writeWarehouseSection(&b, DatasourceInfo{Dialect: "bigquery"})
	out := b.String()
	if strings.Contains(out, "Subject areas") || strings.Contains(out, "Key entities") {
		t.Fatalf("no card should render no card lines:\n%s", out)
	}
	if !strings.Contains(out, "READ-ONLY") {
		t.Fatalf("read-only rule should still render:\n%s", out)
	}
}

func TestWriteProjectContextSection(t *testing.T) {
	var b strings.Builder
	writeProjectContextSection(&b, &ProjectRuntime{
		BusinessSummary: "Acme sells widgets to EU retailers.",
		BaseContext:     "Treat null revenue as zero.",
	})
	out := b.String()
	for _, want := range []string{"PROJECT CONTEXT", "Acme sells widgets", "Treat null revenue as zero."} {
		if !strings.Contains(out, want) {
			t.Fatalf("project context missing %q:\n%s", want, out)
		}
	}

	// Only one of the two present is fine.
	var b2 strings.Builder
	writeProjectContextSection(&b2, &ProjectRuntime{BusinessSummary: "Just a summary."})
	if !strings.Contains(b2.String(), "Just a summary.") || !strings.Contains(b2.String(), "PROJECT CONTEXT") {
		t.Fatalf("summary-only should still render a block:\n%s", b2.String())
	}

	// Neither present → no-op (no empty PROJECT CONTEXT header).
	var b3 strings.Builder
	writeProjectContextSection(&b3, &ProjectRuntime{})
	if b3.Len() != 0 {
		t.Fatalf("empty context should render nothing, got %q", b3.String())
	}

	// nil runtime → no panic, no output.
	var b4 strings.Builder
	writeProjectContextSection(&b4, nil)
	if b4.Len() != 0 {
		t.Fatalf("nil runtime should render nothing, got %q", b4.String())
	}
}
