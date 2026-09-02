package models

import "testing"

func TestNormalizedQuestionKey(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"punctuation and case collide", "Is HESAP_DURUMU_ID = 4 closed?", "is hesap_durumu_id  4 closed", true},
		{"whitespace collapses", "one   two", "one two", true},
		{"different wording differs", "what is churn", "what is retention", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ka := NormalizedQuestionKey(c.a)
			kb := NormalizedQuestionKey(c.b)
			if (ka == kb) != c.same {
				t.Fatalf("NormalizedQuestionKey(%q)=%q vs (%q)=%q: same=%v want %v", c.a, ka, c.b, kb, ka == kb, c.same)
			}
		})
	}
}

// The same business question must produce the SAME key across runs even though
// the insight/recommendation it links to gets a fresh UUID each run — that is
// what lets an answered/dismissed question stay suppressed. The key is text-only.
func TestNormalizedQuestionKey_StableAcrossRunScopedTargets(t *testing.T) {
	q := "does code 4 mean closed"
	if NormalizedQuestionKey(q) != NormalizedQuestionKey(q) {
		t.Fatalf("key must be deterministic for the same text")
	}
	// (The dedup set keys on text only, so a re-linked question still matches.)
	if NormalizedQuestionKey("Does code 4 mean closed?") != NormalizedQuestionKey(q) {
		t.Fatalf("normalization must ignore punctuation/case so the same question matches across runs")
	}
}

func TestValidAnswerType(t *testing.T) {
	for _, ok := range []string{AnswerTypeBoolean, AnswerTypeSingleChoice, AnswerTypeMultiChoice, AnswerTypeFreeText} {
		if !ValidAnswerType(ok) {
			t.Errorf("ValidAnswerType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "yesno", "choice"} {
		if ValidAnswerType(bad) {
			t.Errorf("ValidAnswerType(%q) = true, want false", bad)
		}
	}
}

func TestValidQuestionTargetType(t *testing.T) {
	for _, ok := range []string{QuestionTargetInsight, QuestionTargetRecommendation, QuestionTargetTable, QuestionTargetArea} {
		if !ValidQuestionTargetType(ok) {
			t.Errorf("ValidQuestionTargetType(%q) = false, want true", ok)
		}
	}
	if ValidQuestionTargetType("column") {
		t.Errorf("ValidQuestionTargetType(column) = true, want false")
	}
}
