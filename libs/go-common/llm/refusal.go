package llm

import "fmt"

// RefusedError is the error a provider returns when the model declined the
// request (StopReasonRefusal). It matches ErrModelRefused under errors.Is and
// carries the call's usage: a refused call still bills its input tokens.
type RefusedError struct {
	Model string
	Usage Usage
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s (model %s)", ErrModelRefused, e.Model)
}

// Is makes errors.Is(err, ErrModelRefused) true for a RefusedError.
func (e *RefusedError) Is(target error) bool { return target == ErrModelRefused }

// RefusalError returns a *RefusedError when stopReason is StopReasonRefusal,
// and nil otherwise. Providers on the Anthropic wire call it on every reply:
// a refusal arrives as a successful response with no content, which a caller
// would otherwise read as an empty answer.
func RefusalError(stopReason, model string, usage Usage) error {
	if stopReason != StopReasonRefusal {
		return nil
	}
	return &RefusedError{Model: model, Usage: usage}
}
