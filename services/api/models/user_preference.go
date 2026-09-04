package models

import "time"

// UserPreference stores a single user's dashboard preferences, keyed by the
// authenticated principal's subject (Sub). One document per user — upserting by
// Sub updates in place.
//
// The UI locale here is independent of a project's analysis Language: this only
// controls the dashboard chrome, never what the agent generates.
type UserPreference struct {
	ID        string    `bson:"_id,omitempty" json:"-"`
	Sub       string    `bson:"sub" json:"-"`
	Locale    string    `bson:"locale,omitempty" json:"locale"`
	UpdatedAt time.Time `bson:"updated_at" json:"-"`
}
