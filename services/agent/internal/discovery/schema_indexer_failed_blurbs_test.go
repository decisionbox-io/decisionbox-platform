package discovery

import (
	"errors"
	"reflect"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
)

// A table whose description failed (an error or an empty reply) is left out
// of the index, so table search can never return it. Each one is named with
// its reason so the operator can see which table is missing and why.
func TestFailedBlurbs_NamesEachDroppedTableWithReason(t *testing.T) {
	got := failedBlurbs([]blurb.Output{
		{Table: "ORDERS", Blurb: "Each row is an order."},
		{Table: "STOCK_MOVES", Err: errors.New("blurb(litellm): response too long")},
		{Table: "PRICES"},
	})
	want := []string{"STOCK_MOVES: blurb(litellm): response too long", "PRICES: empty description"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("failedBlurbs = %q, want %q", got, want)
	}
	if got := failedBlurbs([]blurb.Output{{Table: "ORDERS", Blurb: "ok"}}); len(got) != 0 {
		t.Errorf("no failures, got %q", got)
	}
}
