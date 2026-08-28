package ai

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// The verbatim Bedrock/GLM ValidationException from issue #347.
const bedrockOverflowErr = `bedrock/openai-compat: InvokeModel failed: operation error Bedrock Runtime: ` +
	`InvokeModel, https response error StatusCode: 400, RequestID: abc, ValidationException: ` +
	`This model's maximum context length is 202752 tokens. However, you requested 64000 output ` +
	`tokens and your prompt contains at least 138753 input tokens, for a total of at least 202753 ` +
	`tokens. Please reduce the length of the input prompt or the number of requested output tokens.`

// The classic OpenAI phrasing.
const openaiOverflowErr = `openai: API error (400): invalid_request_error - This model's maximum ` +
	`context length is 8192 tokens. However, you requested 9000 tokens (5000 in the messages, ` +
	`4000 in the completion). Please reduce the length of the messages or completion.`

// Real Bedrock openai-compat (qwen3-32b) captures — two distinct shapes.
// Output-cap alone exceeded:
const bedrockMaxOutputErr = `bedrock/openai-compat: InvokeModel failed: operation error Bedrock ` +
	`Runtime: InvokeModel, https response error StatusCode: 400, RequestID: x, ValidationException: ` +
	`{"error":{"code":"validation_error","message":"'max_tokens' (990000) exceeds model maximum ` +
	`(32768)","param":null,"type":"invalid_request_error"}}`

// Total-context exceeded, but input reported in CHARACTERS ("0 input tokens"):
const bedrockCharsOverflowErr = `bedrock/openai-compat: InvokeModel failed: operation error Bedrock ` +
	`Runtime: InvokeModel, https response error StatusCode: 400, ValidationException: This model's ` +
	`maximum context length is 32768 tokens. However, you requested 32768 output tokens and your ` +
	`prompt contains 600069 characters (more than 0 characters, which is the upper bound for 0 ` +
	`input tokens). Please reduce the length of the input prompt or the number of requested output tokens.`

func TestParseContextLengthError_Bedrock(t *testing.T) {
	window, input, ok := parseContextLengthError(bedrockOverflowErr)
	if !ok {
		t.Fatal("expected Bedrock overflow to parse")
	}
	if window != 202752 || input != 138753 {
		t.Fatalf("got window=%d input=%d, want 202752/138753", window, input)
	}
}

func TestParseContextLengthError_OpenAI(t *testing.T) {
	window, input, ok := parseContextLengthError(openaiOverflowErr)
	if !ok {
		t.Fatal("expected OpenAI overflow to parse")
	}
	if window != 8192 || input != 5000 {
		t.Fatalf("got window=%d input=%d, want 8192/5000", window, input)
	}
}

func TestParseContextLengthError_Unparseable(t *testing.T) {
	// Recognised as overflow (has the marker) but no numbers we can key on.
	if _, _, ok := parseContextLengthError("the model reports maximum context length exceeded"); ok {
		t.Fatal("expected unparseable numbers to yield ok=false")
	}
	// Not an overflow at all.
	if _, _, ok := parseContextLengthError("some unrelated 400 error"); ok {
		t.Fatal("expected non-overflow string to yield ok=false")
	}
}

func TestIsContextLengthError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New(bedrockOverflowErr), true},
		{errors.New(openaiOverflowErr), true},
		{errors.New(bedrockMaxOutputErr), true},
		{errors.New(bedrockCharsOverflowErr), true},
		{errors.New("openai: API error: context_length_exceeded"), true},
		{errors.New("bedrock: AccessDeniedException: not authorized"), false},
		{nil, false},
	}
	for i, c := range cases {
		if got := isContextLengthError(c.err); got != c.want {
			t.Errorf("case %d: isContextLengthError=%v, want %v", i, got, c.want)
		}
	}
}

func TestReducedMaxTokens_OutputCapShape(t *testing.T) {
	// "'max_tokens' (990000) exceeds model maximum (32768)" → retry below 32768,
	// leaving room for input + margin (some models set output cap == window, so
	// requesting exactly 32768 would re-overflow once input is counted).
	nm, ok := reducedMaxTokensForContextOverflow(990000, 100, errors.New(bedrockMaxOutputErr))
	if !ok {
		t.Fatal("output-cap shape should produce a retry")
	}
	margin := 32768 * contextOverflowRetryMarginPct / 100
	if want := 32768 - 100 - margin; nm != want {
		t.Fatalf("output-cap shape nm=%d, want %d", nm, want)
	}
	if nm >= 32768 {
		t.Fatalf("retry must be strictly below the output cap, got %d", nm)
	}
	// The output cap is NOT a context window — self-calibration must not learn it.
	if w := windowFromContextLengthError(errors.New(bedrockMaxOutputErr)); w != 0 {
		t.Fatalf("output-cap shape must not yield a window, got %d", w)
	}
}

func TestReducedMaxTokens_CharsShape_NoRetryButLearnsWindow(t *testing.T) {
	// The real client passes an input estimate from the request (here ~150K
	// tokens for the 600K-char prompt), which alone overflows the 32768 window →
	// output reduction can't help, so no retry...
	if _, ok := reducedMaxTokensForContextOverflow(32768, 150000, errors.New(bedrockCharsOverflowErr)); ok {
		t.Fatal("input alone overfilling the window must yield no retry")
	}
	// ...but the true window (32768) is still learned for self-calibration.
	if w := windowFromContextLengthError(errors.New(bedrockCharsOverflowErr)); w != 32768 {
		t.Fatalf("chars shape window = %d, want 32768", w)
	}
}

func TestReducedMaxTokensForContextOverflow(t *testing.T) {
	// Bedrock: newMax = 202752 - 138753 - (202752*2/100=4055) = 59944.
	nm, ok := reducedMaxTokensForContextOverflow(64000, 0, errors.New(bedrockOverflowErr))
	if !ok {
		t.Fatal("expected a reduced max for the Bedrock overflow")
	}
	if want := 202752 - 138753 - (202752 * 2 / 100); nm != want {
		t.Fatalf("got newMax=%d, want %d", nm, want)
	}
	if nm+138753 > 202752 {
		t.Fatalf("reduced request still overflows: %d + 138753 > 202752", nm)
	}

	// Non-overflow error → no retry.
	if _, ok := reducedMaxTokensForContextOverflow(64000, 0, errors.New("bedrock: AccessDenied")); ok {
		t.Fatal("non-overflow error must not trigger a retry")
	}

	// Input alone overfills the window → output reduction can't help.
	tight := `maximum context length is 1000 tokens. However, you requested 500 output tokens ` +
		`and your prompt contains at least 2000 input tokens`
	if _, ok := reducedMaxTokensForContextOverflow(500, 0, errors.New(tight)); ok {
		t.Fatal("input >= window must yield ok=false")
	}

	// Recognised overflow but no usable number to recompute from → no retry
	// (rely on self-calibration + the window-coupled input budget).
	if _, ok := reducedMaxTokensForContextOverflow(20000, 0, errors.New("maximum context length exceeded")); ok {
		t.Fatal("unparseable overflow must yield ok=false (no blind retry)")
	}

	// Computed newMax would not actually reduce currentMax → ok=false.
	roomy := `maximum context length is 200000 tokens. However, you requested 5 output tokens ` +
		`and your prompt contains at least 100 input tokens`
	if _, ok := reducedMaxTokensForContextOverflow(4096, 0, errors.New(roomy)); ok {
		t.Fatal("a newMax >= currentMax must yield ok=false")
	}
}

func TestWindowFromContextLengthError(t *testing.T) {
	if got := windowFromContextLengthError(errors.New(bedrockOverflowErr)); got != 202752 {
		t.Fatalf("got window=%d, want 202752", got)
	}
	if got := windowFromContextLengthError(errors.New("plain 400")); got != 0 {
		t.Fatalf("non-overflow should yield 0, got %d", got)
	}
}

// recordingOverflowLLM returns a scripted error on the first call and a success
// on the second, recording the MaxTokens seen on every call.
type recordingOverflowLLM struct {
	calls        int64
	firstErr     error
	seenMaxToken []int
}

func (f *recordingOverflowLLM) Chat(_ context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	n := atomic.AddInt64(&f.calls, 1)
	f.seenMaxToken = append(f.seenMaxToken, req.MaxTokens)
	if n == 1 && f.firstErr != nil {
		return nil, f.firstErr
	}
	return &gollm.ChatResponse{Content: "ok", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func (f *recordingOverflowLLM) Validate(_ context.Context) error { return nil }

func TestCreateMessage_AdaptiveContextOverflowRetry(t *testing.T) {
	fake := &recordingOverflowLLM{firstErr: errors.New(bedrockOverflowErr)}
	client, _ := New(fake, "GLM-5")

	var learnedModel string
	var learnedWindow int
	client.SetContextWindowObserver(func(model string, window int) {
		learnedModel, learnedWindow = model, window
	})

	resp, err := client.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "hi"}}, "", 64000)
	if err != nil {
		t.Fatalf("expected the adaptive retry to succeed, got err: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("expected the second-call success content, got %q", resp.Content)
	}
	if fake.calls != 2 {
		t.Fatalf("expected exactly 2 provider calls (overflow + retry), got %d", fake.calls)
	}
	want := 202752 - 138753 - (202752 * 2 / 100)
	if fake.seenMaxToken[1] != want {
		t.Fatalf("retry MaxTokens=%d, want %d", fake.seenMaxToken[1], want)
	}
	if learnedModel != "GLM-5" || learnedWindow != 202752 {
		t.Fatalf("observer got (%q,%d), want (GLM-5,202752)", learnedModel, learnedWindow)
	}
}

func TestCreateMessage_NonOverflow400_NoRetry(t *testing.T) {
	fake := &recordingOverflowLLM{firstErr: fmt.Errorf("bedrock: API error (400): AccessDeniedException")}
	client, _ := New(fake, "some-model")

	observerFired := false
	client.SetContextWindowObserver(func(string, int) { observerFired = true })

	_, err := client.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "hi"}}, "", 64000)
	if err == nil {
		t.Fatal("a non-overflow 400 must surface as an error")
	}
	if fake.calls != 1 {
		t.Fatalf("a non-overflow 400 must not retry; got %d calls", fake.calls)
	}
	if observerFired {
		t.Fatal("observer must not fire on a non-overflow error")
	}
}
