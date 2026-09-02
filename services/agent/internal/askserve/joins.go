package askserve

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
)

// A turn combines datasources in hops: query one, then filter another by values
// read out of that result. Whether the hop is sound turns on one thing the
// query text cannot show — that the field those values came from names the same
// entities on both sides. joins_on is where the model says which field it used,
// so the claim becomes checkable instead of assumed.
//
// The declaration is optional and stays optional. An omitted one does not fail
// the query; it fails to earn `scoped`, and the model is told the result is not
// verified as scoped to the earlier datasource's rows. That keeps the existing
// SQL multi-hop path working exactly as it did while making the difference
// between a checked hop and an unchecked one visible in the result rather than
// silent. A declaration that is self-contradictory — naming a step that does
// not exist, or a column that step never returned — IS rejected: that is a
// malformed call, not an omission, and the model is handed what it needs to fix
// it.

// joinDeclaration is the model's claim that this query filters on values it
// read out of an earlier query against a different datasource.
type joinDeclaration struct {
	// SourceStep is the q<N> id of that earlier query.
	SourceStep string
	// Field is the column IN THAT STEP'S RESULT whose values this query filters
	// on. It is the source-side name on purpose: that is the one the model
	// actually observed, so it can be checked against what the step returned,
	// and it is the one a join-key report is keyed by.
	Field string
}

// queryStep is what a completed query contributes to a later hop's check.
type queryStep struct {
	// datasource is the datasource the query ran against.
	datasource string
	// columns are the result's columns: the only names a declaration may cite,
	// because they are the only ones the model could have read values out of.
	columns []string
	// round is the loop round whose results the model saw it in.
	//
	// A join may cite a step only from an EARLIER round. A native provider can
	// return several query_data calls in one response; the loop runs them in
	// sequence, so an earlier one's result is recorded before a later one
	// executes — but the model wrote them all before seeing any of them. A
	// value from one therefore cannot have reached another's filter, and
	// treating it as though it had would certify a hop that did not happen and
	// caveat two independent queries that never touched each other.
	round int
}

// joinScope is what examining a query's cross-datasource position concluded.
type joinScope struct {
	// reject, when non-empty, is the repair message for a declaration that
	// contradicts the turn. The query MUST NOT run.
	reject string
	// scoped is stamped on the result. nil means the question does not arise —
	// no earlier query in this turn ran against another datasource — and
	// nothing is added to the summary or the observation.
	scoped *bool
	// note is the model-facing explanation, set only when scoped is false.
	note string
	// outcome is the coarse telemetry label for this decision.
	outcome string
}

// Telemetry outcome labels. They are the evidence for whether the model
// populates joins_on reliably enough to make an undeclared cross-datasource
// filter a refusal rather than a caveat.
const (
	joinOutcomeUndeclared     = "undeclared"
	joinOutcomeVerified       = "verified"
	joinOutcomeUnverified     = "unverified"
	joinOutcomeValidatorError = "validator_error"
	joinOutcomeRejectedStep   = "rejected_unknown_step"
	joinOutcomeRejectedBatch  = "rejected_same_batch"
	joinOutcomeRejectedSameDS = "rejected_same_datasource"
	joinOutcomeRejectedField  = "rejected_unobserved_field"
)

// recordCrossDatasourceQuery is a package-level var so tests can observe what
// was reported without a real telemetry sink.
var recordCrossDatasourceQuery = telemetry.TrackCrossDatasourceQuery

// resolveJoinScope decides what this query's result may claim about its scope.
//
// targetDS is the datasource the query is about to run against; decl is the
// model's declaration, or nil when it made none.
func (st *turnState) resolveJoinScope(ctx context.Context, targetDS string, decl *joinDeclaration) joinScope {
	crossed := st.hasQueriedAnotherDatasource(targetDS)

	if decl == nil {
		if !crossed {
			// Nothing observed from another datasource yet, so no hop can have
			// been made. The summary and the observation stay exactly as they
			// are on a single-datasource turn.
			return joinScope{}
		}
		return joinScope{
			scoped:  boolPtr(false),
			outcome: joinOutcomeUndeclared,
			note: "This turn has already queried another datasource, so this result is NOT verified as scoped to what you observed there. " +
				"If this query filtered on values you read out of an earlier result, declare which field they came from with joins_on " +
				"so the join key can be checked; if it did not, the result stands on its own.",
		}
	}

	step, known := st.queryStepsByID[decl.SourceStep]
	if !known {
		return joinScope{
			outcome: joinOutcomeRejectedStep,
			reject: fmt.Sprintf("joins_on names step %q, which is not a query result you have seen this turn (%s). "+
				"Use the q<N> id shown on the result you took the values from.",
				decl.SourceStep, st.describeQuerySteps()),
		}
	}
	if !st.observed(step) {
		return joinScope{
			outcome: joinOutcomeRejectedBatch,
			reject: fmt.Sprintf("joins_on names step %s, which you issued in this same step — its result did not exist when this query was written, "+
				"so no value from it can be in this filter. Run the query that gathers the values on its own, read the result, then filter on it in a later step.",
				decl.SourceStep),
		}
	}
	sourceDS := step.datasource
	if sourceDS == targetDS {
		return joinScope{
			outcome: joinOutcomeRejectedSameDS,
			reject: fmt.Sprintf("joins_on names step %s, which ran against this same datasource (%s), so this is not a cross-datasource hop. "+
				"Declare joins_on only when the values came from a DIFFERENT datasource; otherwise omit it.",
				decl.SourceStep, targetDS),
		}
	}
	if !hasColumn(step.columns, decl.Field) {
		return joinScope{
			outcome: joinOutcomeRejectedField,
			reject: fmt.Sprintf("joins_on says the values came from %q in step %s, but that result has no such column (it returned: %s). "+
				"Name the column you actually read the values from.",
				decl.Field, decl.SourceStep, strings.Join(step.columns, ", ")),
		}
	}

	verdict, err := agentplugin.ValidateJoinKey(ctx, agentplugin.JoinKeyRequest{
		ProjectID:          st.req.ProjectID,
		SourceDatasourceID: sourceDS,
		TargetDatasourceID: targetDS,
		Field:              decl.Field,
	})
	if err != nil {
		// The report being unreachable is not a reason to fail a query the user
		// asked for — but it is every reason not to claim the join was checked.
		return joinScope{
			scoped:  boolPtr(false),
			outcome: joinOutcomeValidatorError,
			note: fmt.Sprintf("The join on %s from step %s could not be checked (%s), so this result is NOT verified as scoped to that datasource's rows. "+
				"Say so if the answer leans on it.", decl.Field, decl.SourceStep, err.Error()),
		}
	}
	if verdict.Verified {
		return joinScope{scoped: boolPtr(true), outcome: joinOutcomeVerified}
	}
	return joinScope{
		scoped:  boolPtr(false),
		outcome: joinOutcomeUnverified,
		note: fmt.Sprintf("The join on %s from step %s is NOT verified — %s. These rows may not be the ones you observed there; "+
			"say so if the answer leans on it.", decl.Field, decl.SourceStep, verdict.Detail),
	}
}

// track reports the decision, once, for a query that raised the question at all.
// A query on a turn that has touched only one datasource reports nothing: it is
// the ordinary single-source path and would drown the signal this exists to
// gather.
func (js joinScope) track(declared bool) {
	if js.outcome == "" {
		return
	}
	recordCrossDatasourceQuery(declared, js.outcome)
}

// hasQueriedAnotherDatasource reports whether an earlier query in this turn
// succeeded against a datasource other than target — i.e. whether the model has
// values in hand that it could have carried across.
//
// Only successful queries count, because only they returned rows: a failed
// query against another datasource gave the model nothing to filter by.
func (st *turnState) hasQueriedAnotherDatasource(target string) bool {
	for _, step := range st.queryStepsByID {
		if step.datasource != target && st.observed(step) {
			return true
		}
	}
	return false
}

// observed reports whether the model has actually SEEN a step's result — i.e.
// the step completed in an earlier round. A step from this same round was
// issued in the same breath as the query now being checked, so its result was
// not in front of the model when that query was written.
func (st *turnState) observed(step queryStep) bool {
	return step.round < st.round
}

// describeQuerySteps lists the step ids this turn could legitimately be cited,
// or says there are none. Steps from the current round are left out: naming one
// would point the model at a step that the very next check rejects.
func (st *turnState) describeQuerySteps() string {
	steps := make([]string, 0, len(st.queryStepsByID))
	for id, step := range st.queryStepsByID {
		if st.observed(step) {
			steps = append(steps, id)
		}
	}
	if len(steps) == 0 {
		return "no query result is available to join on yet this turn"
	}
	sort.Strings(steps)
	return "results so far: " + strings.Join(steps, ", ")
}

// hasColumn reports whether cols contains name, ignoring case — column casing
// varies by warehouse, and the model reads the name off the preview. The match
// is otherwise exact: accepting a near-miss would let a field the model never
// observed pass as one it did.
func hasColumn(cols []string, name string) bool {
	for _, c := range cols {
		if strings.EqualFold(c, name) {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }
