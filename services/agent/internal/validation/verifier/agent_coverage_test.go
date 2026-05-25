package verifier

import (
	"strings"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// helper: build a base verdict the finaliser can rewrite. Each test
// then mutates only the fields it cares about.
func baseVerdict() valmodels.StructuredVerdict {
	return valmodels.StructuredVerdict{
		ClaimsConsidered: []string{"headline claim", "sub claim"},
		ClaimVerdicts: []valmodels.ClaimVerdict{
			{
				ClaimText:  "headline claim",
				IsHeadline: true,
				Status:     valmodels.StatusSupported,
				Evidence:   valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"x": 1}},
			},
			{
				ClaimText:  "sub claim",
				IsHeadline: false,
				Status:     valmodels.StatusConfirmed,
				Evidence:   valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"y": 2}},
			},
		},
		Overall: valmodels.StatusSupported,
	}
}

func runFinalise(v valmodels.StructuredVerdict) valmodels.StructuredVerdict {
	a := &Agent{cfg: DefaultConfig()}
	s := &runState{
		mode:      valmodels.ModeVerifier,
		bundle:    Bundle{Doc: DocDigest{ID: "d", Kind: valmodels.DocInsight}},
		startedAt: time.Now(),
	}
	return a.finalise(v, s)
}

func TestFinalise_HappyPath(t *testing.T) {
	out := runFinalise(baseVerdict())
	if out.Overall != valmodels.StatusSupported {
		t.Errorf("happy path overall = %s; want supported. reason=%q", out.Overall, out.OverallReason)
	}
}

func TestFinalise_EmptyClaimsConsidered(t *testing.T) {
	v := baseVerdict()
	v.ClaimsConsidered = nil
	v.ClaimVerdicts = nil
	out := runFinalise(v)
	if out.Overall != valmodels.StatusUnverifiable {
		t.Errorf("got %s want unverifiable", out.Overall)
	}
	if !strings.Contains(out.OverallReason, "no claims") {
		t.Errorf("reason should mention no claims: %q", out.OverallReason)
	}
}

func TestFinalise_DuplicateClaimsConsidered(t *testing.T) {
	v := baseVerdict()
	v.ClaimsConsidered = []string{"a", "a"}
	v.ClaimVerdicts = []valmodels.ClaimVerdict{
		{ClaimText: "a", IsHeadline: true, Status: valmodels.StatusSupported, Evidence: valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"x": 1}}},
	}
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial", out.Overall)
	}
}

// Empty-string claim_text entries collapse to a single duplicate
// under hasDuplicates — the MVP observed this failure mode in
// production refuter output.
func TestFinalise_EmptyStringClaimTextDuplicates(t *testing.T) {
	v := valmodels.StructuredVerdict{
		ClaimsConsidered: []string{"h"},
		ClaimVerdicts: []valmodels.ClaimVerdict{
			{ClaimText: "", IsHeadline: true, Status: valmodels.StatusSupported, Evidence: valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"x": 1}}},
			{ClaimText: "", Status: valmodels.StatusSupported, Evidence: valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"x": 2}}},
		},
		Overall: valmodels.StatusSupported,
	}
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial", out.Overall)
	}
	if !strings.Contains(out.OverallReason, "duplicate") {
		t.Errorf("reason should mention duplicate: %q", out.OverallReason)
	}
}

func TestFinalise_HeadlineNotAtIndexZero(t *testing.T) {
	v := baseVerdict()
	// flip is_headline to the second entry while keeping
	// claims_considered[0] as the actual headline.
	v.ClaimVerdicts[0].IsHeadline = false
	v.ClaimVerdicts[1].IsHeadline = true
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial", out.Overall)
	}
}

func TestFinalise_MultipleHeadlineFlags(t *testing.T) {
	v := baseVerdict()
	v.ClaimVerdicts[1].IsHeadline = true // both flagged
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial; reason=%q", out.Overall, out.OverallReason)
	}
}

func TestFinalise_SetEqualityMismatch(t *testing.T) {
	v := baseVerdict()
	// missing one claim_verdict for a declared claim
	v.ClaimVerdicts = v.ClaimVerdicts[:1]
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial", out.Overall)
	}
}

func TestFinalise_MissingEvidenceKind(t *testing.T) {
	v := baseVerdict()
	v.ClaimVerdicts[1].Evidence.Kind = ""
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial; reason=%q", out.Overall, out.OverallReason)
	}
}

func TestFinalise_MissingEvidenceRow(t *testing.T) {
	v := baseVerdict()
	v.ClaimVerdicts[1].Evidence.Row = nil
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial; reason=%q", out.Overall, out.OverallReason)
	}
}

// Step 4.5 — unknown status enum.
func TestFinalise_UnknownStatusEnum(t *testing.T) {
	v := baseVerdict()
	v.ClaimVerdicts[1].Status = "spported" // typo
	v.ClaimVerdicts[1].Evidence = valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"x": 1}}
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s want partial; reason=%q", out.Overall, out.OverallReason)
	}
	if !strings.Contains(out.OverallReason, "invalid status") && !strings.Contains(out.OverallReason, "unknown status") {
		t.Errorf("reason should mention invalid/unknown status: %q", out.OverallReason)
	}
}

// Per-claim status MUST exclude the doc-level trip-wires. A claim
// emitted with status="validation_disabled" would silently pass the
// evidence-required check (which only fires for
// supported/confirmed/rejected) — finaliser step 4.5 now rejects it
// explicitly.
func TestFinalise_DocLevelStatusOnClaimIsRejected(t *testing.T) {
	for _, s := range []valmodels.Status{valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap} {
		t.Run(string(s), func(t *testing.T) {
			v := baseVerdict()
			v.ClaimVerdicts[1].Status = s
			v.ClaimVerdicts[1].Evidence = valmodels.ClaimEvidence{Kind: "step_row", Row: map[string]any{"x": 1}}
			out := runFinalise(v)
			if out.Overall != valmodels.StatusPartial {
				t.Errorf("doc-level status %q on per-claim should downgrade to partial; got %s", s, out.Overall)
			}
			if !strings.Contains(out.OverallReason, "invalid status") {
				t.Errorf("reason should mention invalid status: %q", out.OverallReason)
			}
		})
	}
}

func TestFinalise_AllClaimsUnverifiable(t *testing.T) {
	v := baseVerdict()
	v.ClaimVerdicts[0].Status = valmodels.StatusUnverifiable
	v.ClaimVerdicts[0].Evidence = valmodels.ClaimEvidence{Kind: "none"}
	v.ClaimVerdicts[1].Status = valmodels.StatusUnverifiable
	v.ClaimVerdicts[1].Evidence = valmodels.ClaimEvidence{Kind: "none"}
	out := runFinalise(v)
	if out.Overall != valmodels.StatusUnverifiable {
		t.Errorf("got %s want unverifiable", out.Overall)
	}
}

func TestFinalise_HeadlineUnverifiable(t *testing.T) {
	v := baseVerdict()
	v.ClaimVerdicts[0].Status = valmodels.StatusUnverifiable
	v.ClaimVerdicts[0].Evidence = valmodels.ClaimEvidence{Kind: "none"}
	out := runFinalise(v)
	if out.Overall != valmodels.StatusUnverifiable {
		t.Errorf("got %s want unverifiable", out.Overall)
	}
}

// Step 6 — model omits Overall; finaliser derives it from per-claim
// verdicts.
func TestFinalise_DeriveOverallWhenOmitted(t *testing.T) {
	v := baseVerdict()
	v.Overall = "" // model didn't set it
	out := runFinalise(v)
	if out.Overall != valmodels.StatusSupported {
		t.Errorf("derived overall = %s want supported", out.Overall)
	}
	if !strings.Contains(out.OverallReason, "derived") {
		t.Errorf("reason should mention derivation: %q", out.OverallReason)
	}
}

// When the LLM emits a known-bad top-level Overall (typo like
// "supportd") the finaliser used to persist it; Combine() then
// treated the verifier as Unknown and the doc collapsed to
// Unverifiable, getting filtered out of recommendations even though
// every per-claim verdict was supported. The finaliser now treats
// unknown Overall the same as missing Overall — derive from
// per-claim verdicts.
func TestFinalise_OverridesUnknownOverallByDeriving(t *testing.T) {
	v := baseVerdict()
	v.Overall = valmodels.Status("supportd") // typo
	out := runFinalise(v)
	if out.Overall != valmodels.StatusSupported {
		t.Errorf("invalid Overall not corrected: got %q, want supported", out.Overall)
	}
	if !strings.Contains(out.OverallReason, "supportd") {
		t.Errorf("reason should mention the invalid value the model returned: %q", out.OverallReason)
	}
	if !out.Overall.IsKnown() {
		t.Errorf("post-finalise Overall is still unknown: %q", out.Overall)
	}
}

// `partial` belongs in the evidence-required branch (step 4).
// Without that, a per-claim `partial` with no attached row launders
// through the coverage rules and the doc keeps its
// supported/confirmed overall.
func TestFinalise_PartialClaimWithoutEvidenceIsDowngraded(t *testing.T) {
	v := baseVerdict()
	// Replace the second claim (sub claim) with a partial verdict
	// that has no row attached.
	v.ClaimVerdicts[1].Status = valmodels.StatusPartial
	v.ClaimVerdicts[1].Evidence = valmodels.ClaimEvidence{} // empty
	out := runFinalise(v)
	if out.Overall != valmodels.StatusPartial {
		t.Errorf("got %s, want partial; reason=%q", out.Overall, out.OverallReason)
	}
	if !strings.Contains(out.OverallReason, "missing evidence") {
		t.Errorf("reason should call out the missing evidence: %q", out.OverallReason)
	}
}

// deriveOverall now folds partial verdicts in (used to silently
// skip them). One supported + one partial must resolve to partial,
// not supported.
func TestDeriveOverall_PartialDemotesSupported(t *testing.T) {
	cvs := []valmodels.ClaimVerdict{
		{Status: valmodels.StatusSupported, IsHeadline: true},
		{Status: valmodels.StatusPartial},
	}
	got := deriveOverall(cvs)
	if got != valmodels.StatusPartial {
		t.Errorf("deriveOverall = %q, want partial (one supported + one partial)", got)
	}
}

// All-partial used to return unverifiable. The fold now returns
// partial — which is a real, communicable verdict — instead of
// pretending we had no evidence.
func TestDeriveOverall_AllPartialReturnsPartial(t *testing.T) {
	cvs := []valmodels.ClaimVerdict{
		{Status: valmodels.StatusPartial, IsHeadline: true},
		{Status: valmodels.StatusPartial},
	}
	got := deriveOverall(cvs)
	if got != valmodels.StatusPartial {
		t.Errorf("deriveOverall = %q, want partial (all partial)", got)
	}
}
