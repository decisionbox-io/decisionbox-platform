package validation

import "time"

// InsightValidation is the doc-attached validation summary persisted
// on insights and recommendations. Plan §"Models updates summary"
// requires this struct to live here so all three model packages
// (services/agent/internal/models, services/api/models,
// libs/go-common/models) reference the same definition.
//
// The struct keeps the legacy v0.x fields (Status, VerifiedCount,
// OriginalCount, Reasoning, Query, ValidatedAt, InputTokens,
// OutputTokens) so MongoDB documents written before this plan still
// decode cleanly. They are all `omitempty` — new validation runs do
// NOT populate them; the dashboard prefers the new fields when present
// and falls back to the legacy ones for old docs.
type InsightValidation struct {
	// --- legacy fields (pre-plan, kept for read compatibility) ---

	Status        string    `bson:"status,omitempty"          json:"status,omitempty"`
	VerifiedCount int       `bson:"verified_count,omitempty"  json:"verified_count,omitempty"`
	OriginalCount int       `bson:"original_count,omitempty"  json:"original_count,omitempty"`
	Query         string    `bson:"query,omitempty"           json:"query,omitempty"`
	Reasoning     string    `bson:"reasoning,omitempty"       json:"reasoning,omitempty"`
	ValidatedAt   time.Time `bson:"validated_at"              json:"validated_at"`
	InputTokens   int       `bson:"input_tokens,omitempty"    json:"input_tokens,omitempty"`
	OutputTokens  int       `bson:"output_tokens,omitempty"   json:"output_tokens,omitempty"`

	// --- new fields (plan v5) ---

	// Verifier is the structured verdict from the defender agent.
	Verifier *StructuredVerdict `bson:"verifier,omitempty" json:"verifier,omitempty"`

	// Refuter is the structured verdict from the skeptic agent.
	// Nil when RefuterDisabled is true OR when the refuter run
	// failed at the transport layer.
	Refuter *StructuredVerdict `bson:"refuter,omitempty" json:"refuter,omitempty"`

	// Combined is the merge of Verifier and Refuter via the 7-status
	// matrix. This is the status the dashboard sorts and filters by.
	Combined Status `bson:"combined,omitempty" json:"combined,omitempty"`

	// RefuterDisabled distinguishes "refuter intentionally not run"
	// from "refuter expected but missing". When true, Combine treats
	// the refuter side as confirming — verifier=supported stays
	// supported, not partial. The dashboard renders a "refuter off"
	// pill when this is set.
	RefuterDisabled bool `bson:"refuter_disabled,omitempty" json:"refuter_disabled,omitempty"`
}
