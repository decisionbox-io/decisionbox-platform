package models

import "time"

// ReadMark records that a user has read an insight or recommendation.
// Scoped by (project_id, user_id, target_type, target_id) — uniquely keyed,
// so upserting with the same key refreshes ReadAt instead of duplicating.
type ReadMark struct {
	ID         string    `bson:"_id,omitempty" json:"id"`
	ProjectID  string    `bson:"project_id" json:"project_id"`
	UserID     string    `bson:"user_id" json:"user_id"`
	TargetType string    `bson:"target_type" json:"target_type"`
	TargetID   string    `bson:"target_id" json:"target_id"`
	ReadAt     time.Time `bson:"read_at" json:"read_at"`
}
