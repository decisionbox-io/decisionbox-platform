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
func askQueryFixer(client *ai.Client, wp gowarehouse.Provider, datasets []string, wh models.WarehouseConfig) *ai.SQLFixer {
	return ai.NewQueryFixerFor(wp, ai.SQLFixerOptions{
		Client:  client,
		Dataset: strings.Join(datasets, ", "),
		Filter:  buildFilterClause(wh.FilterField, wh.FilterValue),
	})
}
