package discovery

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// The claim that shipped, with the evidence it was drawn from. Two of the ten
// largest sub-categories by sales run a loss -- Tables and Bookcases -- and the
// insight's own body named the second one.
const shippedOnlyClaim = "Tables is the only loss-making sub-category among the 10 largest by sales"

func refutedInsight() models.Insight {
	return models.Insight{
		ID:          "insight-1",
		Name:        "Furniture drags the top ten",
		Description: shippedOnlyClaim + ". Chairs leads the category on volume.",
		SourceSteps: []int{4},
		Severity:    "high",
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: shippedOnlyClaim, Kind: QuantifierOnly,
			Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales",
		}},
	}
}

func newRepairOrchestrator(responses ...string) (*Orchestrator, *testutil.MockLLMProvider) {
	provider := testutil.NewMockLLMProvider()
	for _, r := range responses {
		provider.ResponseQueue = append(provider.ResponseQueue, &gollm.ChatResponse{
			Content: r,
			Usage:   gollm.Usage{InputTokens: 7, OutputTokens: 11},
		})
	}
	client, _ := ai.New(provider, "mock-model")
	return &Orchestrator{aiClient: client}, provider
}

// repairOne runs the pass over a single insight and hands back what it became.
func repairOne(t *testing.T, o *Orchestrator, ins models.Insight) (models.Insight, repairTally) {
	t.Helper()
	insights := []models.Insight{ins}
	attachQuantifierVerdicts(insights, step4ByID())
	if countRefuted(insights[0].QuantifierVerdicts) == 0 {
		t.Fatalf("precondition: the insight must enter repair with a refuted claim")
	}
	tally := o.repairRefutedInsights(context.Background(), "profitability", insights, step4ByID(), 8000)
	return insights[0], tally
}

// --- the happy path ---

func TestRepair_CorrectsTheClaimInOneRound(t *testing.T) {
	o, provider := newRepairOrchestrator(`{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"Two of the ten largest sub-categories by sales run a loss: Tables and Bookcases. Chairs leads the category on volume.",
		"severity":"high","source_steps":[4],
		"quantifier_claims":[{"claim":"Two of the ten largest sub-categories by sales run a loss","kind":"cardinality","step":4,"filter":"profit < 0","top_n":10,"top_n_column":"sales","count":2}]
	}]}`)

	got, tally := repairOne(t, o, refutedInsight())

	if len(provider.Calls) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(provider.Calls))
	}
	if got.Repair == nil {
		t.Fatalf("no repair record")
	}
	if got.Repair.Outcome != models.RepairRepaired {
		t.Errorf("outcome = %q, want %q (record: %+v)", got.Repair.Outcome, models.RepairRepaired, got.Repair)
	}
	if got.Repair.Rounds != 1 {
		t.Errorf("rounds = %d, want 1", got.Repair.Rounds)
	}
	if len(got.Repair.Fixed) != 1 || got.Repair.Fixed[0] != shippedOnlyClaim {
		t.Errorf("fixed = %v, want the original claim verbatim", got.Repair.Fixed)
	}
	if countRefuted(got.QuantifierVerdicts) != 0 {
		t.Errorf("verdicts still refuted: %+v", got.QuantifierVerdicts)
	}
	if got.ID != "insight-1" {
		t.Errorf("id = %q; a repair must not re-key the finding", got.ID)
	}
	if tally.repaired != 1 || tally.rounds != 1 {
		t.Errorf("tally = %+v, want 1 repaired in 1 round", tally)
	}
	if tally.tokensIn != 7 || tally.tokensOut != 11 {
		t.Errorf("tally tokens = %d/%d; the repair call's cost must be recorded", tally.tokensIn, tally.tokensOut)
	}
}

// --- the loophole ---

// Keeping the sentence and deleting its declaration passes every check
// trivially, and is the first thing a model asked to make a claim check out
// would reach for. The round must be discarded, not adopted.
func TestRepair_RejectsARoundThatRemovesTheCheckInsteadOfTheError(t *testing.T) {
	stripped := `{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"` + shippedOnlyClaim + `. Chairs leads the category on volume.",
		"severity":"high","source_steps":[4]
	}]}`
	o, provider := newRepairOrchestrator(stripped, stripped)

	got, tally := repairOne(t, o, refutedInsight())

	if len(provider.Calls) != 2 {
		t.Fatalf("LLM calls = %d, want 2 (both rounds attempted)", len(provider.Calls))
	}
	if got.Repair.Outcome != models.RepairClaimDropped {
		t.Errorf("outcome = %q, want %q", got.Repair.Outcome, models.RepairClaimDropped)
	}
	if len(got.Repair.Dropped) != 1 {
		t.Errorf("dropped = %v, want the refuted claim", got.Repair.Dropped)
	}
	// The undeclared claim must not have been accepted as repaired.
	if len(got.Repair.Fixed) != 0 {
		t.Errorf("fixed = %v; an undeclared claim is not thereby true", got.Repair.Fixed)
	}
	if insightMentions(got, shippedOnlyClaim) {
		t.Errorf("the refuted sentence survived: %q", got.Description)
	}
	if got.Description != "Chairs leads the category on volume." {
		t.Errorf("description = %q, want only the surviving sentence", got.Description)
	}
	// The declaration goes with its sentence, and the stored verdicts are
	// re-settled, or downstream reads a failure about text that is gone.
	if len(got.QuantifierClaims) != 0 {
		t.Errorf("claims = %+v, want the dropped claim's declaration removed", got.QuantifierClaims)
	}
	if countRefuted(got.QuantifierVerdicts) != 0 {
		t.Errorf("verdicts = %+v, want none refuted after the sentence was removed", got.QuantifierVerdicts)
	}
	if tally.claimsDropped != 1 || tally.rounds != 2 {
		t.Errorf("tally = %+v, want 1 claim dropped after 2 rounds", tally)
	}
}

// --- the cheapest repair a model could reach for ---

// A claim made true by citing a different step is the same sentence with
// different, unchecked evidence.
func TestRepair_PinsTheCitedEvidence(t *testing.T) {
	o, _ := newRepairOrchestrator(`{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"Two of the ten largest sub-categories by sales run a loss. Chairs leads the category on volume.",
		"severity":"high","source_steps":[99,4],
		"quantifier_claims":[{"claim":"Two of the ten largest sub-categories by sales run a loss","kind":"cardinality","step":4,"filter":"profit < 0","top_n":10,"top_n_column":"sales","count":2}]
	}]}`)

	got, _ := repairOne(t, o, refutedInsight())

	if len(got.SourceSteps) != 1 || got.SourceSteps[0] != 4 {
		t.Errorf("source_steps = %v, want the original [4]", got.SourceSteps)
	}
}

// --- a rewrite that makes things worse ---

func TestRepair_DiscardsARoundThatContradictsMoreOfItsEvidence(t *testing.T) {
	worse := `{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"Bookcases is the only loss-making line in the top ten and profit rises across the range.",
		"severity":"high","source_steps":[4],
		"quantifier_claims":[
			{"claim":"Bookcases is the only loss-making line in the top ten","kind":"only","step":4,"filter":"profit < 0","top_n":10,"top_n_column":"sales"},
			{"claim":"profit rises across the range","kind":"monotonic","step":4,"column":"profit","trend":"decreasing"}
		]
	}]}`
	o, _ := newRepairOrchestrator(worse, worse)

	got, tally := repairOne(t, o, refutedInsight())

	if got.Name != "Furniture drags the top ten" {
		t.Errorf("name = %q", got.Name)
	}
	// The worse text was never adopted, so the original claim is what the drop
	// pass acts on.
	if len(got.Repair.Dropped) != 1 || got.Repair.Dropped[0] != shippedOnlyClaim {
		t.Errorf("dropped = %v, want the original claim", got.Repair.Dropped)
	}
	if got.Description != "Chairs leads the category on volume." {
		t.Errorf("description = %q; the worse rewrite must not have been adopted", got.Description)
	}
	if tally.rounds != 2 {
		t.Errorf("rounds = %d, want 2 spent and both discarded", tally.rounds)
	}
}

// --- what Go settles without asking ---

func TestRepair_SubstitutesACountWithoutAnLLMCall(t *testing.T) {
	o, provider := newRepairOrchestrator()
	ins := models.Insight{
		ID:          "insight-2",
		Name:        "Top-ten drag",
		Description: "3 of the ten largest lines by sales run a loss.",
		SourceSteps: []int{4},
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: "3 of the ten largest lines by sales run a loss", Kind: QuantifierCardinality,
			Step: 4, Filter: "profit < 0", TopN: 10, TopNColumn: "sales", Count: 3,
		}},
	}

	got, tally := repairOne(t, o, ins)

	if len(provider.Calls) != 0 {
		t.Fatalf("LLM calls = %d; a count Go computed needs no rewrite", len(provider.Calls))
	}
	if got.Repair.Outcome != models.RepairRepaired || got.Repair.Rounds != 0 {
		t.Errorf("repair = %+v, want repaired in 0 rounds", got.Repair)
	}
	if got.Description != "2 of the ten largest lines by sales run a loss." {
		t.Errorf("description = %q", got.Description)
	}
	if tally.substituted != 1 || tally.rounds != 0 {
		t.Errorf("tally = %+v, want 1 substitution and no rounds", tally)
	}
}

// --- the off switch ---

func TestRepair_ZeroRoundsStillRemovesTheSentence(t *testing.T) {
	t.Setenv(analysisRepairMaxRoundsEnv, "0")
	o, provider := newRepairOrchestrator()

	got, tally := repairOne(t, o, refutedInsight())

	if len(provider.Calls) != 0 {
		t.Fatalf("LLM calls = %d, want 0 with the rewrite disabled", len(provider.Calls))
	}
	if got.Repair.Outcome != models.RepairClaimDropped {
		t.Errorf("outcome = %q, want %q", got.Repair.Outcome, models.RepairClaimDropped)
	}
	if insightMentions(got, shippedOnlyClaim) {
		t.Errorf("the refuted sentence survived: %q", got.Description)
	}
	if tally.rounds != 0 {
		t.Errorf("rounds = %d, want 0", tally.rounds)
	}
}

// --- what cannot be removed ---

// A refuted claim that is the headline has no smaller unit to drop. It ships,
// recorded as unrepaired and with its verdict attached, rather than as an
// insight with a fragment for a name.
func TestRepair_HeadlineClaimShipsRecordedAsUnrepaired(t *testing.T) {
	t.Setenv(analysisRepairMaxRoundsEnv, "0")
	o, _ := newRepairOrchestrator()
	ins := refutedInsight()
	ins.Name = shippedOnlyClaim

	got, tally := repairOne(t, o, ins)

	if got.Repair.Outcome != models.RepairUnrepaired {
		t.Errorf("outcome = %q, want %q", got.Repair.Outcome, models.RepairUnrepaired)
	}
	if len(got.Repair.Unrepaired) != 1 {
		t.Errorf("unrepaired = %v, want the headline claim", got.Repair.Unrepaired)
	}
	if got.Name != shippedOnlyClaim {
		t.Errorf("name = %q, want it left intact", got.Name)
	}
	// The verdict has to stay attached, or the claim is silently false.
	if countRefuted(got.QuantifierVerdicts) != 1 {
		t.Errorf("verdicts = %+v, want the refutation still recorded", got.QuantifierVerdicts)
	}
	if tally.unrepaired != 1 {
		t.Errorf("tally = %+v, want 1 unrepaired", tally)
	}
}

// --- the common case ---

func TestRepair_LeavesASoundInsightUntouched(t *testing.T) {
	o, provider := newRepairOrchestrator()
	insights := []models.Insight{{
		ID: "insight-3", Name: "Chairs leads", SourceSteps: []int{4},
		Description: "Chairs is the largest sub-category by sales.",
		QuantifierClaims: []models.QuantifierClaim{{
			Claim: "Chairs is the largest sub-category by sales", Kind: QuantifierRank,
			Step: 4, Column: "sales", Subject: "sub_category = 'Chairs'", Rank: 1,
		}},
	}}
	attachQuantifierVerdicts(insights, step4ByID())
	if countRefuted(insights[0].QuantifierVerdicts) != 0 {
		t.Fatalf("precondition: this claim holds; got %+v", insights[0].QuantifierVerdicts)
	}

	tally := o.repairRefutedInsights(context.Background(), "profitability", insights, step4ByID(), 8000)

	if len(provider.Calls) != 0 {
		t.Errorf("LLM calls = %d; a sound insight must cost nothing", len(provider.Calls))
	}
	if insights[0].Repair != nil {
		t.Errorf("repair record = %+v, want nil on an untouched insight", insights[0].Repair)
	}
	if tally != (repairTally{}) {
		t.Errorf("tally = %+v, want zero", tally)
	}
}

// --- the prompt ---

// The prompt is the whole mechanism: without the evaluator's reason the model is
// told only that it is wrong, and has to re-find the counter-examples over the
// same table that misled it.
func TestRepairPrompt_CarriesTheReasonAndPinsTheEvidence(t *testing.T) {
	insights := []models.Insight{refutedInsight()}
	attachQuantifierVerdicts(insights, step4ByID())
	failed := refutedVerdicts(insights[0].QuantifierVerdicts)
	prompt := buildInsightRepairPrompt(insights[0], failed, refutedSteps(failed, step4ByID()))

	for _, want := range []string{
		shippedOnlyClaim,         // the sentence that must change
		"Bookcases",              // the row that refuted it, named
		"source_steps",           // the field a rewrite may not touch
		"quantifier_claims",      // re-declared for the new text
		"Reading `query_result`", // the digest legend
		`{"insights": [`,         // the response shape the parser expects
	} {
		if !containsFold(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	// The verdicts must not be echoed back in a shape the model could return.
	if containsFold(prompt, "evidence_checks") {
		t.Errorf("prompt shows the model its own verdict field")
	}
}
