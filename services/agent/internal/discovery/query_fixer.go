package discovery

import (
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// newQueryFixer builds the repair-loop fixer for one datasource.
//
// Both orchestration paths — the single-warehouse run and the per-datasource
// executors of a multi-warehouse one — need the same thing: the source's own
// repair template and query language, and the run's resolved model budget.
// They assembled it separately, which is two places for the pair to drift
// apart, and the pair is exactly what must not drift: a fixer given a source's
// repair template but the SQL instruction contradicts itself.
//
// The budget is resolved here rather than passed in because it is a property
// of the SQL-repair LAYER's model, not of the datasource — every fixer in a
// run gets the same one, and it is what keeps a fix call inside a small
// model's real limit. Repair is its own layer precisely because a cheap model
// does it well, and a cheap model's window is not the analysis model's.
func (o *Orchestrator) newQueryFixer(p gowarehouse.Provider, datasets, filter string) *ai.SQLFixer {
	window, outputCap := o.budgetFor(llmlayer.LayerSQLRepair)
	return ai.NewQueryFixerFor(p, ai.SQLFixerOptions{
		Client:    o.ai(llmlayer.LayerSQLRepair),
		Dataset:   datasets,
		Filter:    filter,
		Window:    window,
		OutputCap: outputCap,
	})
}
