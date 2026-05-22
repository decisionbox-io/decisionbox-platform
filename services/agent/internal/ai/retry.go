package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// LLM Chat retry on transient upstream failures.
//
// Why this exists: Vertex AI's shared serving tier — and any
// multi-tenant LLM upstream — transiently cancels in-flight
// requests (HTTP 499 / gRPC code 1 CANCELLED) when the scheduler
// decides the tenant should yield. The discovery agent's
// exploration loop dispatches dozens of Chat calls per run; a
// single transient cancellation used to kill the entire run.
// The blurb generator (PR #227) added a per-table retry; the
// discovery exploration loop did not get one, so a single 499
// at step 20 of an otherwise-healthy run still aborted everything.
//
// What it does: wraps `gollm.Provider.Chat` in a bounded
// exponential backoff with jitter. Only transient classes retry;
// deterministic 4xx (other than 429) and parent-context
// cancellation bail immediately.

// DefaultMaxAttempts caps the total number of Chat calls
// (initial + retries). 3 means up to two retries — enough to
// absorb the rare transient on shared serving without burning
// wall-clock on a permanently-broken upstream.
const DefaultMaxAttempts = 3

// DefaultBaseBackoff is the first retry's delay. Exponential
// scaling thereafter: 5s, 15s. Long enough that a server-side
// queue overload has time to drain; short enough that we don't
// add real wall-clock to an exploration run that mostly succeeds.
const DefaultBaseBackoff = 5 * time.Second

// MaxBackoff caps the per-retry sleep. Without a cap the
// exponential could grow past the discovery context deadline
// on cohorts with deep retry chains.
const MaxBackoff = 60 * time.Second

// LLMRetryMaxAttemptsEnv is the env var operators flip to widen
// or shrink the retry budget. Set to 1 to disable retries
// entirely (initial attempt only); set higher for flaky upstreams.
const LLMRetryMaxAttemptsEnv = "LLM_RETRY_MAX_ATTEMPTS"

// LLMRetryBaseBackoffEnv is the env var that overrides the first
// retry's delay. Accepts any Go-duration string ("3s", "10s",
// "30s"). Default DefaultBaseBackoff.
const LLMRetryBaseBackoffEnv = "LLM_RETRY_BASE_BACKOFF"

// chatWithRetry executes provider.Chat with bounded exponential
// backoff on transient errors. The caller still owns context
// cancellation: if the parent context is cancelled the retry
// loop bails immediately with ctx.Err() rather than waiting out
// the backoff.
//
// The function does not log token usage or any other Chat-success
// observability — the caller does that on the returned response.
// It DOES log every retry attempt with the underlying error so
// operators can see in the agent log why a run took longer than
// expected.
func chatWithRetry(ctx context.Context, provider gollm.Provider, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	maxAttempts := config.GetEnvAsInt(LLMRetryMaxAttemptsEnv, DefaultMaxAttempts)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	baseBackoff := DefaultBaseBackoff
	if raw := config.GetEnvOrDefault(LLMRetryBaseBackoffEnv, ""); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			baseBackoff = parsed
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := provider.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !isRetryableLLMError(ctx, err) {
			return nil, err
		}
		if attempt == maxAttempts {
			break
		}

		backoff := backoffDuration(baseBackoff, attempt)
		logger.WithFields(logger.Fields{
			"attempt":     attempt,
			"max":         maxAttempts,
			"backoff_ms":  backoff.Milliseconds(),
			"error":       err.Error(),
		}).Warn("LLM call failed with transient error; retrying")

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	return nil, fmt.Errorf("llm: exhausted %d retry attempts: %w", maxAttempts, lastErr)
}

// backoffDuration returns the sleep for attempt N (1-indexed).
// Exponential: base * 2^(N-1), with ±25% jitter, with the
// FINAL result clamped to MaxBackoff. Jitter prevents
// thundering-herd reconnects when multiple discovery runs
// share a single upstream throttle event.
//
// Implementation note: the cap is applied AFTER jitter so the
// 25%-upward jitter excursion can't push the value past
// MaxBackoff. An earlier version clamped d pre-jitter and the
// `d - d/4 + jitter` shape allowed up to `1.25 * MaxBackoff`
// to leak through — pinned by TestBackoffDuration_RespectsCap.
func backoffDuration(base time.Duration, attempt int) time.Duration {
	d := base << (attempt - 1) // base * 2^(attempt-1)
	// Jitter: ±25%. Deterministic seeding not desired — the goal
	// is anti-synchronisation across concurrent agents.
	jitter := time.Duration(rand.Int63n(int64(d) / 2)) //nolint:gosec // jitter, not crypto
	result := d - d/4 + jitter
	if result > MaxBackoff {
		result = MaxBackoff
	}
	return result
}

// isRetryableLLMError classifies an error from gollm.Provider.Chat
// into retryable or terminal. The split is conservative:
//
//   - Parent context cancelled (errors.Is(err, context.Canceled)):
//     NEVER retry — the operator or a parent timeout asked us to
//     stop, and retrying would defeat that signal.
//
//   - context.DeadlineExceeded: retry. Per-request HTTP timeouts
//     are tracked separately from the parent run context; an
//     individual call hitting our HTTP deadline is exactly the
//     transient case retry is for.
//
//   - Network errors (net.Error, io.EOF, broken pipe, connection
//     reset, DNS): retry. These are the canonical transient
//     classes.
//
//   - HTTP 499 (CANCELLED): retry. The motivating case — Vertex
//     AI shared serving cancels in-flight requests when the
//     scheduler decides the tenant should yield.
//
//   - HTTP 429 (Too Many Requests): retry. Standard rate limit;
//     the backoff handles the burst correctly.
//
//   - HTTP 5xx: retry. Server-side problem, possibly transient.
//
//   - HTTP 4xx (other than 429): terminal. 400 / 401 / 403 / 404
//     are deterministic configuration errors; retrying just
//     burns time. Operator action required.
//
// The status-code detection uses substring matching on the
// formatted error string because gollm.Provider doesn't surface
// typed HTTP errors at the interface boundary. Centralising it
// here keeps the substring patterns in ONE place — a future
// gollm typed-error refactor only touches this function.
func isRetryableLLMError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}

	// Parent context cancellation always bails — propagating the
	// signal is more important than the retry budget.
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Per-request HTTP timeout: classic transient case.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Network classes — net.Error.Timeout(), EOF, connection
	// reset, broken pipe, DNS resolution failure. errors.As
	// catches wrapped versions.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	msg := err.Error()
	lower := strings.ToLower(msg)
	for _, hint := range networkErrorHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}

	// HTTP status codes surface via the provider error string
	// (gollm doesn't have typed HTTP errors at the interface
	// boundary today). Match by substring against every shape
	// the in-tree providers actually format. As of this PR:
	//   - vertex-ai/google-native, vertex-ai/anthropic,
	//     azure-foundry/claude, claude:     "API error (status NNN):"
	//   - openai, azure-foundry/openai,
	//     vertex-ai/openai-compat:          "API error (NNN):"
	// Bedrock takes a different path entirely (AWS SDK typed
	// exceptions), handled below.
	for _, code := range retryableStatusCodes {
		if strings.Contains(msg, "status "+code) || strings.Contains(msg, "("+code+")") {
			return true
		}
	}

	// Provider-typed error names for transient classes. None of
	// these include a numeric HTTP status in the error string,
	// so the status-code branch above misses them on its own.
	for _, hint := range retryableProviderHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}

	return false
}

// retryableStatusCodes lists the HTTP statuses we re-issue on.
// 499 is the Vertex AI shared-serving cancellation case;
// 429 is the standard rate-limit code; 5xx are server-side
// failures. Anything else is treated as deterministic and
// surfaced to the caller without retry.
// retryableStatusCodes lists every HTTP status the in-tree
// providers actually return for transient failure. Sources:
// Anthropic Errors doc (529 overloaded, 504 timeout), AWS
// Bedrock InvokeModel API reference (429 ThrottlingException,
// 429 ModelNotReadyException, 408 ModelTimeoutException, 500
// InternalServerException, 503 ServiceUnavailableException),
// OpenAI Python SDK exceptions (429 RateLimitError, 5xx
// InternalServerError), Vertex AI google-native (499 CANCELLED
// shared-serving yield).
var retryableStatusCodes = []string{
	"408", "429", "499",
	"500", "502", "503", "504", "529",
}

// networkErrorHints matches transient network failures whose
// errors arrive as plain `errors.New(...)` strings rather than
// net.Error implementations. The lowercase prefix list keeps
// the check cheap on the hot path.
var networkErrorHints = []string{
	"connection reset",
	"broken pipe",
	"connection refused",
	"no such host",
	"i/o timeout",
	"server closed idle connection",
	"unexpected eof",
}

// retryableProviderHints catches transient classes whose error
// strings don't carry a numeric HTTP status:
//
//   - "operation was cancelled" — Vertex's gRPC-code-1
//     audit-log shape (HTTP 499 transport, but the body's
//     status field reads "CANCELLED"). Surfaced by
//     google-native's response body parse.
//   - Anthropic Claude typed errors. When the response body is
//     a parseable JSON `{"error":{"type":"rate_limit_error",...}}`
//     the provider formats `"claude: API error: rate_limit_error - ..."`
//     with NO status code. The Type field IS the signal. Full
//     transient-class list per Anthropic Errors doc:
//     rate_limit_error (429), api_error (500), timeout_error
//     (504), overloaded_error (529).
//   - AWS Bedrock SDK exception names. Bedrock errors arrive as
//     wrapped smithy.OperationError values whose String() form
//     embeds the AWS exception type. Per InvokeModel API
//     reference: ThrottlingException (429),
//     ModelNotReadyException (429 — docs explicitly mark
//     SDK-retryable), ServiceUnavailableException (503),
//     InternalServerException (500), ModelTimeoutException
//     (408). ModelStreamErrorException covers
//     InvokeModelWithResponseStream which we don't currently
//     use but include for forward compatibility.
//
// All matched lowercased to keep the check case-insensitive.
var retryableProviderHints = []string{
	// Cross-provider: cancellation
	"operation was cancelled",
	// Anthropic typed errors (no status code in the formatted
	// string; the Type field is the only signal)
	"rate_limit_error",
	"overloaded_error",
	"api_error",
	"timeout_error",
	// AWS Bedrock SDK exception names
	"throttlingexception",
	"modelnotreadyexception",
	"serviceunavailableexception",
	"internalserverexception",
	"modeltimeoutexception",
	"modelstreamerrorexception",
}
