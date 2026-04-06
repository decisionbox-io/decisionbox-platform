package models

import "time"

// Feedback represents a user's rating on an insight or recommendation.
type Feedback struct {
	ID          string    `bson:"_id,omitempty" json:"id"`
	ProjectID   string    `bson:"project_id" json:"project_id"`
	DiscoveryID string    `bson:"discovery_id" json:"discovery_id"`
	TargetType  string    `bson:"target_type" json:"target_type"`   // "insight" | "recommendation"
	TargetID    string    `bson:"target_id" json:"target_id"`       // insight id/index or recommendation index
	Rating      string    `bson:"rating" json:"rating"`             // "like" | "dislike"
	Comment     string    `bson:"comment,omitempty" json:"comment,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
}
