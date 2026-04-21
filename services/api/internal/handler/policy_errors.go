package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
)

// writePolicyError translates a *policy.PolicyError into the structured
// JSON body the dashboard renders as "Upgrade to …" UX. If the error is
// not a PolicyError, returns false so the caller can fall back to its
// own generic handling. Limit denials become HTTP 402, feature denials
// become HTTP 403.
func writePolicyError(w http.ResponseWriter, err error) bool {
	var pe *policy.PolicyError
	if !errors.As(err, &pe) {
		return false
	}

	status := http.StatusForbidden
	if pe.IsLimit() {
		status = http.StatusPaymentRequired
	}

	body := map[string]any{
		"error": pe.Error(),
		"code":  pe.Kind,
	}
	if pe.Limit != "" {
		body["limit"] = pe.Limit
	}
	if pe.Feature != "" {
		body["feature"] = pe.Feature
	}
	if pe.Max > 0 {
		body["current"] = pe.Current
		body["max"] = pe.Max
	}
	if pe.PlanID != "" {
		body["plan_id"] = pe.PlanID
	}
	if len(pe.Allowed) > 0 {
		body["allowed"] = pe.Allowed
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Error: pe.Error(), Data: body})
	return true
}
