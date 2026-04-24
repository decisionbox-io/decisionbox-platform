package blurb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

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
	text string
	err  error
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
			return &gollm.ChatResponse{Content: s.text, Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}}, nil
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

func TestGenerate_TruncatesOversize(t *testing.T) {
	giant := strings.Repeat("x", MaxBlurbLen+500)
	llm := &fakeLLM{respondWith: giant}
	g, _ := New(Config{LLM: llm, Model: "gpt-4o"})
	outs, _ := g.Generate(context.Background(), []Input{sampleInput("t")}, nil)
	if len(outs[0].Blurb) != MaxBlurbLen {
		t.Errorf("blurb len = %d, want %d", len(outs[0].Blurb), MaxBlurbLen)
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
	// 20 inputs, 1 script-failure = 5% = at the budget (1 allowed).
	scripted := make([]scriptedResp, 20)
	for i := range scripted {
		scripted[i] = scriptedResp{text: "ok"}
	}
	scripted[10] = scriptedResp{err: errors.New("transient")}
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
