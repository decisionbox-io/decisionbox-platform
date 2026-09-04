package models

import "time"

// PackProposal is a proposed domain-pack (analysis-area) delta the reflection
// phase emits and the enterprise evolution workflow governs (enterprise#261).
// The agent (writer) inserts proposals with status "proposed"; the enterprise
// API (reader/updater) lists them and — per the project's evolution_mode —
// approves/rejects/applies/reverts them, mutating the per-project
// Project.Prompts.AnalysisAreas copy. The shared type lives here so the two
// modules cannot drift on its BSON tags.
type PackProposal struct {
	ID                string   `bson:"_id" json:"id"`
	ProjectID         string   `bson:"project_id" json:"project_id"`
	Action            string   `bson:"action" json:"action"`
	AreaID            string   `bson:"area_id" json:"area_id"`
	AreaName          string   `bson:"area_name,omitempty" json:"area_name,omitempty"`
	Prompt            string   `bson:"prompt,omitempty" json:"prompt,omitempty"`
	Keywords          []string `bson:"keywords,omitempty" json:"keywords,omitempty"`
	Rationale         string   `bson:"rationale" json:"rationale"`
	Status            string   `bson:"status" json:"status"`
	CreatedRunID      string   `bson:"created_run_id,omitempty" json:"created_run_id,omitempty"`
	SourceDiscoveryID string   `bson:"source_discovery_id,omitempty" json:"source_discovery_id,omitempty"`

	// Before is the snapshot of the affected area captured at apply time, so a
	// revert restores the exact prior state. Nil until applied. Written by the
	// enterprise apply path, never by the agent.
	Before *AnalysisAreaSnapshot `bson:"before,omitempty" json:"before,omitempty"`

	// Audit fields, stamped by the enterprise workflow.
	DecidedBy string     `bson:"decided_by,omitempty" json:"decided_by,omitempty"`
	DecidedAt *time.Time `bson:"decided_at,omitempty" json:"decided_at,omitempty"`
	AppliedAt *time.Time `bson:"applied_at,omitempty" json:"applied_at,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// AnalysisAreaSnapshot captures the fields of a per-project analysis area a
// pack delta can change, so an applied proposal can be reverted exactly. Whether
// the area existed distinguishes an add (revert = remove) from an edit (revert =
// restore fields).
type AnalysisAreaSnapshot struct {
	Existed  bool     `bson:"existed" json:"existed"`
	Prompt   string   `bson:"prompt" json:"prompt"`
	Keywords []string `bson:"keywords,omitempty" json:"keywords,omitempty"`
	Enabled  bool     `bson:"enabled" json:"enabled"`
	Name     string   `bson:"name,omitempty" json:"name,omitempty"`
}

// Pack-delta actions.
const (
	PackDeltaAddArea     = "add_area"
	PackDeltaEditArea    = "edit_area"
	PackDeltaDisableArea = "disable_area"
	PackDeltaEnableArea  = "enable_area"
)

// PackProposal lifecycle statuses.
const (
	PackProposalStatusProposed = "proposed"
	PackProposalStatusApproved = "approved"
	PackProposalStatusRejected = "rejected"
	PackProposalStatusApplied  = "applied"
	PackProposalStatusReverted = "reverted"
)

// ValidPackDeltaAction reports whether a is a known delta action.
func ValidPackDeltaAction(a string) bool {
	switch a {
	case PackDeltaAddArea, PackDeltaEditArea, PackDeltaDisableArea, PackDeltaEnableArea:
		return true
	}
	return false
}
