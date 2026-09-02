package models

import "testing"

func TestNormalizedQuestionKey(t *testing.T) {
	tgt := QuestionTarget{Type: QuestionTargetInsight, ID: "abc"}
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
			ka := NormalizedQuestionKey(c.a, tgt)
			kb := NormalizedQuestionKey(c.b, tgt)
			if (ka == kb) != c.same {
				t.Fatalf("NormalizedQuestionKey(%q)=%q vs (%q)=%q: same=%v want %v", c.a, ka, c.b, kb, ka == kb, c.same)
			}
		})
	}
}

func TestNormalizedQuestionKey_TargetDistinguishes(t *testing.T) {
	q := "does code 4 mean closed"
	k1 := NormalizedQuestionKey(q, QuestionTarget{Type: QuestionTargetInsight, ID: "a"})
	k2 := NormalizedQuestionKey(q, QuestionTarget{Type: QuestionTargetInsight, ID: "b"})
	if k1 == k2 {
		t.Fatalf("same question against different targets must not collide: %q", k1)
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
