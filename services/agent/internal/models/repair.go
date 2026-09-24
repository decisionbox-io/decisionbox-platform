package models

// InsightRepair records what bounded repair did to one insight whose declared
// quantifier claims were refuted by the rows of the steps it cited.
//
// Derived, never authored. The JSON tag on Insight is `evidence_repair` for the
// same reason Quality's is `evidence_quality` and QuantifierVerdicts' is
// `evidence_checks`: insights are decoded from model output with the standard
// decoder, and a model that has just been told one of its claims is refuted has
// an obvious reason to declare itself repaired. Under a name no prompt mentions,
// such a key is an unknown field and is ignored.
//
// The three claim lists are the audit trail. A count alone cannot be checked
// against the document that shipped -- "1 repaired" does not say which sentence
// changed, and the whole point of this record is that a later reader can tell a
// corrected claim from a removed one.
type InsightRepair struct {
	// Rounds is how many corrective LLM calls this insight cost, and is the
	// passing round when Outcome is RepairRepaired. Zero with a non-empty
	// Fixed means the repair was mechanical -- a count the evaluator had
	// already computed, substituted in Go without asking the model.
	Rounds int `bson:"rounds" json:"rounds"`

	// Outcome is the worst thing that happened to any one claim:
	// RepairUnrepaired beats RepairClaimDropped beats RepairRepaired. An
	// insight with one corrected claim and one removed sentence reads as
	// RepairClaimDropped, because that is the part a reviewer needs to see.
	Outcome string `bson:"outcome" json:"outcome"`

	// Fixed names the claims that were refuted and now hold, verbatim.
	Fixed []string `bson:"fixed,omitempty" json:"fixed,omitempty"`

	// Dropped names the claims whose sentence was removed from the insight
	// after the round cap was spent without the claim coming true.
	Dropped []string `bson:"dropped,omitempty" json:"dropped,omitempty"`

	// Unrepaired names the claims that are still refuted and whose sentence
	// could not be removed -- the claim is the headline, or it is the whole
	// description, and there is no smaller unit to drop. These ship refuted
	// and visibly so; the alternative is an insight with no name.
	Unrepaired []string `bson:"unrepaired,omitempty" json:"unrepaired,omitempty"`
}

// Repair outcomes, worst last.
const (
	// RepairRepaired -- every refuted claim now holds over the same rows.
	RepairRepaired = "repaired"
	// RepairClaimDropped -- at least one refuted sentence was removed from the
	// insight rather than corrected.
	RepairClaimDropped = "claim_dropped"
	// RepairUnrepaired -- at least one refuted claim survives in the text.
	RepairUnrepaired = "unrepaired"
)
