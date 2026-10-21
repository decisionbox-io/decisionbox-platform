package discovery

import (
	"strings"
	"testing"
)

func TestClassifyResponse_StopReasonVocabulary(t *testing.T) {
	// Every wire spells the output-limit reason its own way, and the phase has
	// to read all of them: a reason it does not recognise is a truncation it
	// re-asks for identically, which is the failure this replaces.
	for _, reason := range []string{"length", "max_tokens", "MAX_TOKENS", " Length ", "tool_calls_length_unknown_no"} {
		h := classifyResponse(reason, 100, 4096, "{}")
		want := reason != "tool_calls_length_unknown_no"
		if h.truncated != want {
			t.Errorf("classifyResponse(%q).truncated = %v, want %v", reason, h.truncated, want)
		}
	}
}

func TestClassifyResponse_TruncatedReasonNamesTheStopReason(t *testing.T) {
	h := classifyResponse("length", 10, 4096, "{}")
	if !h.truncated || !strings.Contains(h.reason, "length") {
		t.Errorf("reason = %q, want it to name the stop reason", h.reason)
	}
}

func TestClassifyResponse_WholeBudgetSpentIsTruncated(t *testing.T) {
	// The provider-independent signal: a reply cannot emit more tokens than it
	// was given, so spending them all says the same thing the stop reason
	// does. This is the signal that catches a provider reporting no usable
	// stop reason at all.
	h := classifyResponse("", 8192, 8192, "{}")
	if !h.truncated {
		t.Fatal("output tokens equal to the requested cap must read as truncated")
	}
	if !strings.Contains(h.reason, "8192") {
		t.Errorf("reason = %q, want it to state the budget", h.reason)
	}
}

func TestClassifyResponse_UnderBudgetIsHealthy(t *testing.T) {
	h := classifyResponse("end_turn", 900, 4096, strings.Repeat("a", 1024))
	if h.unusable() {
		t.Errorf("a complete reply under budget must be usable, got %+v", h)
	}
	if h.reason != "" {
		t.Errorf("reason = %q, want empty for a healthy reply", h.reason)
	}
}

func TestClassifyResponse_UnknownBudgetSkipsTheTokenSignal(t *testing.T) {
	// requestedMaxTokens <= 0 means "not known". Guessing from the token count
	// alone would call every reply truncated.
	if h := classifyResponse("end_turn", 8192, 0, "{}"); h.truncated {
		t.Error("an unknown budget must not be read as a truncation")
	}
	if h := classifyResponse("end_turn", 0, 4096, "{}"); h.truncated {
		t.Error("an unreported token count must not be read as a truncation")
	}
}

func TestClassifyResponse_WhitespaceFloodIsDegenerate(t *testing.T) {
	// The other half of the observed failure: a reply that spent its whole
	// budget on a repeating newline run. Shrinking the request does not fix a
	// sampler, so it is classified apart from a reply that was simply too
	// long.
	content := `{"recommendations":[` + strings.Repeat("\n", 20000)
	h := classifyResponse("length", 8192, 8192, content)
	if !h.degenerate {
		t.Fatalf("a whitespace flood must read as degenerate, got %+v", h)
	}
	if !h.truncated {
		t.Error("it also stopped at the limit, and both are true of it")
	}
	if !strings.Contains(h.reason, "whitespace") {
		t.Errorf("reason = %q, want it to name the whitespace", h.reason)
	}
}

func TestClassifyResponse_DenseJSONIsNotDegenerate(t *testing.T) {
	// An indented JSON document is a few tens of percent whitespace and must
	// never be mistaken for a flood.
	var sb strings.Builder
	sb.WriteString("{\n  \"recommendations\": [\n")
	for i := 0; i < 40; i++ {
		sb.WriteString("    {\n      \"title\": \"Recover high-value carts within two hours\",\n      \"priority\": 1\n    },\n")
	}
	sb.WriteString("  ]\n}")
	if h := classifyResponse("end_turn", 900, 4096, sb.String()); h.degenerate {
		t.Errorf("indented JSON must not read as degenerate: %+v", h)
	}
}

func TestClassifyResponse_ShortContentIsNotJudgedForWhitespace(t *testing.T) {
	// A short reply can be mostly whitespace for innocent reasons, and there
	// is nothing to salvage either way.
	if h := classifyResponse("end_turn", 5, 4096, "\n\n\n  {}  \n\n\n"); h.degenerate {
		t.Errorf("a short reply must not be judged on its whitespace: %+v", h)
	}
}

func TestResponseMinNonSpacePct_RejectsUnusableOverrides(t *testing.T) {
	// A floor of 0 or 100 would classify nothing, or everything, as
	// degenerate; both make the signal useless, so both fall back.
	for _, v := range []string{"0", "-5", "100", "140", "not a number"} {
		t.Setenv(responseMinNonSpacePctEnv, v)
		if got := responseMinNonSpacePct(); got != defaultResponseMinNonSpacePct {
			t.Errorf("%s=%s: got %d, want the default %d", responseMinNonSpacePctEnv, v, got, defaultResponseMinNonSpacePct)
		}
	}
	t.Setenv(responseMinNonSpacePctEnv, "35")
	if got := responseMinNonSpacePct(); got != 35 {
		t.Errorf("a valid override was ignored: got %d, want 35", got)
	}
}

func TestResponseDegenerateMinRunes_FallsBackOnUnusableOverrides(t *testing.T) {
	for _, v := range []string{"0", "-1", "nonsense"} {
		t.Setenv(responseDegenerateMinRunesEnv, v)
		if got := responseDegenerateMinRunes(); got != defaultResponseDegenerateMinRunes {
			t.Errorf("%s=%s: got %d, want the default %d", responseDegenerateMinRunesEnv, v, got, defaultResponseDegenerateMinRunes)
		}
	}
	t.Setenv(responseDegenerateMinRunesEnv, "64")
	if got := responseDegenerateMinRunes(); got != 64 {
		t.Errorf("a valid override was ignored: got %d, want 64", got)
	}
}
