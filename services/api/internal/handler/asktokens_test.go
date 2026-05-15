package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// --- trimMessagesByTokens -----------------------------------------

// fixedCounter returns the same per-call result, ignoring input.
// Lets unit tests precisely control whether the budget walk fits or
// drops each pair.
type fixedCounter struct {
	perCall int
}

func (f fixedCounter) Count(_ context.Context, _ string) (int, error) {
	return f.perCall, nil
}

// errorCounter returns a stubbed error on the Nth call, so tests can
// simulate a flaky upstream counter and assert the trim walk falls
// back gracefully.
type errorCounter struct {
	failOnCall int
	calls      int
	err        error
}

func (e *errorCounter) Count(_ context.Context, _ string) (int, error) {
	e.calls++
	if e.calls == e.failOnCall {
		return 0, e.err
	}
	return 1, nil
}

// charCounter approximates 1 token per character, ignoring multibyte.
// Used for tests where relative ordering matters but exact values do
// not.
type charCounter struct{}

func (charCounter) Count(_ context.Context, s string) (int, error) {
	return len(s), nil
}

func mkMsg(q, a string) commonmodels.AskSessionMessage {
	return commonmodels.AskSessionMessage{Question: q, Answer: a, CreatedAt: time.Now()}
}

func TestTrimMessagesByTokens_EmptyInputReturnsNil(t *testing.T) {
	got, err := trimMessagesByTokens(context.Background(), nil, fixedCounter{perCall: 1}, 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestTrimMessagesByTokens_ZeroBudgetReturnsNothing(t *testing.T) {
	msgs := []commonmodels.AskSessionMessage{mkMsg("q1", "a1"), mkMsg("q2", "a2")}
	got, err := trimMessagesByTokens(context.Background(), msgs, fixedCounter{perCall: 1}, 0)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d messages, want 0 (budget=0)", len(got))
	}
}

func TestTrimMessagesByTokens_NegativeBudget(t *testing.T) {
	msgs := []commonmodels.AskSessionMessage{mkMsg("q1", "a1")}
	got, err := trimMessagesByTokens(context.Background(), msgs, fixedCounter{perCall: 1}, -10)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0 (negative budget)", len(got))
	}
}

func TestTrimMessagesByTokens_AllFit_KeepsEveryPair(t *testing.T) {
	// fixedCounter returns 1 token per call, 2 calls per pair → 2 tokens/pair.
	// 5 pairs * 2 tokens = 10 ≤ budget=10 → all retained.
	msgs := []commonmodels.AskSessionMessage{
		mkMsg("q1", "a1"), mkMsg("q2", "a2"), mkMsg("q3", "a3"),
		mkMsg("q4", "a4"), mkMsg("q5", "a5"),
	}
	got, err := trimMessagesByTokens(context.Background(), msgs, fixedCounter{perCall: 1}, 10)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d flat messages, want 10 (5 pairs × 2)", len(got))
	}
	// Pairs must come out oldest-first.
	if got[0].Content != "q1" || got[1].Content != "a1" || got[8].Content != "q5" || got[9].Content != "a5" {
		t.Fatalf("ordering wrong: got %+v", flattenContents(got))
	}
}

func TestTrimMessagesByTokens_TightBudgetKeepsNewestOnly(t *testing.T) {
	// Budget for exactly 2 tokens (one pair). The walk must keep the
	// most recent pair and drop the older.
	msgs := []commonmodels.AskSessionMessage{
		mkMsg("ancient-q", "ancient-a"),
		mkMsg("recent-q", "recent-a"),
	}
	got, err := trimMessagesByTokens(context.Background(), msgs, fixedCounter{perCall: 1}, 2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (newest pair only)", len(got))
	}
	if got[0].Content != "recent-q" || got[1].Content != "recent-a" {
		t.Fatalf("kept wrong pair: %+v", flattenContents(got))
	}
}

func TestTrimMessagesByTokens_NeverProducesOrphanAssistant(t *testing.T) {
	// Use a charCounter so each pair has a real token cost. The
	// budget should never permit an odd-length output (assistant
	// without its question).
	msgs := []commonmodels.AskSessionMessage{
		mkMsg("aa", "bb"),  // q+a = 4
		mkMsg("ccc", "dd"), // q+a = 5
	}
	for _, budget := range []int{0, 1, 2, 3, 4, 5, 8, 9, 100} {
		got, err := trimMessagesByTokens(context.Background(), msgs, charCounter{}, budget)
		if err != nil {
			t.Fatalf("budget=%d errored: %v", budget, err)
		}
		if len(got)%2 != 0 {
			t.Fatalf("budget=%d produced %d messages (odd) — orphan assistant! got %+v", budget, len(got), flattenContents(got))
		}
		// If anything was kept, the first kept item must be a user role.
		if len(got) > 0 && got[0].Role != "user" {
			t.Fatalf("budget=%d: first kept role = %q, want \"user\"", budget, got[0].Role)
		}
	}
}

func TestTrimMessagesByTokens_NilCounterFallsBackToApproximate(t *testing.T) {
	// A nil counter must not panic — the trim layer is supposed to
	// substitute ApproximateCounter so callers in error paths still
	// get usable output.
	msgs := []commonmodels.AskSessionMessage{mkMsg("hi", "hello")}
	got, err := trimMessagesByTokens(context.Background(), msgs, nil, 1000)
	if err != nil {
		t.Fatalf("nil counter errored: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (nil → approx counter)", len(got))
	}
}

func TestTrimMessagesByTokens_CounterErrorReturnsPartial(t *testing.T) {
	// 4 messages, counter fails on the 3rd call (mid-pair). The trim
	// loop should return what it has so far (the 1 newest pair that
	// fit before the error) and propagate err.
	msgs := []commonmodels.AskSessionMessage{
		mkMsg("oldQ", "oldA"),
		mkMsg("newQ", "newA"),
	}
	stub := errors.New("boom")
	// First pair (newest, "newQ"/"newA"): 2 calls succeed.
	// Second pair (older, "oldQ"/"oldA"): the 3rd call errors.
	c := &errorCounter{failOnCall: 3, err: stub}
	got, err := trimMessagesByTokens(context.Background(), msgs, c, 100)
	if !errors.Is(err, stub) {
		t.Fatalf("err = %v, want %v", err, stub)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (the newer pair before the error)", len(got))
	}
	if got[0].Content != "newQ" {
		t.Fatalf("kept wrong pair: %+v", flattenContents(got))
	}
}

func TestTrimMessagesByTokens_FirstCallErrorReturnsEmpty(t *testing.T) {
	// Counter fails on the very first call — no pair fit.
	msgs := []commonmodels.AskSessionMessage{mkMsg("q", "a")}
	stub := errors.New("first-call-fail")
	c := &errorCounter{failOnCall: 1, err: stub}
	got, err := trimMessagesByTokens(context.Background(), msgs, c, 100)
	if !errors.Is(err, stub) {
		t.Fatalf("err = %v, want %v", err, stub)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}

func flattenContents(msgs []gollm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = fmt.Sprintf("%s:%s", m.Role, m.Content)
	}
	return out
}

// --- formatRAGContext ----------------------------------------------

func TestFormatRAGContext_Empty(t *testing.T) {
	if got := formatRAGContext(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := formatRAGContext([]searchResultItem{}); got != "" {
		t.Fatalf("got %q, want empty for empty slice", got)
	}
}

func TestFormatRAGContext_NumbersByPosition(t *testing.T) {
	insights := []searchResultItem{
		{Name: "n1", Description: "d1", Score: 0.9},
		{Name: "n2", Description: "d2", Score: 0.7},
	}
	got := formatRAGContext(insights)
	if !strings.Contains(got, "[1] n1: d1") {
		t.Errorf("missing [1] block: %s", got)
	}
	if !strings.Contains(got, "[2] n2: d2") {
		t.Errorf("missing [2] block: %s", got)
	}
	if !strings.Contains(got, "score: 0.90") {
		t.Errorf("missing score: %s", got)
	}
}

// --- fitRAGContext -------------------------------------------------

func TestFitRAGContext_NoShrinkWhenAllFit(t *testing.T) {
	insights := []searchResultItem{
		{Name: "a", Description: "x", Score: 0.5},
		{Name: "b", Description: "y", Score: 0.4},
	}
	ctx, tokens, kept, err := fitRAGContext(context.Background(), charCounter{}, insights, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(ctx, "[1]") || !strings.Contains(ctx, "[2]") {
		t.Fatalf("expected both items, got: %s", ctx)
	}
	if tokens <= 0 {
		t.Fatalf("tokens = %d, want > 0", tokens)
	}
	if kept != 2 {
		t.Fatalf("kept = %d, want 2 (no shrink)", kept)
	}
}

func TestFitRAGContext_DropsTailUntilFits(t *testing.T) {
	// 3 items. Each formatted line is ~25 chars including the
	// citation marker and score. With a tight budget the loop must
	// drop the last (worst-scoring) item first.
	insights := []searchResultItem{
		{Name: "best", Description: "first-class", Score: 0.99},
		{Name: "mid", Description: "second-class", Score: 0.50},
		{Name: "worst", Description: "third-class", Score: 0.10},
	}
	full, _, fullKept, _ := fitRAGContext(context.Background(), charCounter{}, insights, 10000)
	if fullKept != 3 {
		t.Fatalf("unbounded budget should keep all 3, got %d", fullKept)
	}
	shrunk, _, kept, err := fitRAGContext(context.Background(), charCounter{}, insights, len(full)/2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kept >= 3 {
		t.Fatalf("kept = %d at half budget; expected < 3", kept)
	}
	if strings.Contains(shrunk, "worst") {
		t.Fatalf("shrink failed to drop tail: %s", shrunk)
	}
	if !strings.Contains(shrunk, "best") {
		t.Fatalf("shrink dropped the best result: %s", shrunk)
	}
}

func TestFitRAGContext_StopsAtMinFloor(t *testing.T) {
	// Tiny budget forces shrink to the floor. The function returns
	// even though the result still exceeds available — the caller
	// is responsible for surfacing the 413.
	insights := []searchResultItem{
		{Name: "a", Description: "x", Score: 0.5},
		{Name: "b", Description: "y", Score: 0.4},
	}
	got, tokens, kept, err := fitRAGContext(context.Background(), charCounter{}, insights, 1)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(got, "[1]") {
		t.Fatalf("min-floor result missing item 1: %s", got)
	}
	if tokens == 0 {
		t.Fatalf("expected non-zero token count at floor")
	}
	if kept != askMinRAGItems {
		t.Fatalf("kept = %d, want askMinRAGItems=%d", kept, askMinRAGItems)
	}
}

func TestFitRAGContext_NilCounterFallsBackToApproximate(t *testing.T) {
	insights := []searchResultItem{{Name: "n", Description: "d", Score: 0.5}}
	got, _, kept, err := fitRAGContext(context.Background(), nil, insights, 1000)
	if err != nil {
		t.Fatalf("nil counter errored: %v", err)
	}
	if !strings.Contains(got, "[1] n: d") {
		t.Fatalf("got %q", got)
	}
	if kept != 1 {
		t.Fatalf("kept = %d, want 1", kept)
	}
}

func TestFitRAGContext_CounterErrorReturnsBestEffort(t *testing.T) {
	stub := errors.New("count-fail")
	insights := []searchResultItem{{Name: "n", Description: "d", Score: 0.5}}
	got, _, kept, err := fitRAGContext(context.Background(), &errorCounter{failOnCall: 1, err: stub}, insights, 1000)
	if !errors.Is(err, stub) {
		t.Fatalf("err = %v, want %v", err, stub)
	}
	if got == "" {
		t.Fatal("expected best-effort string output despite error")
	}
	if kept != 1 {
		t.Fatalf("kept = %d on error, want 1 (input length)", kept)
	}
}

// --- classifyLLMError ----------------------------------------------

func TestClassifyLLMError_Nil(t *testing.T) {
	status, code, _, _ := classifyLLMError(nil)
	if status != http.StatusOK || code != "" {
		t.Fatalf("nil error → status=%d code=%q", status, code)
	}
}

func TestClassifyLLMError_ContextOverflowMessages(t *testing.T) {
	for _, msg := range []string{
		"prompt is too long: 250000 tokens > 200000 context window",
		"context_length_exceeded: please reduce the length of the messages",
		"context length exceeded by 1024 tokens",
		"input too long for the model",
		"too many tokens in your request",
		"maximum context length is 200000 tokens",
	} {
		t.Run(msg[:20], func(t *testing.T) {
			status, code, _, _ := classifyLLMError(errors.New(msg))
			if status != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d, want 413", status)
			}
			if code != ErrCodeContextOverflow {
				t.Errorf("code = %q, want %q", code, ErrCodeContextOverflow)
			}
		})
	}
}

func TestClassifyLLMError_RateLimit(t *testing.T) {
	status, code, _, _ := classifyLLMError(errors.New("rate_limit_error: too many requests"))
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if code != ErrCodeLLMUpstream {
		t.Errorf("code = %q, want %q", code, ErrCodeLLMUpstream)
	}
}

func TestClassifyLLMError_Quota(t *testing.T) {
	status, _, _, _ := classifyLLMError(errors.New("you exceeded your current quota"))
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
}

func TestClassifyLLMError_ContentFilter(t *testing.T) {
	status, code, _, _ := classifyLLMError(errors.New("content_filter triggered on output"))
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if code != ErrCodeLLMUpstream {
		t.Errorf("code = %q, want %q", code, ErrCodeLLMUpstream)
	}
}

func TestClassifyLLMError_SafetyPhrase(t *testing.T) {
	// "safety" is the third upstream phrase in the content-filter
	// switch; verify it routes to 502/llm_upstream like the
	// content_filter variants.
	status, code, _, _ := classifyLLMError(errors.New("output blocked by safety classifier"))
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if code != ErrCodeLLMUpstream {
		t.Errorf("code = %q, want %q", code, ErrCodeLLMUpstream)
	}
}

func TestClassifyLLMError_Cancelled(t *testing.T) {
	status, code, _, _ := classifyLLMError(context.Canceled)
	if status != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", status)
	}
	if code != ErrCodeLLMUpstream {
		t.Errorf("code = %q, want %q", code, ErrCodeLLMUpstream)
	}
}

func TestClassifyLLMError_DeadlineExceeded(t *testing.T) {
	status, _, _, _ := classifyLLMError(context.DeadlineExceeded)
	if status != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504 (deadline)", status)
	}
}

func TestClassifyLLMError_UnknownFallsToSynthesisFailed(t *testing.T) {
	status, code, msg, details := classifyLLMError(errors.New("totally unfamiliar error"))
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if code != ErrCodeLLMSynthesisFailed {
		t.Errorf("code = %q, want %q", code, ErrCodeLLMSynthesisFailed)
	}
	if !strings.Contains(msg, "synthesis failed") {
		t.Errorf("message = %q, missing synthesis-failed phrase", msg)
	}
	if !strings.Contains(details, "totally unfamiliar") {
		t.Errorf("details should carry sanitised upstream text, got %q", details)
	}
}

func TestClassifyLLMError_SanitisesSecretsInDetails(t *testing.T) {
	// SanitizeErrorBody must redact bearer tokens / api keys leaked
	// through the upstream message.
	err := errors.New("upstream said: Bearer sk-supersecret-token: invalid")
	_, _, _, details := classifyLLMError(err)
	if strings.Contains(details, "sk-supersecret") {
		t.Fatalf("details leaked secret: %q", details)
	}
	if !strings.Contains(details, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in details, got %q", details)
	}
}

// --- resolveCounter ------------------------------------------------

// tcProvider implements gollm.TokenCounterProvider for resolveCounter tests.
type tcProvider struct {
	counter gollm.TokenCounter
	err     error
}

func (p *tcProvider) Chat(_ context.Context, _ gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return &gollm.ChatResponse{}, nil
}
func (p *tcProvider) Validate(_ context.Context) error { return nil }
func (p *tcProvider) TokenCounter(_ context.Context, _ string) (gollm.TokenCounter, error) {
	return p.counter, p.err
}

func TestResolveCounter_NilProviderFallsBackToApproximate(t *testing.T) {
	c, exact := resolveCounter(context.Background(), nil, "any-model")
	if c == nil {
		t.Fatal("nil provider yielded nil counter; expected approximate")
	}
	if exact {
		t.Fatal("nil provider should not register as exact")
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want gollm.ApproximateCounter", c)
	}
}

func TestResolveCounter_ProviderWithoutInterfaceFallsBackToApproximate(t *testing.T) {
	// mockProvider does not implement TokenCounterProvider.
	c, exact := resolveCounter(context.Background(), &chatOnlyProvider{}, "any-model")
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want gollm.ApproximateCounter", c)
	}
	if exact {
		t.Fatal("non-implementing provider should not register as exact")
	}
}

func TestResolveCounter_ProviderTokenCounterErrorFallsBackToApproximate(t *testing.T) {
	p := &tcProvider{err: errors.New("can't load encoding")}
	c, exact := resolveCounter(context.Background(), p, "")
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want gollm.ApproximateCounter on err", c)
	}
	if exact {
		t.Fatal("error should not register as exact")
	}
}

func TestResolveCounter_ProviderSuppliesExact(t *testing.T) {
	exactC := fixedCounter{perCall: 42}
	p := &tcProvider{counter: exactC}
	c, exact := resolveCounter(context.Background(), p, "")
	if !exact {
		t.Fatal("provider-supplied counter should register as exact")
	}
	if _, ok := c.(fixedCounter); !ok {
		t.Fatalf("got %T, want fixedCounter", c)
	}
}

// chatOnlyProvider implements gollm.Provider but NOT
// gollm.TokenCounterProvider — used to verify resolveCounter degrades
// gracefully when the active provider has no native counter.
type chatOnlyProvider struct{}

func (chatOnlyProvider) Chat(_ context.Context, _ gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return &gollm.ChatResponse{}, nil
}
func (chatOnlyProvider) Validate(_ context.Context) error { return nil }
