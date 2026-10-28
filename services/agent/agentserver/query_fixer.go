package agentserver

import (
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// askQueryFixer builds the repair-loop fixer for one datasource on an Ask
// turn.
//
// Named rather than inlined because what it must get right is a PAIR: the
// source's own repair template and the language its instruction asks for. A
// fixer holding a source's template while still instructing the model in SQL
// contradicts itself, and the model resolves that in favour of the
// instruction — it is the later and more concrete of the two. Pulling it out
// of the runtime builder is what lets that pairing be asserted.
// The whole LAYER is taken, not just its client: the repair budget is a
// property of the sql_repair model, and passing the client alone left Window
// and OutputCap at zero — so a repair against a small or uncatalogued model
// fell back to the catalog ceiling (64K for an unknown OpenAI-compatible
// endpoint) and could exceed what that endpoint accepts. Repair is its own
// layer precisely because a cheap model does it well, and a cheap model's
// window is not the analysis model's. The discovery fixer passes both.
func askQueryFixer(layer *ai.LayerLLM, wp gowarehouse.Provider, datasets []string, wh models.WarehouseConfig) *ai.SQLFixer {
	opts := ai.SQLFixerOptions{
		Dataset: strings.Join(datasets, ", "),
		Filter:  buildFilterClause(wh.FilterField, wh.FilterValue),
	}
	if layer != nil {
		opts.Client = layer.Client
		opts.Window = layer.Window
		opts.OutputCap = layer.OutputCap
	}
	return ai.NewQueryFixerFor(wp, opts)
}
