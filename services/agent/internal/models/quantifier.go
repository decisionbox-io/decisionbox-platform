package models

import gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"

// The declared-claim types live in libs/go-common/models so the agent and the API
// see one struct on the wire -- the API decodes a discovery into its own mirror of
// Insight and cannot import this internal package. Aliased rather than re-declared
// so every existing reference to models.QuantifierClaim keeps working and the two
// definitions cannot drift.
type (
	QuantifierClaim   = gomodels.QuantifierClaim
	QuantifierVerdict = gomodels.QuantifierVerdict
)
