package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// retryFakeLLM scripts a deterministic sequence of responses so
// tests can verify exact retry behaviour. Distinct from the
// existing test stubs to keep retry-specific cases isolated.
type retryFakeLLM struct {
	calls    int64
	scripted []retryScript
}

type retryScript struct {
	resp *gollm.ChatResponse
	err  error
}

func (f *retryFakeLLM) Chat(ctx context.Context, _ gollm.ChatRequest) (*gollm.ChatResponse, error) {
	idx := int(atomic.AddInt64(&f.calls, 1) - 1)
	if idx < len(f.scripted) {
		s := f.scripted[idx]
		return s.resp, s.err
	}
	return &gollm.ChatResponse{Content: "exhausted-script"}, nil
}

func (f *retryFakeLLM) Validate(_ context.Context) error { return nil }

// shortBackoffFor speeds up the retry loop in tests by pointing
// LLM_RETRY_BASE_BACKOFF at 1ms. Per-test t.Setenv keeps each
// case isolated.
func shortBackoffFor(t *testing.T) {
	t.Helper()
	t.Setenv(LLMRetryBaseBackoffEnv, "1ms")
}

func okResp() *gollm.ChatResponse {
	return &gollm.ChatResponse{Content: "ok", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}}
}

// Vertex / OpenAI / Bedrock providers format HTTP errors as
// "<provider>: ... (status NNN): ...". Build representative
// strings so the classifier sees real shapes.
func httpErr(code int) error {
	return fmt.Errorf("vertex-ai/google-native: API error (status %d): something happened", code)
}

// TestChatWithRetry_RetriesOn499_RecoversOnSecondAttempt pins
// the motivating case: Vertex shared-serving cancels one
// in-flight request, the retry succeeds, and the discovery run
// continues instead of aborting at step 20.
func TestChatWithRetry_RetriesOn499_RecoversOnSecondAttempt(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: httpErr(499)},
		{resp: okResp()},
	}}
	resp, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("expected success after one retry; got %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("got resp %+v", resp)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 2 {
		t.Errorf("LLM called %d times, want 2", got)
	}
}

// TestChatWithRetry_RetriesOn429_HonoursWidenedBudget covers
// the env-knob path: setting LLM_RETRY_MAX_ATTEMPTS=3 lets two
// consecutive 429s be absorbed by the budget with success on
// the third attempt. With the default 2-attempt budget the
// same scenario would surface after the first retry; the env
// override is the operator's tool for flaky-upstream tuning.
func TestChatWithRetry_RetriesOn429_HonoursWidenedBudget(t *testing.T) {
	shortBackoffFor(t)
	t.Setenv(LLMRetryMaxAttemptsEnv, "3")
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: httpErr(429)},
		{err: httpErr(429)},
		{resp: okResp()},
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("expected success after two retries with widened budget; got %v", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 3 {
		t.Errorf("LLM called %d times, want 3 (one initial + two retries)", got)
	}
}

// TestChatWithRetry_RetriesOn5xx pins server-error retryability.
// The blurb-side classifier already covers 5xx via the "any
// other Chat() error" catch-all; this test pins it explicitly
// for the LLM-call path so the contract is documented at the
// call site.
func TestChatWithRetry_RetriesOn5xx(t *testing.T) {
	shortBackoffFor(t)
	for _, code := range []int{500, 502, 503, 504} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			llm := &retryFakeLLM{scripted: []retryScript{
				{err: httpErr(code)},
				{resp: okResp()},
			}}
			_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
			if err != nil {
				t.Fatalf("status %d should be retryable, got %v", code, err)
			}
		})
	}
}

// TestChatWithRetry_RetriesOnNetworkErrors absorbs the canonical
// transport-layer failures: a net.Error, plain EOF, and the
// substring-detected "connection reset" / "broken pipe" /
// "no such host" classes that providers wrap into untyped
// errors.New strings. Without this, a TCP RST mid-stream would
// kill discovery just like a 499 used to.
func TestChatWithRetry_RetriesOnNetworkErrors(t *testing.T) {
	shortBackoffFor(t)
	cases := []struct {
		name string
		err  error
	}{
		{"net_error_timeout", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}},
		{"io_eof", io.EOF},
		{"unexpected_eof", io.ErrUnexpectedEOF},
		{"connection_reset_string", errors.New("read tcp 1.2.3.4:443: connection reset by peer")},
		{"broken_pipe_string", errors.New("write: broken pipe")},
		{"no_such_host_string", errors.New("dial tcp: lookup foo: no such host")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			llm := &retryFakeLLM{scripted: []retryScript{
				{err: c.err},
				{resp: okResp()},
			}}
			_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
			if err != nil {
				t.Fatalf("%s should be retryable, got %v", c.name, err)
			}
		})
	}
}

// TestChatWithRetry_GivesUpAfterMaxAttempts ensures the retry
// budget is bounded. A permanently-broken upstream surfaces the
// error wrapped with the attempt count so operators can see in
// the agent log that we exhausted retries rather than hanging.
func TestChatWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: httpErr(499)},
		{err: httpErr(499)},
		{err: httpErr(499)},
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err == nil {
		t.Fatal("expected error after exhausting retry budget")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("error %q should mention exhausted retries", err)
	}
	if !strings.Contains(err.Error(), "status 499") {
		t.Errorf("error %q should wrap the underlying 499", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != int64(DefaultMaxAttempts) {
		t.Errorf("LLM called %d times, want %d", got, DefaultMaxAttempts)
	}
}

// TestChatWithRetry_DoesNotRetryOn4xx pins the deterministic-
// config-error path. A bad credential (401) or a missing model
// (404) won't fix itself on retry — surfacing them immediately
// saves wall-clock and makes the operator's debug loop tight.
func TestChatWithRetry_DoesNotRetryOn4xx(t *testing.T) {
	shortBackoffFor(t)
	for _, code := range []int{400, 401, 403, 404} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			llm := &retryFakeLLM{scripted: []retryScript{
				{err: httpErr(code)},
				{resp: okResp()}, // should never be reached
			}}
			_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
			if err == nil {
				t.Fatal("expected deterministic 4xx to surface immediately")
			}
			if got := atomic.LoadInt64(&llm.calls); got != 1 {
				t.Errorf("LLM called %d times for status %d, want 1 (no retry)", got, code)
			}
		})
	}
}

// TestChatWithRetry_DoesNotRetryOnParentContextCancel pins the
// signal-propagation contract. When the parent context is
// cancelled (the operator clicked Cancel, or a parent timeout
// fired), every in-flight Chat call gets ctx.Err(). Retrying
// would defeat the cancellation signal; we bail immediately
// with the original error.
func TestChatWithRetry_DoesNotRetryOnParentContextCancel(t *testing.T) {
	shortBackoffFor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel — the first Chat sees ctx.Canceled
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: context.Canceled},
		{resp: okResp()}, // should never be reached
	}}
	_, err := chatWithRetry(ctx, llm, gollm.ChatRequest{})
	if err == nil {
		t.Fatal("expected context cancellation to surface")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM called %d times after ctx cancel, want 1", got)
	}
}

// signalFakeLLM is a retryFakeLLM variant that publishes on a
// channel as soon as its first Chat call enters — gives tests a
// deterministic synchronisation point without depending on
// wall-clock sleep. Used by TestChatWithRetry_BackoffRespectsContextDeadline
// where the earlier `time.Sleep(2ms)` shape was a CI-jitter
// flake risk (Copilot review round 1).
type signalFakeLLM struct {
	calls   int64
	entered chan struct{} // closed after the first Chat() call enters
	err     error
}

func (f *signalFakeLLM) Chat(ctx context.Context, _ gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if atomic.AddInt64(&f.calls, 1) == 1 {
		close(f.entered)
	}
	return nil, f.err
}
func (f *signalFakeLLM) Validate(_ context.Context) error { return nil }

// TestChatWithRetry_BackoffRespectsContextDeadline pins the
// sleep-cancellation contract. A ctx that cancels DURING the
// retry backoff sleep must surface ctx.Err() promptly — without
// this the worker would wait out a 60s backoff on a run that's
// already been told to stop.
func TestChatWithRetry_BackoffRespectsContextDeadline(t *testing.T) {
	// Long enough backoff that the test would obviously hang
	// without the ctx-aware select.
	t.Setenv(LLMRetryBaseBackoffEnv, "5s")
	llm := &signalFakeLLM{entered: make(chan struct{}), err: httpErr(499)}
	ctx, cancel := context.WithCancel(context.Background())

	// Synchronised cancellation: wait until the first Chat()
	// call has actually entered the provider (so the retry loop
	// is about to start its backoff sleep), then cancel. No
	// sleep-based race window — the channel close is the
	// happens-before edge.
	go func() {
		<-llm.entered
		cancel()
	}()

	start := time.Now()
	_, err := chatWithRetry(ctx, llm, gollm.ChatRequest{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected ctx-cancelled retry to surface as error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	// Without the ctx-aware select on timer.C we'd wait the
	// full 5s backoff; the cancel should fire well within
	// that window. Generous upper bound keeps the test stable
	// on busy CI.
	if elapsed > time.Second {
		t.Errorf("backoff did not bail on ctx cancel; elapsed=%v", elapsed)
	}
}

// TestChatWithRetry_RetriesOnContextDeadlineExceeded covers the
// per-request HTTP timeout path. An individual call timing out
// against the provider's HTTP client (vertex sets ~5min by
// default; we layer LLM_TIMEOUT on top) is exactly the
// transient case the retry is for. We MUST distinguish it from
// parent-context cancellation, which bails immediately.
func TestChatWithRetry_RetriesOnContextDeadlineExceeded(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: context.DeadlineExceeded},
		{resp: okResp()},
	}}
	// Background ctx so the bail-on-parent-cancel branch doesn't
	// match.
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("DeadlineExceeded should be retryable; got %v", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 2 {
		t.Errorf("LLM called %d times, want 2", got)
	}
}

// TestChatWithRetry_OperationCancelledStringIsRetryable pins
// the Vertex audit-log shape. The audit log for the live failure
// surfaced as `"The operation was cancelled."` (gRPC code 1)
// even though the HTTP transport reported 499. Both shapes must
// reach the same retryable verdict.
func TestChatWithRetry_OperationCancelledStringIsRetryable(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: errors.New(`vertex-ai/google-native: {"error":{"code":1,"message":"The operation was cancelled.","status":"CANCELLED"}}`)},
		{resp: okResp()},
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("operation-cancelled string should be retryable; got %v", err)
	}
}

// TestChatWithRetry_RespectsMaxAttemptsEnv ensures operators can
// shrink or widen the retry budget without code changes — the
// flaky-upstream knob lives in env, not source.
func TestChatWithRetry_RespectsMaxAttemptsEnv(t *testing.T) {
	shortBackoffFor(t)
	t.Setenv(LLMRetryMaxAttemptsEnv, "1")
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: httpErr(499)},
		{resp: okResp()}, // should never be reached when max=1
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err == nil {
		t.Fatal("expected immediate surface with max_attempts=1")
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM called %d times, want 1 (max_attempts=1 disables retry)", got)
	}
}

// TestIsRetryableLLMError_Truthtable is the contract pin for the
// classifier — every retryability decision the loop relies on
// has its own row, so a future edit that flips a verdict
// (e.g. accidentally treating 401 as retryable) fails this test
// rather than silently changing production behaviour.
func TestIsRetryableLLMError_Truthtable(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context_canceled", context.Canceled, false},
		{"wrapped_context_canceled", fmt.Errorf("wrap: %w", context.Canceled), false},
		{"deadline_exceeded", context.DeadlineExceeded, true},
		{"status_499", httpErr(499), true},
		{"status_429", httpErr(429), true},
		{"status_500", httpErr(500), true},
		{"status_502", httpErr(502), true},
		{"status_503", httpErr(503), true},
		{"status_504", httpErr(504), true},
		{"status_400", httpErr(400), false},
		{"status_401", httpErr(401), false},
		{"status_403", httpErr(403), false},
		{"status_404", httpErr(404), false},
		{"status_409", httpErr(409), false},
		{"operation_cancelled_string", errors.New("operation was cancelled"), true},
		{"connection_reset", errors.New("connection reset by peer"), true},
		{"net_op_timeout", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}, true},
		{"io_eof", io.EOF, true},
		{"plain_unrelated", errors.New("invalid model id"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableLLMError(ctx, c.err); got != c.want {
				t.Errorf("isRetryableLLMError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestIsRetryableLLMError_ParentCtxCanceledOverridesRetryableErr
// pins a subtle invariant: even if the in-flight error LOOKS
// retryable (e.g. 499), we MUST honour parent-ctx cancellation
// instead of retrying. The retry would defeat the operator's
// stop signal.
func TestIsRetryableLLMError_ParentCtxCanceledOverridesRetryableErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The error itself is 499, which normally retries. The
	// parent ctx is cancelled — bail.
	if isRetryableLLMError(ctx, httpErr(499)) {
		t.Error("parent ctx cancel must override the retryable-error verdict")
	}
}

// TestIsRetryableLLMError_CrossProviderFormats pins detection
// across every error-string shape the in-tree providers
// actually emit. Codex round 1 caught the gap: the classifier
// originally only matched "status NNN" and missed OpenAI /
// Azure-foundry/openai / Vertex-openai-compat which format as
// "(NNN)", missed Claude's typed-error path which has no
// status at all, and missed AWS Bedrock's SDK exception names.
// Each row below is a real error string copied verbatim from
// the provider source files; if a provider rephrases its
// errors in the future, this test catches the regression.
func TestIsRetryableLLMError_CrossProviderFormats(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		// vertex-ai/google-native, vertex-ai/anthropic, claude,
		// azure-foundry/claude — "status NNN" form
		{"google_native_499", "vertex-ai/google-native: API error (status 499): cancelled", true},
		{"vertex_anthropic_503", "vertex-ai/anthropic: API error (status 503): overloaded", true},
		{"claude_http_429", "claude: API error (status 429): rate", true},
		{"azure_foundry_claude_500", "azure-foundry/claude: API error (status 500): boom", true},
		// openai, azure-foundry/openai, vertex-ai/openai-compat
		// — "(NNN)" form (NO "status" prefix)
		{"openai_429", "openai: API error (429): rate_limit_error - You exceeded your current quota", true},
		{"openai_500", "openai: API error (500): internal", true},
		{"openai_502", "openai: API error (502): bad gateway", true},
		{"openai_503", "openai: API error (503): service unavailable", true},
		{"azure_foundry_openai_429", "azure-foundry/openai: API error (429): RateLimit", true},
		{"vertex_openai_compat_500", "vertex-ai/openai-compat: API error (500): server", true},
		// Claude typed errors (no status code in the string). Full
		// transient list per docs.claude.com/en/api/errors.
		{"claude_rate_limit_typed", "claude: API error: rate_limit_error - Number of request tokens has exceeded your per-minute rate limit", true},
		{"claude_overloaded_typed", "claude: API error: overloaded_error - Anthropic is overloaded", true},
		{"claude_api_error_typed_500", "claude: API error: api_error - An unexpected error occurred", true},
		{"claude_timeout_error_typed_504", "claude: API error: timeout_error - The request timed out while processing", true},
		// Anthropic-specific status codes our list previously
		// missed (Anthropic Errors doc): 504 timeout and 529
		// overloaded.
		{"claude_http_504_status_form", "vertex-ai/anthropic: API error (status 504): processing timeout", true},
		{"claude_http_529_status_form", "vertex-ai/anthropic: API error (status 529): overloaded", true},
		// Bedrock SDK exception names per InvokeModel API
		// reference.
		{"bedrock_throttling", "bedrock/anthropic: InvokeModel failed: operation error Bedrock Runtime: InvokeModel, ThrottlingException: Too many requests", true},
		{"bedrock_model_not_ready", "bedrock/anthropic: InvokeModel failed: operation error Bedrock Runtime: InvokeModel, ModelNotReadyException: model warming up", true},
		{"bedrock_service_unavailable", "bedrock/anthropic: InvokeModel failed: operation error Bedrock Runtime: InvokeModel, ServiceUnavailableException: Try again", true},
		{"bedrock_internal_server", "bedrock/anthropic: InvokeModel failed: operation error Bedrock Runtime: InvokeModel, InternalServerException: Internal failure", true},
		{"bedrock_model_timeout", "bedrock/anthropic: InvokeModel failed: ModelTimeoutException: model took too long", true},
		{"bedrock_408_status_form", "bedrock/anthropic: API error (408): timeout", true},
		// Anthropic deterministic typed errors — must NOT
		// retry. Per docs: invalid_request_error,
		// authentication_error, billing_error, permission_error,
		// not_found_error, request_too_large.
		{"claude_invalid_request_typed", "claude: API error: invalid_request_error - Bad model id", false},
		{"claude_billing_error_typed", "claude: API error: billing_error - Payment required", false},
		{"claude_not_found_typed", "claude: API error: not_found_error - Model not found", false},
		{"claude_request_too_large_typed", "claude: API error: request_too_large - 32 MB exceeded", false},
		// Bedrock deterministic exceptions — must NOT retry.
		{"bedrock_access_denied", "bedrock/anthropic: AccessDeniedException: not authorised", false},
		{"bedrock_validation", "bedrock/anthropic: ValidationException: invalid contentType", false},
		{"bedrock_resource_not_found", "bedrock/anthropic: ResourceNotFoundException: model arn not found", false},
		// Deterministic config errors — must NOT match
		{"openai_400", "openai: API error (400): invalid_request_error - Invalid value for 'model'", false},
		{"openai_401", "openai: API error (401): invalid_api_key", false},
		{"google_native_403", "vertex-ai/google-native: API error (status 403): caller does not have permission", false},
		{"google_native_404", "vertex-ai/google-native: API error (status 404): model not found", false},
		{"claude_authentication", "claude: API error: authentication_error - invalid x-api-key", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableLLMError(ctx, errors.New(c.msg)); got != c.want {
				t.Errorf("isRetryableLLMError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

// TestChatWithRetry_RetriesOnOpenAIParenFormat is the worked
// example of Codex round 1's finding: an OpenAI 429 used to
// surface immediately (no "status NNN" in the error string) and
// the retry never engaged. Now it does.
func TestChatWithRetry_RetriesOnOpenAIParenFormat(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: errors.New("openai: API error (429): rate_limit_error - try again")},
		{resp: okResp()},
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("OpenAI 429 paren-format should retry; got %v", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 2 {
		t.Errorf("LLM called %d times, want 2 (recovered after retry)", got)
	}
}

// TestChatWithRetry_RetriesOnBedrockThrottling is the
// AWS-SDK-shaped variant of the same finding. Bedrock errors
// don't carry an HTTP status string at all; the typed exception
// name IS the signal.
func TestChatWithRetry_RetriesOnBedrockThrottling(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: errors.New("bedrock/anthropic: InvokeModel failed: operation error Bedrock Runtime: InvokeModel, ThrottlingException: Too many requests, please wait before trying again")},
		{resp: okResp()},
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("Bedrock throttling should retry; got %v", err)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 2 {
		t.Errorf("LLM called %d times, want 2 (recovered after retry)", got)
	}
}

// TestChatWithRetry_RetriesOnClaudeTypedRateLimit covers the
// Claude path where the response body is a parseable JSON error
// and the provider formats `"claude: API error: rate_limit_error - ..."`
// WITHOUT a status code. The Type field is the only signal.
func TestChatWithRetry_RetriesOnClaudeTypedRateLimit(t *testing.T) {
	shortBackoffFor(t)
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: errors.New("claude: API error: rate_limit_error - Number of request tokens has exceeded your per-minute rate limit")},
		{resp: okResp()},
	}}
	_, err := chatWithRetry(context.Background(), llm, gollm.ChatRequest{})
	if err != nil {
		t.Fatalf("Claude rate_limit_error should retry; got %v", err)
	}
}

// TestIsRetryableLLMError_ParentDeadlineExceededBails pins the
// subtle invariant Copilot caught in round 1: when the parent
// run context's deadline fires (parent timeout), Chat() returns
// `context.DeadlineExceeded`. The per-request HTTP-timeout case
// also surfaces as DeadlineExceeded but is retryable. The
// classifier MUST disambiguate by checking ctx.Err() first — a
// parent deadline means the operator (or a parent timeout) said
// stop; retrying defeats the signal.
func TestIsRetryableLLMError_ParentDeadlineExceededBails(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	// Even though the error itself is context.DeadlineExceeded
	// (which is normally retryable per the per-request-timeout
	// branch), the parent ctx has the same deadline-exceeded
	// state and that signal wins.
	if isRetryableLLMError(ctx, context.DeadlineExceeded) {
		t.Error("parent ctx deadline must override the per-request-DeadlineExceeded retry")
	}
}

// TestChatWithRetry_DoesNotRetryWhenParentDeadlineFires covers
// the integration of the above through the full retry loop.
func TestChatWithRetry_DoesNotRetryWhenParentDeadlineFires(t *testing.T) {
	shortBackoffFor(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	llm := &retryFakeLLM{scripted: []retryScript{
		{err: context.DeadlineExceeded},
		{resp: okResp()}, // would succeed but ctx is dead
	}}
	_, err := chatWithRetry(ctx, llm, gollm.ChatRequest{})
	if err == nil {
		t.Fatal("expected parent-deadline cancellation to surface")
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM called %d times when parent ctx is dead; want 1", got)
	}
}

// TestBackoffDuration_OverflowProtection pins the contract Copilot
// caught in round 1: large operator-supplied
// LLM_RETRY_MAX_ATTEMPTS × LLM_RETRY_BASE_BACKOFF combinations
// could overflow time.Duration with the original
// `base << (attempt - 1)` shape. Iterative doubling with a
// per-step cap check keeps the value sane regardless of how the
// operator misconfigures the knobs.
func TestBackoffDuration_OverflowProtection(t *testing.T) {
	// Pathological inputs: a generous base + a high attempt index
	// that would `<<` past int64.
	cases := []struct {
		name    string
		base    time.Duration
		attempt int
	}{
		{"high_attempt_default_base", 5 * time.Second, 100},
		{"large_base_high_attempt", time.Hour, 50},
		{"max_int_attempt", time.Second, 62},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := backoffDuration(c.base, c.attempt)
			if d < 0 {
				t.Errorf("overflow: backoff %v is negative", d)
			}
			if d > MaxBackoff {
				t.Errorf("backoff %v exceeded MaxBackoff %v", d, MaxBackoff)
			}
			if d <= 0 {
				t.Errorf("backoff %v must be positive (or the retry loop wouldn't sleep at all)", d)
			}
		})
	}
}

// TestBackoffDuration_FirstAttemptApproximatesBase confirms the
// 5s default does what the doc says — the first retry's wait is
// somewhere in the ±25% jitter window around base, not 2×base.
func TestBackoffDuration_FirstAttemptApproximatesBase(t *testing.T) {
	d := backoffDuration(5*time.Second, 1)
	if d < 3*time.Second || d > 6*time.Second {
		t.Errorf("first-attempt backoff %v outside expected 3-6s window", d)
	}
}

// TestBackoffDuration_RespectsCap pins the upper-bound contract.
// Attempt 6 with a 5s base would compute 160s without the cap;
// the MaxBackoff floor keeps us from accidentally introducing a
// minute-scale wait on a configurable base.
func TestBackoffDuration_RespectsCap(t *testing.T) {
	d := backoffDuration(5*time.Second, 6)
	if d > MaxBackoff {
		t.Errorf("backoff %v exceeded MaxBackoff %v", d, MaxBackoff)
	}
	// Account for the -25% jitter floor.
	if d < MaxBackoff*3/4 {
		t.Errorf("backoff %v should be near MaxBackoff %v (with jitter window)", d, MaxBackoff)
	}
}
