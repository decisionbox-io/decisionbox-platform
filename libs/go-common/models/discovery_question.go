package models

import (
	"strings"
	"time"
	"unicode"
)

// DiscoveryQuestion is a single clarifying question the discovery agent
// generates at the end of a run when it was genuinely uncertain about
// something a business analyst could resolve — an opaque enum code, an
// ambiguous column, an unexplained anomaly, a finding the verifier could not
// confirm. The agent (writer, services/agent) inserts rows into
// CollectionDiscoveryQuestions; the enterprise API (reader) lists them and
// records the analyst's answer or a dismissal. Because the two live in
// different modules, the document type is defined here so their BSON tags
// cannot drift.
//
// Answering a question materializes a note into the knowledge base (enterprise
// side), which the next discovery run retrieves — turning one-off analyst
// knowledge into durable, reused context.
type DiscoveryQuestion struct {
	ID           string         `bson:"_id" json:"id"`
	ProjectID    string         `bson:"project_id" json:"project_id"`
	RunID        string         `bson:"run_id" json:"run_id"`
	DiscoveryID  string         `bson:"discovery_id" json:"discovery_id"`
	Question     string         `bson:"question" json:"question"`
	Rationale    string         `bson:"rationale" json:"rationale"`
	LinkedTarget QuestionTarget `bson:"linked_target" json:"linked_target"`

	// AnswerType is the simplest sufficient shape for the answer (see the
	// AnswerType* constants). Options is populated for the choice types.
	AnswerType string           `bson:"answer_type" json:"answer_type"`
	Options    []QuestionOption `bson:"options,omitempty" json:"options,omitempty"`

	Status string `bson:"status" json:"status"`

	// Answer is the canonical answer text — always populated when the question
	// is answered, regardless of AnswerType, so the knowledge-base note is
	// uniform ("Q: … / A: …"). AnswerOptionIDs / AnswerNote carry the typed
	// detail the API derives Answer from.
	Answer          string     `bson:"answer,omitempty" json:"answer,omitempty"`
	AnswerOptionIDs []string   `bson:"answer_option_ids,omitempty" json:"answer_option_ids,omitempty"`
	AnswerNote      string     `bson:"answer_note,omitempty" json:"answer_note,omitempty"`
	AnsweredBy      string     `bson:"answered_by,omitempty" json:"answered_by,omitempty"`
	AnsweredAt      *time.Time `bson:"answered_at,omitempty" json:"answered_at,omitempty"`
	// AnswerSourceID links the answer back to the knowledge-base note created
	// from it, for traceability and cleanup on dismiss/edit.
	AnswerSourceID string `bson:"answer_source_id,omitempty" json:"answer_source_id,omitempty"`

	// NormalizedKey is a stable fingerprint of (question text + linked target)
	// used to dedup across runs so an already-asked or already-answered
	// question is not raised again. Written by the agent; not part of the API
	// contract. Compute it with NormalizedQuestionKey.
	NormalizedKey string `bson:"normalized_key" json:"-"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// QuestionTarget links a question to the specific finding that raised it, so
// the dashboard can render "why we're asking" with a jump to the source.
type QuestionTarget struct {
	Type string `bson:"type" json:"type"` // one of the QuestionTarget* constants
	ID   string `bson:"id" json:"id"`     // insight/recommendation UUID, "dataset.table", or area id
}

// QuestionOption is one selectable answer for a single_choice / multi_choice
// question.
type QuestionOption struct {
	ID    string `bson:"id" json:"id"`
	Label string `bson:"label" json:"label"`
}

// DiscoveryQuestion lifecycle statuses.
const (
	DiscoveryQuestionStatusPending   = "pending"
	DiscoveryQuestionStatusAnswered  = "answered"
	DiscoveryQuestionStatusDismissed = "dismissed"
)

// Answer formats. The generator prefers the simplest sufficient type:
// boolean > single_choice/multi_choice > free_text.
const (
	AnswerTypeBoolean      = "boolean"
	AnswerTypeSingleChoice = "single_choice"
	AnswerTypeMultiChoice  = "multi_choice"
	AnswerTypeFreeText     = "free_text"
)

// Linked-target kinds.
const (
	QuestionTargetInsight        = "insight"
	QuestionTargetRecommendation = "recommendation"
	QuestionTargetTable          = "table"
	QuestionTargetArea           = "area"
)

// OtherOptionID is the reserved option id for the always-present
// "Other / add a note" escape on choice questions. Selecting it must reveal a
// free-text field so a wrong guess at the options can never trap the analyst.
const OtherOptionID = "__other"

// ValidAnswerType reports whether s is a known answer type.
func ValidAnswerType(s string) bool {
	switch s {
	case AnswerTypeBoolean, AnswerTypeSingleChoice, AnswerTypeMultiChoice, AnswerTypeFreeText:
		return true
	}
	return false
}

// ValidQuestionTargetType reports whether s is a known linked-target kind.
func ValidQuestionTargetType(s string) bool {
	switch s {
	case QuestionTargetInsight, QuestionTargetRecommendation, QuestionTargetTable, QuestionTargetArea:
		return true
	}
	return false
}

// NormalizedQuestionKey builds the dedup fingerprint for a question: the
// normalized question text joined with the linked target. Normalization
// lower-cases, collapses runs of whitespace, and drops punctuation so trivial
// rewordings ("closed?" vs "closed") collide. Deterministic and allocation-light.
func NormalizedQuestionKey(question string, target QuestionTarget) string {
	var b strings.Builder
	b.Grow(len(question))
	prevSpace := false
	for _, r := range strings.ToLower(question) {
		switch {
		case unicode.IsSpace(r):
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		default:
			// drop punctuation
		}
	}
	norm := strings.TrimRight(b.String(), " ")
	return norm + "|" + target.Type + ":" + target.ID
}
