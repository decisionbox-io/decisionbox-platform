package askserve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

const (
	// routeClarifyThreshold — below this router confidence the turn asks a
	// clarifying question rather than guess a datasource.
	routeClarifyThreshold = 0.45
	// routeRetrievalTopK — candidate tables pulled from the cross-datasource
	// index to ground the routing decision.
	routeRetrievalTopK = 8
	// routeMaxOutputTokens bounds the router's reasoning output (one small JSON).
	routeMaxOutputTokens = 512
)

// routeDecision is the evidence-grounded router's structured verdict.
type routeDecision struct {
	Datasources []string `json:"datasources"`
	Reason      string   `json:"reason"`
	Confidence  float64  `json:"confidence"`
	Clarify     bool     `json:"clarify"`
	Question    string   `json:"question"`
}

// route runs the datasource router for a multi-datasource turn. It grounds the
// decision in a cross-datasource semantic search (which datasources hold tables
// relevant to the question), asks the model to pick the datasource(s) the
// question needs, and then either:
//
//   - asks a clarifying question and finalizes the turn (returns true) when the
//     question is genuinely ambiguous or the router is not confident;
//   - pins the turn to a single confident datasource (fast path); or
//   - narrows the turn to the chosen datasources and records the decision,
//     letting the answering loop chain across them.
//
// A router error is non-fatal: the turn proceeds with the model choosing
// datasources organically (the Phase 2 behaviour).
func (r *runner) route(ctx context.Context, rt *ProjectRuntime, st *turnState) (terminated bool) {
	evidence := ""
	if rt.Schema != nil {
		if hits, err := rt.Schema.SearchAll(ctx, st.req.Question, routeRetrievalTopK); err == nil {
			evidence = formatRouteEvidence(hits)
		}
	}

	dec, err := r.decideRoute(ctx, st, evidence)
	if err != nil {
		applog.WithError(err).WithField("turn_id", st.req.TurnID).Warn("ask-serve: router failed; letting the model choose datasources")
		return false
	}
	st.routeReason = strings.TrimSpace(dec.Reason)
	st.routeConfidence = dec.Confidence

	valid := filterKnownDatasources(rt, dec.Datasources)

	// Ambiguous / low-confidence / nothing chosen → clarify instead of guessing.
	if dec.Clarify || len(valid) == 0 || dec.Confidence < routeClarifyThreshold {
		q := strings.TrimSpace(dec.Question)
		if q == "" {
			q = routeDefaultClarify(st.routing.datasources)
		}
		st.routeClarify = true
		r.finalize(ctx, st, TurnFinal{
			Status:      commonmodels.AskTurnStatusDone,
			Disposition: commonmodels.AskTurnDispositionClarify,
			Answer:      q,
		})
		return true
	}

	chosen := datasourceInfosFor(rt, valid)
	if len(valid) == 1 {
		// Single-source question: pin it. Dialect + tenant scope are then
		// unambiguous and the model isn't offered a datasource choice.
		st.routing.pinned = valid[0]
		st.routing.primary = valid[0]
		st.routing.multi = false
		st.routing.datasources = chosen
		return false
	}
	// Cross-source: keep the turn multi but narrow the visible set to the routed
	// datasources; the loop chains across them.
	st.routing.datasources = chosen
	if !containsStr(valid, st.routing.primary) {
		st.routing.primary = valid[0]
	}
	return false
}

// decideRoute asks the model which datasource(s) the question needs, grounded in
// the datasource cards + the cross-datasource retrieval evidence.
func (r *runner) decideRoute(ctx context.Context, st *turnState, evidence string) (routeDecision, error) {
	system := "You are a data-source router for a multi-warehouse analytics agent. " +
		"Given a QUESTION and the available DATASOURCES, decide which datasource(s) are needed to answer it. " +
		"Prefer the FEWEST datasources that can answer it; a question may need more than one (e.g. a value from one datasource filters a query on another). " +
		"If you genuinely cannot tell which datasource the question refers to, set clarify=true and give ONE short clarifying question. " +
		"Respond with EXACTLY ONE JSON object and nothing else: " +
		`{"datasources":["<id>",...],"reason":"<one sentence>","confidence":<0.0-1.0>,"clarify":<true|false>,"question":"<clarifying question, or empty>"}. ` +
		"Use the exact datasource ids. confidence is your certainty in the datasource choice."

	var b strings.Builder
	fmt.Fprintf(&b, "QUESTION:\n%s\n\nDATASOURCES:\n", st.req.Question)
	for _, d := range st.routing.datasources {
		fmt.Fprintf(&b, "- id: %s", d.ID)
		if d.Label != "" {
			fmt.Fprintf(&b, " (%s)", d.Label)
		}
		b.WriteByte('\n')
		if d.Description != "" {
			fmt.Fprintf(&b, "  holds: %s\n", d.Description)
		}
		if d.Card != nil {
			if len(d.Card.SubjectAreas) > 0 {
				fmt.Fprintf(&b, "  subject areas: %s\n", strings.Join(d.Card.SubjectAreas, ", "))
			}
			if len(d.Card.KeyEntities) > 0 {
				fmt.Fprintf(&b, "  key entities: %s\n", strings.Join(d.Card.KeyEntities, ", "))
			}
			if len(d.Card.KeyMetrics) > 0 {
				fmt.Fprintf(&b, "  key metrics: %s\n", strings.Join(d.Card.KeyMetrics, ", "))
			}
		}
	}
	if evidence != "" {
		fmt.Fprintf(&b, "\nRELEVANT TABLES (semantic search across datasources):\n%s\n", evidence)
	}

	resp, err := st.client.CreateMessage(ctx, []gollm.Message{{Role: "user", Content: b.String()}}, system, routeMaxOutputTokens)
	if err != nil {
		return routeDecision{}, err
	}
	st.tokensIn += resp.Usage.InputTokens
	st.tokensOut += resp.Usage.OutputTokens
	return parseRouteDecision(resp.Content)
}

// parseRouteDecision extracts the router's JSON verdict from a possibly-noisy
// model response, preferring the last balanced object that carries a decision.
func parseRouteDecision(text string) (routeDecision, error) {
	candidates := balancedJSONObjects(text)
	for i := len(candidates) - 1; i >= 0; i-- {
		var d routeDecision
		if err := json.Unmarshal([]byte(candidates[i]), &d); err == nil && (len(d.Datasources) > 0 || d.Clarify) {
			return d, nil
		}
	}
	// Fall back to the whole trimmed response.
	var d routeDecision
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &d); err == nil {
		return d, nil
	}
	return routeDecision{}, fmt.Errorf("router: no JSON decision found in response")
}

// filterKnownDatasources keeps only the ids that are real datasources of the
// project, deduped and in order — the model could name an id that doesn't exist.
func filterKnownDatasources(rt *ProjectRuntime, ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := rt.datasource(id); ok {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func datasourceInfosFor(rt *ProjectRuntime, ids []string) []DatasourceInfo {
	out := make([]DatasourceInfo, 0, len(ids))
	for _, id := range ids {
		if d, ok := rt.datasource(id); ok {
			out = append(out, d)
		}
	}
	return out
}

func formatRouteEvidence(hits []TaggedHit) string {
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "- [%s] %s\n", h.DatasourceID, h.Table)
	}
	return strings.TrimRight(b.String(), "\n")
}

func routeDefaultClarify(ds []DatasourceInfo) string {
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		if d.Label != "" {
			names = append(names, d.Label)
		} else {
			names = append(names, d.ID)
		}
	}
	return "This project has multiple datasources (" + strings.Join(names, ", ") + "). Which one should I use for this question?"
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
