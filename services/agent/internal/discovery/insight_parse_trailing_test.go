package discovery

// A model that answers the analysis prompt and then explains itself was being told its
// answer was malformed.
//
// The two fixtures below are verbatim first-attempt responses from
// us.anthropic.claude-opus-4-8 replaying a frozen exploration corpus, captured 2026-09-28
// because analyzeAreaInsights keeps only the last attempt and the failing shape was
// therefore never stored. Both are a valid envelope followed by the reason it is empty.
// Both were rejected with `invalid character 'T' after top-level value` -- the T of
// "The" -- and each rejection cost a re-prompt carrying a full copy of the area's
// prompt: 17 of them over 12 replays, 14.4% of the analysis input spend, for answers
// that were already correct.

import (
	"strings"
	"testing"
)

// Verbatim capture, the session_behavior area.
const trailingProseEmpty = `{"insights": []}

The provided query results are drawn from a TPC-H schema (customer, orders, lineitem, part, partsupp, supplier, nation, region) and contain no session-level, event-level, or browsing-behavior data. There are no session identifiers, page views, cart events, timestamps of browsing activity, or event types. The available queries cover regional revenue, customer revenue deciles, and negative-margin part-supplier pairs — none of which support session or browsing behavior analysis. No session and browsing patterns can be identified from this data.`

// Verbatim capture, the conversion area. Pretty-printed envelope, longer tail.
const trailingProseEmptyPretty = `{
  "insights": []
}

The dataset is a TPC-H benchmark database (orders, customer, part, partsupp, nation, region tables). It contains no funnel event data — no product view events, no add-to-cart events, no cart removal events, and no session-level tracking. Every query returns order-level and customer-level aggregates only.

Reporting funnel insights here would require fabricating numbers that do not exist in the query results, which the claim-discipline rules prohibit.

Therefore, no valid conversion funnel insights can be produced.`

func TestParseInsights_TrailingProseAfterEmptyEnvelope(t *testing.T) {
	o := &Orchestrator{}
	for name, in := range map[string]string{
		"compact": trailingProseEmpty,
		"pretty":  trailingProseEmptyPretty,
	} {
		t.Run(name, func(t *testing.T) {
			insights, dropped, err := o.parseInsights(in, "session_behavior")
			if err != nil {
				t.Fatalf("err = %v, want nil: a valid envelope followed by prose is an answer, not a malformed response", err)
			}
			if len(insights) != 0 || dropped != 0 {
				t.Fatalf("got %d insights, %d dropped; want 0, 0", len(insights), dropped)
			}
		})
	}
}

// The case that would cost findings rather than money. Not observed in the replays,
// because every area that hit the trailing-prose shape was legitimately empty -- but
// the parser cannot tell the two apart, so a full set of insights followed by a closing
// remark is discarded whole, and the re-prompt is not guaranteed to reproduce it.
func TestParseInsights_TrailingProseAfterInsights(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[
		{"name":"Returns concentrated in one region","severity":"high","affected_count":1200},
		{"name":"Discount leakage on bulk orders","severity":"medium","affected_count":340}
	]}

These two findings are the only ones the query results support. I have not included a
third on shipping delays because the relevant column was not returned.`
	insights, dropped, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 2 || dropped != 0 {
		t.Fatalf("got %d insights, %d dropped; want 2, 0", len(insights), dropped)
	}
	if insights[0].Name != "Returns concentrated in one region" {
		t.Errorf("first insight = %q", insights[0].Name)
	}
}

func TestParseInsights_TrailingProseAfterBareArray(t *testing.T) {
	o := &Orchestrator{}
	const in = `[{"name":"A","severity":"high"}]

Only one finding is supported by these results.`
	insights, dropped, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || dropped != 0 {
		t.Fatalf("got %d insights, %d dropped; want 1, 0", len(insights), dropped)
	}
}

// The other shape the model demonstrably emits: the array as a JSON-encoded string.
// The frozen conversion re-prompt came back this way, carrying a real insight that was
// then dropped -- a loss downstream of the trailing-prose rejection, since the
// re-prompt only ran because of it.
func TestParseInsights_StringifiedInsightsArray(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":"[{\"name\": \"No purchase-funnel event data\", \"severity\": \"low\", \"affected_count\": 99996}]"}`
	insights, dropped, err := o.parseInsights(in, "conversion")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || dropped != 0 {
		t.Fatalf("got %d insights, %d dropped; want 1, 0", len(insights), dropped)
	}
	if insights[0].Name != "No purchase-funnel event data" {
		t.Errorf("name = %q", insights[0].Name)
	}
	if insights[0].AffectedCount != 99996 {
		t.Errorf("affected_count = %d", insights[0].AffectedCount)
	}
}

// --- the failures that must stay failures, so the re-prompt still happens ---

func TestParseInsights_TruncatedEnvelopeStillFails(t *testing.T) {
	o := &Orchestrator{}
	// Cut off mid-insight: the response really is incomplete and a retry is right.
	const in = `{"insights":[{"name":"A","severity":"high"`
	if _, _, err := o.parseInsights(in, "revenue"); err == nil {
		t.Fatal("err = nil, want an error: a truncated response must still be retried")
	}
}

func TestParseInsights_StringifiedNonArrayStillFails(t *testing.T) {
	o := &Orchestrator{}
	// A string that is not an encoded array must not be read as an empty result.
	const in = `{"insights":"there is nothing to report for this area"}`
	_, _, err := o.parseInsights(in, "revenue")
	if err == nil {
		t.Fatal("err = nil, want an error: a non-array string must not become a silent empty result")
	}
	if !strings.Contains(err.Error(), "insights") {
		t.Errorf("err = %v, want it to name the insights key", err)
	}
}

func TestParseInsights_ProseBeforeAnyJSONStillFails(t *testing.T) {
	o := &Orchestrator{}
	// No JSON at all. cleanJSONResponse finds no brace, so the whole string is parsed
	// and must fail -- otherwise a refusal in prose would ship as an empty area.
	const in = `I cannot produce insights for this area because the data does not support it.`
	if _, _, err := o.parseInsights(in, "revenue"); err == nil {
		t.Fatal("err = nil, want an error: a prose-only response must be retried")
	}
}
