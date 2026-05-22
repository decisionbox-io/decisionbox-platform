package blurb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- fakeLLM: replaces a Provider for unit tests ---

type fakeLLM struct {
	respondWith string
	err         error
	called      int64
	// scriptedErrors[i] returns an error on the i-th call (0-indexed).
	// Use nil for "succeed"; len() capping the sequence.
	scripted []scriptedResp
	cursor   int64
}

type scriptedResp struct {
	text       string
	err        error
	stopReason string // mirrors gollm.ChatResponse.StopReason (e.g. "SAFETY", "MAX_TOKENS", "STOP")
}

func (f *fakeLLM) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	n := atomic.AddInt64(&f.called, 1)
	if f.scripted != nil {
		idx := int(atomic.AddInt64(&f.cursor, 1) - 1)
		if idx < len(f.scripted) {
			s := f.scripted[idx]
			if s.err != nil {
				return nil, s.err
			}
			return &gollm.ChatResponse{Content: s.text, StopReason: s.stopReason, Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}}, nil
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	text := f.respondWith
	if text == "" {
		text = fmt.Sprintf("blurb #%d", n)
	}
	return &gollm.ChatResponse{Content: text, Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}}, nil
}
func (f *fakeLLM) Validate(ctx context.Context) error { return nil }

// --- helpers ---

func sampleInput(name string) Input {
	return Input{
		Dataset: "analytics",
		Schema: models.TableSchema{
			TableName: name,
			RowCount:  1000,
			Columns: []models.ColumnInfo{
				{Name: "id", Type: "INT64", Category: "primary_key"},
				{Name: "created_at", Type: "TIMESTAMP", Category: "time"},
			},
		},
		DomainPackBlurb: "Generic SaaS warehouse.",
	}
}

// --- New() construction ---

func TestNew_RejectsReasoningModels(t *testing.T) {
	cases := []string{
		"deepseek.deepseek-r1-v1:0",
		"openai.o1-preview",
		"openai.o3-mini",
		"openai.o4-mini-2025",
		"claude-opus-4-6-extended-thinking",
	}
	for _, m := range cases {
		_, err := New(Config{LLM: &fakeLLM{}, Model: m})
		if !errors.Is(err, ErrReasoningModelNotSupported) {
			t.Errorf("model %q: expected ErrReasoningModelNotSupported, got %v", m, err)
		}
	}
}

func TestNew_AcceptsRegularModels(t *testing.T) {
	cases := []string{
		"qwen.qwen3-32b-v1:0",
		"gpt-4.1-nano",
		"gpt-4o",
		"anthropic.claude-haiku-4-5-v1:0",
		"claude-sonnet-4-6",
	}
	for _, m := range cases {
		_, err := New(Config{LLM: &fakeLLM{}, Model: m})
		if err != nil {
			t.Errorf("model %q: unexpected error: %v", m, err)
		}
	}
}

func TestNew_RequiresLLM(t *testing.T) {
	_, err := New(Config{Model: "gpt-4o"})
	if !errors.Is(err, ErrLLMMissing) {
		t.Errorf("got %v, want ErrLLMMissing", err)
	}
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(Config{LLM: &fakeLLM{}})
	if !errors.Is(err, ErrModelMissing) {
		t.Errorf("got %v, want ErrModelMissing", err)
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	g, err := New(Config{LLM: &fakeLLM{}, Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if g.workers != DefaultWorkers {
		t.Errorf("workers = %d", g.workers)
	}
	if g.maxTokens != DefaultMaxTokens {
		t.Errorf("maxTokens = %d", g.maxTokens)
	}
	if g.maxFailureRate != DefaultMaxFailureRate {
		t.Errorf("maxFailureRate = %f", g.maxFailureRate)
	}
}

// --- IsReasoningClassModel ---

func TestIsReasoningClassModel_Matches(t *testing.T) {
	reasoning := []string{
		"deepseek.deepseek-r1-v1:0",
		"r1-distill-llama",
		"o1-preview",
		"o3-mini-high",
		"o4-mini",
		"some-model-extended-thinking",
		"DeepSeek-R1", // case-insensitive
		// GPT-5 family — reasoning by default, must be rejected for blurb.
		"gpt-5",
		"gpt-5-mini",
		"gpt-5-nano",
		"gpt-5-2025-08-07",
		"GPT-5", // case-insensitive
	}
	for _, m := range reasoning {
		if !IsReasoningClassModel(m) {
			t.Errorf("%q should be reasoning", m)
		}
	}
}

func TestIsReasoningClassModel_NoFalsePositives(t *testing.T) {
	normal := []string{
		"gpt-4o",
		"gpt-4.1-nano",
		"claude-haiku-4-5",
		"claude-sonnet-4-6",
		"qwen.qwen3-32b-v1:0",
		"ollama3-70b",
	}
	for _, m := range normal {
		if IsReasoningClassModel(m) {
			t.Errorf("%q should NOT be reasoning", m)
		}
	}
}

// --- Generate() happy path ---

func TestGenerate_HappyPath_SingleTable(t *testing.T) {
	llm := &fakeLLM{respondWith: "A table of user accounts."}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})

	outs, err := g.Generate(context.Background(), []Input{sampleInput("users")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(outs) != 1 || outs[0].Table != "users" {
		t.Fatalf("outs = %+v", outs)
	}
	if outs[0].Blurb != "A table of user accounts." {
		t.Errorf("blurb = %q", outs[0].Blurb)
	}
	if outs[0].Err != nil {
		t.Errorf("unexpected err: %v", outs[0].Err)
	}
	if outs[0].InputTokens != 10 || outs[0].OutputTokens != 20 {
		t.Errorf("usage lost: %+v", outs[0])
	}
}

func TestGenerate_EmptyInputs(t *testing.T) {
	g, _ := New(Config{LLM: &fakeLLM{}, Model: "gpt-4o"})
	outs, err := g.Generate(context.Background(), nil, nil)
	if err != nil || outs != nil {
		t.Errorf("outs=%v err=%v", outs, err)
	}
}

func TestGenerate_ProgressCallback_FiresOncePerTable(t *testing.T) {
	llm := &fakeLLM{}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})
	inputs := []Input{sampleInput("a"), sampleInput("b"), sampleInput("c")}

	var calls int64
	progress := func(n int) { atomic.AddInt64(&calls, int64(n)) }

	_, err := g.Generate(context.Background(), inputs, progress)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&calls) != 3 {
		t.Errorf("progress called %d times, want 3", calls)
	}
}

func TestGenerate_PreservesOrder(t *testing.T) {
	llm := &fakeLLM{}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o", Workers: 4})
	inputs := make([]Input, 20)
	for i := range inputs {
		inputs[i] = sampleInput(fmt.Sprintf("t%02d", i))
	}
	outs, err := g.Generate(context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range outs {
		if o.Table != fmt.Sprintf("t%02d", i) {
			t.Errorf("order broken at %d: %q", i, o.Table)
		}
	}
}

func TestGenerate_TrimsWhitespace(t *testing.T) {
	llm := &fakeLLM{respondWith: "   a table.   \n\n"}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})
	outs, _ := g.Generate(context.Background(), []Input{sampleInput("t")}, nil)
	if outs[0].Blurb != "a table." {
		t.Errorf("blurb = %q", outs[0].Blurb)
	}
}

func TestGenerate_RejectsOversize(t *testing.T) {
	giant := strings.Repeat("x", MaxBlurbLen+500)
	llm := &fakeLLM{respondWith: giant}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})
	outs, _ := g.Generate(context.Background(), []Input{sampleInput("t")}, nil)
	// Oversize responses fail the per-table blurb rather than silently
	// truncating mid-word and poisoning the embedding.
	if outs[0].Err == nil {
		t.Error("oversize blurb should return an error, not silently truncate")
	}
	if outs[0].Blurb != "" {
		t.Errorf("failed blurb should be empty, got %d chars", len(outs[0].Blurb))
	}
}

func TestGenerate_EmptyResponseIsError(t *testing.T) {
	llm := &fakeLLM{respondWith: "   \n  "}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})
	outs, _ := g.Generate(context.Background(), []Input{sampleInput("t")}, nil)
	if outs[0].Err == nil {
		t.Error("empty response should produce Err")
	}
	if outs[0].Blurb != "" {
		t.Errorf("blurb on error should be empty, got %q", outs[0].Blurb)
	}
}

// --- Failure budget ---

func TestGenerate_BelowFailureBudget_Succeeds(t *testing.T) {
	// 20 inputs, 1 SAFETY block = 5% = at the budget (1 allowed).
	// SAFETY is a deterministic empty-response reason so the per-blurb
	// retry doesn't absorb it — the test still exercises the budget
	// machinery with exactly one failure landing in the output slice.
	scripted := make([]scriptedResp, 20)
	for i := range scripted {
		scripted[i] = scriptedResp{text: "ok"}
	}
	scripted[10] = scriptedResp{text: "", stopReason: "SAFETY"}
	llm := &fakeLLM{scripted: scripted}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o", Workers: 1, MaxFailureRate: 0.05})

	inputs := make([]Input, 20)
	for i := range inputs {
		inputs[i] = sampleInput(fmt.Sprintf("t%d", i))
	}
	outs, err := g.Generate(context.Background(), inputs, nil)
	if err != nil {
		t.Fatalf("should succeed at 1/20 failures: %v", err)
	}
	failed := 0
	for _, o := range outs {
		if o.Err != nil {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

func TestGenerate_AboveFailureBudget_AbortsWithSentinel(t *testing.T) {
	// 20 inputs, all fail → 100% >> 5%.
	scripted := make([]scriptedResp, 20)
	for i := range scripted {
		scripted[i] = scriptedResp{err: fmt.Errorf("boom-%d", i)}
	}
	llm := &fakeLLM{scripted: scripted}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o", Workers: 1, MaxFailureRate: 0.05})
	inputs := make([]Input, 20)
	for i := range inputs {
		inputs[i] = sampleInput(fmt.Sprintf("t%d", i))
	}
	outs, err := g.Generate(context.Background(), inputs, nil)
	if !errors.Is(err, ErrTooManyFailures) {
		t.Errorf("expected ErrTooManyFailures, got %v", err)
	}
	if len(outs) != 20 {
		t.Errorf("partial outs len = %d, want 20 (caller needs the array)", len(outs))
	}
}

func TestGenerate_TinyInputAllowsOneFailure(t *testing.T) {
	// 3 inputs, 1 fails: 33% > 5% technically, but the "min 1 failure"
	// rule should still allow it (transient errors on tiny dev warehouses).
	scripted := []scriptedResp{
		{text: "ok1"},
		{err: errors.New("transient")},
		{text: "ok3"},
	}
	llm := &fakeLLM{scripted: scripted}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o", Workers: 1})
	inputs := []Input{sampleInput("a"), sampleInput("b"), sampleInput("c")}
	_, err := g.Generate(context.Background(), inputs, nil)
	if err != nil {
		t.Errorf("1-failure on 3 inputs should tolerate (min 1 allowed): %v", err)
	}
}

// --- Context cancellation ---

func TestGenerate_ContextCancelled_AllOutputsFilled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	llm := &fakeLLM{}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})
	inputs := []Input{sampleInput("a"), sampleInput("b")}
	outs, err := g.Generate(ctx, inputs, nil)
	if err == nil {
		t.Error("cancelled ctx should error")
	}
	// Length invariant: outputs map 1:1 to inputs even under cancellation.
	if len(outs) != 2 {
		t.Errorf("outs len = %d, want 2", len(outs))
	}
}

// --- Prompt shape ---

func TestBuildPrompt_IncludesPlanRequiredFields(t *testing.T) {
	in := Input{
		Dataset: "sales",
		Schema: models.TableSchema{
			TableName: "orders",
			RowCount:  123456,
			Columns: []models.ColumnInfo{
				{Name: "order_id", Type: "INT64", Category: "primary_key"},
				{Name: "customer_id", Type: "INT64"},
				{Name: "total", Type: "FLOAT64", Nullable: true, Category: "metric"},
			},
			KeyColumns: []string{"order_id", "customer_id"},
		},
		DomainPackBlurb: "E-commerce storefront.",
	}
	p := buildPrompt(in)
	// Plan §6.1 required sentences.
	mustContain := []string{
		"Describe the table \"orders\"",
		"what a row represents",
		"Ground every claim",
		"Do not invent columns",
		"Dataset:",
		"Columns:",
		"order_id INT64",
		"customer_id INT64",
		"total FLOAT64 NULL",
		"Row count:      123456",
		"FK hints:       order_id, customer_id",
		"Domain pack:    E-commerce storefront.",
		"Return plain text, 2-4 sentences.",
	}
	for _, m := range mustContain {
		if !strings.Contains(p, m) {
			t.Errorf("prompt missing %q\nPROMPT:\n%s", m, p)
		}
	}
}

func TestBuildPrompt_NoDomainPackBlurbGracefullyReported(t *testing.T) {
	in := Input{
		Schema: models.TableSchema{TableName: "t", Columns: []models.ColumnInfo{{Name: "c"}}},
	}
	p := buildPrompt(in)
	if !strings.Contains(p, "Domain pack:    (none — describe generically)") {
		t.Error("empty domain pack should be marked explicitly")
	}
}

func TestBuildPrompt_NoFKHintsRenderedNone(t *testing.T) {
	in := Input{Schema: models.TableSchema{TableName: "t"}}
	p := buildPrompt(in)
	if !strings.Contains(p, "FK hints:       (none)") {
		t.Error("missing FK hints should be marked")
	}
}

func TestBuildPrompt_IncludesSampleData(t *testing.T) {
	in := Input{
		Schema: models.TableSchema{
			TableName: "t",
			Columns:   []models.ColumnInfo{{Name: "status", Type: "STRING"}},
			SampleData: []map[string]interface{}{
				{"status": "active"},
				{"status": "inactive"},
			},
		},
	}
	p := buildPrompt(in)
	if !strings.Contains(p, "samples: active, inactive") {
		t.Errorf("samples not rendered: %s", p)
	}
}

func TestFirstNonNilSample_TruncatesLong(t *testing.T) {
	rows := []map[string]interface{}{{"x": strings.Repeat("a", 100)}}
	got := firstNonNilSample(rows, "x", 3)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long value should be truncated: %q", got)
	}
}

func TestFirstNonNilSample_SkipsNil(t *testing.T) {
	rows := []map[string]interface{}{
		{"x": nil},
		{"x": "real"},
	}
	got := firstNonNilSample(rows, "x", 3)
	if got != "real" {
		t.Errorf("got %q", got)
	}
}

func TestFormatFKHints_FiltersBlanks(t *testing.T) {
	got := formatFKHints([]string{"a", "", "b"})
	if got != "a, b" {
		t.Errorf("got %q", got)
	}
}

// --- per-blurb retry + finish-reason surfacing ---

// newTestGen builds a Generator whose only knob is the LLM — every
// other field uses defaults. The MaxFailureRate stays at 5% so the
// failure-budget machinery is exercised the same way production code
// uses it.
func newTestGen(t *testing.T, llm gollm.Provider) *Generator {
	t.Helper()
	g, err := New(Config{LLM: llm, Model: "gpt-4o", ProviderName: "fake"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

// TestOneBlurb_RetriesEmptyResponse_WithoutFinishReason locks the
// primary motivation for the retry: a transient empty response that
// the provider couldn't classify (no finish_reason surfaced) clears
// on the second call. Without the retry, the run trips the 5%
// failure budget on cohorts where the model is mostly fine but
// occasionally returns nothing.
func TestOneBlurb_RetriesEmptyResponse_WithoutFinishReason(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: ""},           // first attempt: transient empty
		{text: "A table of orders.", stopReason: "STOP"}, // retry succeeds
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err != nil {
		t.Fatalf("expected success after retry, got err: %v", out.Err)
	}
	if out.Blurb != "A table of orders." {
		t.Errorf("blurb = %q, want 'A table of orders.'", out.Blurb)
	}
	if got := atomic.LoadInt64(&llm.called); got != 2 {
		t.Errorf("LLM called %d times, want 2 (one retry)", got)
	}
}

// TestOneBlurb_DoesNotRetry_SafetyBlock pins the retry-classifier:
// a deterministic empty (SAFETY) should fail-fast with one call,
// not waste a second token-billed attempt on a prompt the model
// will keep refusing.
func TestOneBlurb_DoesNotRetry_SafetyBlock(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: "SAFETY"},
		{text: "should never run", stopReason: "STOP"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected SAFETY block to surface as error, got success")
	}
	if !strings.Contains(out.Err.Error(), "finish_reason=SAFETY") {
		t.Errorf("error %q must include finish_reason=SAFETY for operator diagnosis", out.Err)
	}
	if got := atomic.LoadInt64(&llm.called); got != 1 {
		t.Errorf("LLM called %d times, want exactly 1 (SAFETY is deterministic)", got)
	}
}

// TestOneBlurb_DoesNotRetry_MaxTokens covers the other deterministic
// finish_reason: MAX_TOKENS means the response WAS produced but we
// asked for too few tokens. Retrying with the same MaxTokens would
// hit the same wall — surface the reason so the operator raises
// MaxTokens or picks a less verbose model.
func TestOneBlurb_DoesNotRetry_MaxTokens(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: "MAX_TOKENS"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected MAX_TOKENS empty to surface as error")
	}
	if !strings.Contains(out.Err.Error(), "finish_reason=MAX_TOKENS") {
		t.Errorf("error %q must include finish_reason=MAX_TOKENS", out.Err)
	}
	if got := atomic.LoadInt64(&llm.called); got != 1 {
		t.Errorf("LLM called %d times, want 1", got)
	}
}

// TestOneBlurb_RetriesChatError absorbs the most common non-empty
// transient: a Chat() error (rate-limit, brief 5xx). Permanent
// errors fall through to the second attempt and surface — they
// don't cost wall-clock because the worker pool is fan-out.
func TestOneBlurb_RetriesChatError(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{err: fmt.Errorf("upstream 429: rate limit")},
		{text: "ok"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err != nil {
		t.Fatalf("expected success after Chat-error retry, got: %v", out.Err)
	}
	if got := atomic.LoadInt64(&llm.called); got != 2 {
		t.Errorf("LLM called %d times, want 2", got)
	}
}

// TestOneBlurb_GivesUpAfterMaxAttempts ensures the retry budget is
// bounded: two consecutive transient empties surface the error
// rather than looping. Returns the LATEST error so the operator
// sees the most recent finish_reason hint.
func TestOneBlurb_GivesUpAfterMaxAttempts(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: ""},
		{text: "", stopReason: "OTHER"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected failure after exhausted retries")
	}
	if got := atomic.LoadInt64(&llm.called); got != int64(blurbMaxAttempts) {
		t.Errorf("LLM called %d times, want %d (blurbMaxAttempts)", got, blurbMaxAttempts)
	}
	if !strings.Contains(out.Err.Error(), "finish_reason=OTHER") {
		t.Errorf("error %q must reflect the latest attempt's finish_reason=OTHER", out.Err)
	}
}

// TestOneBlurb_DoesNotRetry_RunawayResponse covers the third
// deterministic-failure mode: a response that exceeds MaxBlurbLen.
// Retrying with the same prompt + temperature=0 would produce the
// same runaway. Caller diagnostic: pick a less verbose model.
func TestOneBlurb_DoesNotRetry_RunawayResponse(t *testing.T) {
	huge := strings.Repeat("x", MaxBlurbLen+1)
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: huge},
		{text: "should never run"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected runaway response to surface as error")
	}
	if !strings.Contains(out.Err.Error(), "MaxBlurbLen") {
		t.Errorf("error %q must mention MaxBlurbLen", out.Err)
	}
	if got := atomic.LoadInt64(&llm.called); got != 1 {
		t.Errorf("LLM called %d times, want 1 (runaway is deterministic)", got)
	}
}

// TestOneBlurb_EmptyResponse_OmitsFinishReasonWhenAbsent confirms
// the error message degrades gracefully — providers that don't
// surface a finish_reason (some Ollama paths, some self-hosted
// gateways) shouldn't see a "finish_reason=" suffix with nothing
// after the equals sign. Catches the regression where every empty
// error suddenly grew a trailing "finish_reason=" tag.
func TestOneBlurb_EmptyResponse_OmitsFinishReasonWhenAbsent(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: ""},
		{text: "", stopReason: ""},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected failure after retry exhausted")
	}
	if strings.Contains(out.Err.Error(), "finish_reason=") {
		t.Errorf("error %q should not include finish_reason= when provider didn't surface one", out.Err)
	}
}

// TestOneBlurb_BackoffRespectsContextCancellation ensures a
// cancelled context during the retry sleep returns ctx.Err()
// promptly — staring at time.After(500ms) when every retry would
// produce ctx.Err() anyway wastes worker-pool time on a failing
// run.
func TestOneBlurb_BackoffRespectsContextCancellation(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: ""}, // first attempt: empty, retryable
	}}
	g := newTestGen(t, llm)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately — the first attempt should complete,
	// then the backoff sleep should bail on ctx.Done.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	out := g.oneBlurb(ctx, sampleInput("orders"))
	elapsed := time.Since(start)
	if out.Err == nil {
		t.Fatal("expected ctx-cancelled retry to surface as error")
	}
	if !errors.Is(out.Err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", out.Err)
	}
	if elapsed >= blurbRetryBackoff {
		t.Errorf("oneBlurb took %v, want < blurbRetryBackoff=%v (ctx cancel should bail the sleep)", elapsed, blurbRetryBackoff)
	}
}

// TestBlurbErrorRetryable_Truthtable pins the classifier so a future
// edit can't silently flip retryability for the deterministic cases.
func TestBlurbErrorRetryable_Truthtable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil_no_retry", nil, false},
		{"empty_no_finish_reason_retry", &emptyResponseErr{providerID: "p", model: "m"}, true},
		{"empty_safety_no_retry", &emptyResponseErr{providerID: "p", model: "m", stopReason: "SAFETY"}, false},
		{"empty_recitation_no_retry", &emptyResponseErr{providerID: "p", model: "m", stopReason: "RECITATION"}, false},
		{"empty_maxtokens_no_retry", &emptyResponseErr{providerID: "p", model: "m", stopReason: "MAX_TOKENS"}, false},
		{"empty_stop_retry", &emptyResponseErr{providerID: "p", model: "m", stopReason: "STOP"}, true},
		{"empty_other_retry", &emptyResponseErr{providerID: "p", model: "m", stopReason: "OTHER"}, true},
		{"max_blurb_len_no_retry", &runawayResponseErr{providerID: "fake/m", got: 5000}, false},
		{"chat_error_retry", fmt.Errorf("blurb(x): upstream 429"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := blurbErrorRetryable(c.err); got != c.want {
				t.Errorf("blurbErrorRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestOneBlurb_AccumulatesUsageAcrossRetries pins the cost-
// accounting fix: a failed empty-response attempt charges input
// tokens upstream, so when the retry succeeds the final Output
// must carry both attempts' usage so schema_indexer's
// IncrementTokens call sees the full charge.
func TestOneBlurb_AccumulatesUsageAcrossRetries(t *testing.T) {
	// fakeLLM stamps Usage{Input:10, Output:20} on every response. The
	// first attempt empties out but consumes those tokens; the retry
	// succeeds and also consumes them. The final Output must total
	// 20 input / 40 output, not 10 / 20.
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: ""},
		{text: "A table of orders."},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if out.InputTokens != 20 {
		t.Errorf("InputTokens = %d, want 20 (failed-attempt 10 + retry 10)", out.InputTokens)
	}
	if out.OutputTokens != 40 {
		t.Errorf("OutputTokens = %d, want 40 (failed-attempt 20 + retry 20)", out.OutputTokens)
	}
}

// TestOneBlurb_DoesNotRetry_LowercaseMaxTokens covers Anthropic's
// snake_case stop_reason; the classifier must skip the retry on
// `max_tokens` exactly the same as on Gemini's `MAX_TOKENS`.
func TestOneBlurb_DoesNotRetry_LowercaseMaxTokens(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: "max_tokens"},
		{text: "should never run"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected lowercase max_tokens to be deterministic")
	}
	if got := atomic.LoadInt64(&llm.called); got != 1 {
		t.Errorf("LLM called %d times, want 1 (lowercase max_tokens deterministic)", got)
	}
}

// TestOneBlurb_DoesNotRetry_OpenAILength covers OpenAI's
// finish_reason="length" alias for max_tokens. Same semantics as
// Gemini MAX_TOKENS; classifier table includes "length" so we
// don't double-bill the user on under-MaxTokens runs.
func TestOneBlurb_DoesNotRetry_OpenAILength(t *testing.T) {
	llm := &fakeLLM{scripted: []scriptedResp{
		{text: "", stopReason: "length"},
		{text: "should never run"},
	}}
	g := newTestGen(t, llm)
	out := g.oneBlurb(context.Background(), sampleInput("orders"))
	if out.Err == nil {
		t.Fatal("expected OpenAI 'length' stop reason to be deterministic")
	}
	if got := atomic.LoadInt64(&llm.called); got != 1 {
		t.Errorf("LLM called %d times, want 1 (OpenAI 'length' alias)", got)
	}
}

// TestRunawayResponseError_TypedClassification ensures the
// non-retryable classification of MaxBlurbLen violations now goes
// through errors.As on the typed *runawayResponseErr — a future
// edit to the formatted message can't silently flip the
// classifier the way a strings.Contains check would.
func TestRunawayResponseError_TypedClassification(t *testing.T) {
	err := &runawayResponseErr{providerID: "fake/m", got: 9999}
	if blurbErrorRetryable(err) {
		t.Error("runawayResponseErr must be classified as non-retryable")
	}
	// errors.As round-trip through fmt.Errorf wrapping (the way
	// orchestrators wrap errors with extra context).
	wrapped := fmt.Errorf("schema_indexer: blurb: %w", err)
	if blurbErrorRetryable(wrapped) {
		t.Error("wrapped runawayResponseErr must still be classified as non-retryable")
	}
	// Message must still carry the wording operators expect (the
	// MaxBlurbLen-exceeds wording is in the agent's error log).
	if !strings.Contains(err.Error(), "MaxBlurbLen") {
		t.Errorf("error message must mention MaxBlurbLen; got %q", err.Error())
	}
}
