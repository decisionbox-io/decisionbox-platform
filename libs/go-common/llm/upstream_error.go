package llm

import (
	"errors"
	"fmt"
	"net/http"
)

// UpstreamError reports a failure the upstream itself returned, carrying
// the status it used.
//
// It exists so a caller can tell a REJECTED REQUEST from a BROKEN BACKEND.
// Provider.Chat returns a plain error, so every failure has looked the same
// to a caller for as long as the interface has existed: a prompt longer than
// the model's window, a parameter the model does not take and an upstream
// outage all arrive as "the provider failed". A caller that cannot tell them
// apart retries the ones that will never succeed and reports the user's own
// mistake as our fault — which is what a managed gateway in front of this
// does today, answering 502 to a request the backend called invalid
// (decisionbox-io/decisionbox-ai-gateway#41).
//
// Message is already sanitised: an upstream that echoes request headers in
// its error body must not put a bearer token into a caller's logs.
type UpstreamError struct {
	// Provider is the registered provider name, for the message.
	Provider string
	// Status is the HTTP status the upstream returned, or 0 when the
	// transport exposes none.
	Status int
	// Code is the upstream's own classification where it gives one
	// ("invalid_request_error", "ValidationException"), else empty.
	Code string
	// Message is the sanitised upstream message.
	Message string
	// Err is the underlying transport error, when there was one.
	Err error
}

func (e *UpstreamError) Error() string {
	switch {
	case e.Status != 0 && e.Code != "":
		return fmt.Sprintf("%s: upstream error (%d %s): %s", e.Provider, e.Status, e.Code, e.Message)
	case e.Status != 0:
		return fmt.Sprintf("%s: upstream error (%d): %s", e.Provider, e.Status, e.Message)
	case e.Code != "":
		return fmt.Sprintf("%s: upstream error (%s): %s", e.Provider, e.Code, e.Message)
	default:
		return fmt.Sprintf("%s: upstream error: %s", e.Provider, e.Message)
	}
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// ClientFault reports whether the upstream blamed the REQUEST rather than
// itself — a 4xx that is not a rate limit.
//
// 429 is excluded on purpose: it is the one 4xx that says "ask again
// later", so treating it as the caller's mistake would stop a retry that
// would have worked. 408 likewise.
func (e *UpstreamError) ClientFault() bool {
	if e == nil || e.Status < 400 || e.Status > 499 {
		return false
	}
	switch e.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return true
}

// NewUpstreamError builds an UpstreamError from a response a provider read
// itself. body is sanitised here, so callers pass it raw.
func NewUpstreamError(provider string, status int, code string, body []byte) *UpstreamError {
	return &UpstreamError{
		Provider: provider,
		Status:   status,
		Code:     code,
		Message:  SanitizeErrorBody(body, 500),
	}
}

// WrapUpstream turns a transport-level failure into an UpstreamError,
// taking the HTTP status from the error itself when it exposes one.
//
// The status is read through an interface assertion rather than a
// dependency: the AWS SDK's response error, and several other transports,
// carry `HTTPStatusCode() int`. So a Bedrock ValidationException arrives
// with its 400 and no SDK import is needed here. An error that exposes
// nothing keeps Status 0, which callers must read as "unknown", never as a
// success or a client fault.
func WrapUpstream(provider string, err error) error {
	if err == nil {
		return nil
	}
	var withStatus interface{ HTTPStatusCode() int }
	status := 0
	if errors.As(err, &withStatus) {
		status = withStatus.HTTPStatusCode()
	}
	var withCode interface{ ErrorCode() string }
	code := ""
	if errors.As(err, &withCode) {
		code = withCode.ErrorCode()
	}
	return &UpstreamError{
		Provider: provider,
		Status:   status,
		Code:     code,
		Message:  SanitizeErrorBody([]byte(err.Error()), 500),
		Err:      err,
	}
}

// UpstreamStatus returns the status an upstream reported anywhere in err's
// chain, or 0 when nothing in it is an UpstreamError with a status.
//
// Zero means "unknown", not "fine": a provider that has not adopted
// UpstreamError yet reports 0, and a caller must keep whatever default it
// had for that case rather than inventing a status.
func UpstreamStatus(err error) int {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.Status
	}
	return 0
}

// UpstreamClientFault reports whether anything in err's chain is an
// upstream rejection of the request itself. False for an unknown status, so
// a provider that has not adopted UpstreamError cannot have its failures
// reclassified as the caller's fault.
func UpstreamClientFault(err error) bool {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.ClientFault()
	}
	return false
}
