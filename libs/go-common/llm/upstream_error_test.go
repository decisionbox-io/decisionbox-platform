package llm

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// awsLikeError stands in for a transport error that carries its own status
// — the AWS SDK's response error shape. The status is read through an
// interface assertion, so no SDK import is needed to exercise it.
type awsLikeError struct {
	status int
	code   string
}

func (e awsLikeError) Error() string       { return e.code + ": the request was rejected" }
func (e awsLikeError) HTTPStatusCode() int { return e.status }
func (e awsLikeError) ErrorCode() string   { return e.code }

func TestUpstreamError_ClientFault(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusUnprocessableEntity, true},
		// The two 4xx that say "ask again", not "you were wrong". Reading
		// them as the caller's mistake would stop a retry that works.
		{http.StatusTooManyRequests, false},
		{http.StatusRequestTimeout, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
		// Unknown is not a client fault: a provider that has not adopted
		// this cannot have its failures reclassified.
		{0, false},
	} {
		e := &UpstreamError{Provider: "p", Status: tc.status, Message: "m"}
		if got := e.ClientFault(); got != tc.want {
			t.Errorf("status %d: ClientFault = %v, want %v", tc.status, got, tc.want)
		}
	}
	if (*UpstreamError)(nil).ClientFault() {
		t.Error("a nil UpstreamError must not be a client fault")
	}
}

// The helpers walk a wrapped chain, because every provider adds its own
// context with %w before the error reaches a caller.
func TestUpstreamStatus_WalksTheChain(t *testing.T) {
	inner := &UpstreamError{Provider: "litellm", Status: 400, Message: "context length exceeded"}
	wrapped := fmt.Errorf("gateway: chat failed: %w", fmt.Errorf("litellm: %w", inner))

	if got := UpstreamStatus(wrapped); got != 400 {
		t.Errorf("UpstreamStatus = %d, want 400", got)
	}
	if !UpstreamClientFault(wrapped) {
		t.Error("UpstreamClientFault = false, want true")
	}
	if got := UpstreamStatus(errors.New("something else")); got != 0 {
		t.Errorf("an unrelated error reported status %d, want 0", got)
	}
	if UpstreamClientFault(nil) {
		t.Error("nil must not be a client fault")
	}
}

// A transport error that exposes its own status — the AWS SDK shape — is
// read without importing the SDK.
func TestWrapUpstream_TakesTheStatusFromTheError(t *testing.T) {
	err := WrapUpstream("bedrock", awsLikeError{status: 400, code: "ValidationException"})

	if got := UpstreamStatus(err); got != 400 {
		t.Errorf("UpstreamStatus = %d, want 400", got)
	}
	if !UpstreamClientFault(err) {
		t.Error("a ValidationException must read as a client fault")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatal("WrapUpstream did not produce an UpstreamError")
	}
	if ue.Code != "ValidationException" {
		t.Errorf("Code = %q, want ValidationException", ue.Code)
	}
	if !errors.Is(err, err) || errors.Unwrap(ue) == nil {
		t.Error("the underlying error must stay reachable")
	}
}

func TestWrapUpstream_StatuslessErrorStaysUnknown(t *testing.T) {
	err := WrapUpstream("bedrock", errors.New("dial tcp: i/o timeout"))
	if got := UpstreamStatus(err); got != 0 {
		t.Errorf("UpstreamStatus = %d, want 0 — a transport failure has no upstream status", got)
	}
	if UpstreamClientFault(err) {
		t.Error("a transport failure is not the caller's fault")
	}
	if WrapUpstream("bedrock", nil) != nil {
		t.Error("WrapUpstream(nil) must be nil")
	}
}

// A credential echoed in an upstream body must not reach a caller's logs
// through this type, which is the one string on these paths the gateway did
// not compose.
func TestUpstreamError_MessageIsSanitised(t *testing.T) {
	e := NewUpstreamError("litellm", 401, "authentication_error",
		[]byte(`{"error":"invalid key sk-abcdef0123456789"}`))
	if got := e.Error(); contains(got, "sk-abcdef0123456789") {
		t.Errorf("the credential survived into %q", got)
	}
	wrapped := WrapUpstream("litellm", errors.New("Authorization: Bearer sk-abcdef0123456789 rejected"))
	if got := wrapped.Error(); contains(got, "sk-abcdef0123456789") {
		t.Errorf("the credential survived into %q", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
