package models

import "time"

// Valid target types for bookmarks and read marks.
const (
	TargetTypeInsight        = "insight"
	TargetTypeRecommendation = "recommendation"
)

// IsValidTargetType reports whether t is a supported target type.
func IsValidTargetType(t string) bool {
	return t == TargetTypeInsight || t == TargetTypeRecommendation
}

// BookmarkList is a named collection of bookmarked insights and recommendations.
// Scoped by (project_id, user_id): in community (NoAuth) user_id is "anonymous",
// in enterprise (OIDC) user_id is the authenticated principal's sub claim.
type BookmarkList struct {
	ID          string    `bson:"_id,omitempty" json:"id"`
	ProjectID   string    `bson:"project_id" json:"project_id"`
	UserID      string    `bson:"user_id" json:"user_id"`
	Name        string    `bson:"name" json:"name"`
	Description string    `bson:"description,omitempty" json:"description,omitempty"`
	Color       string    `bson:"color,omitempty" json:"color,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at" json:"updated_at"`
	ItemCount   int64     `bson:"-" json:"item_count"`
}

// Bookmark is an entry in a BookmarkList referencing an insight or recommendation.
type Bookmark struct {
	ID          string    `bson:"_id,omitempty" json:"id"`
	ListID      string    `bson:"list_id" json:"list_id"`
	ProjectID   string    `bson:"project_id" json:"project_id"`
	UserID      string    `bson:"user_id" json:"user_id"`
	DiscoveryID string    `bson:"discovery_id" json:"discovery_id"`
	TargetType  string    `bson:"target_type" json:"target_type"`
	TargetID    string    `bson:"target_id" json:"target_id"`
	Note        string    `bson:"note,omitempty" json:"note,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
}

// BookmarkItem is a resolved bookmark returned by list-detail endpoints.
// The target fields come from the underlying insight or recommendation document,
// or are nil with Deleted=true when the source has been removed.
type BookmarkItem struct {
	Bookmark *Bookmark   `json:"bookmark"`
	Target   interface{} `json:"target,omitempty"`
	Deleted  bool        `json:"deleted,omitempty"`
}
