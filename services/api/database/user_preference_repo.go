package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// UserPreferenceRepository stores per-user dashboard preferences, one document
// per principal, keyed by the OIDC subject (Sub). Under NoAuth every request is
// the same "anonymous" principal, so a community deployment has a single row.
type UserPreferenceRepository struct {
	col *mongo.Collection
}

func NewUserPreferenceRepository(db *DB) *UserPreferenceRepository {
	return &UserPreferenceRepository{col: db.Collection("user_preferences")}
}

// Get returns the stored preferences for a principal, or nil when none exist
// (the caller then falls back to Accept-Language / the default locale).
func (r *UserPreferenceRepository) Get(ctx context.Context, sub string) (*models.UserPreference, error) {
	var pref models.UserPreference
	err := r.col.FindOne(ctx, bson.M{"sub": sub}).Decode(&pref)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user preference: %w", err)
	}
	return &pref, nil
}

// SetLocale upserts the UI locale for a principal. Keyed by sub (unique index),
// so repeated calls update in place instead of creating duplicates.
func (r *UserPreferenceRepository) SetLocale(ctx context.Context, sub, locale string) error {
	update := bson.M{
		"$set": bson.M{
			"sub":        sub,
			"locale":     locale,
			"updated_at": time.Now().UTC(),
		},
	}
	opts := options.Update().SetUpsert(true)
	if _, err := r.col.UpdateOne(ctx, bson.M{"sub": sub}, update, opts); err != nil {
		return fmt.Errorf("set user locale: %w", err)
	}
	return nil
}
