package llm

import "fmt"

// RefusalError returns an error wrapping ErrModelRefused when stopReason is
// StopReasonRefusal, and nil otherwise. Providers on the Anthropic wire call
// it on every reply: a refusal arrives as a successful response with no
// content, which a caller would otherwise read as an empty answer.
func RefusalError(stopReason, model string) error {
	if stopReason != StopReasonRefusal {
		return nil
	}
	return fmt.Errorf("%w (model %s)", ErrModelRefused, model)
}
