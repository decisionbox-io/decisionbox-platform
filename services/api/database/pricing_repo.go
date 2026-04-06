package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// PricingRepository manages pricing data in MongoDB.
type PricingRepository struct {
	col *mongo.Collection
}

func NewPricingRepository(db *DB) *PricingRepository {
	return &PricingRepository{col: db.Collection("pricing")}
}

// Get returns the current pricing. Returns nil if not seeded yet.
func (r *PricingRepository) Get(ctx context.Context) (*models.Pricing, error) {
	var pricing models.Pricing
	err := r.col.FindOne(ctx, bson.M{}).Decode(&pricing)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("get pricing: %w", err)
	}
	return &pricing, nil
}

// Save upserts the pricing document (single document collection).
func (r *PricingRepository) Save(ctx context.Context, pricing *models.Pricing) error {
	pricing.UpdatedAt = time.Now()
	pricing.ID = "" // let MongoDB handle _id

	// Delete existing and insert new (single document collection)
	if _, err := r.col.DeleteMany(ctx, bson.M{}); err != nil {
		return fmt.Errorf("delete existing pricing: %w", err)
	}
	_, err := r.col.InsertOne(ctx, pricing)
	if err != nil {
		return fmt.Errorf("save pricing: %w", err)
	}
	return nil
}

// SeedFromFile loads pricing from a JSON file if MongoDB has no pricing yet.
func (r *PricingRepository) SeedFromFile(ctx context.Context, configDir string) error {
	existing, _ := r.Get(ctx)
	if existing != nil {
		return nil // already seeded
	}

	filePath := filepath.Join(configDir, "pricing.json")
	data, err := os.ReadFile(filePath) //nolint:gosec // trusted internal path
	if err != nil {
		return fmt.Errorf("read pricing file: %w", err)
	}

	var raw struct {
		LLM       map[string]map[string]models.TokenPrice `json:"llm"`
		Warehouse map[string]models.WarehousePrice        `json:"warehouse"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse pricing file: %w", err)
	}

	pricing := &models.Pricing{
		LLM:       raw.LLM,
		Warehouse: raw.Warehouse,
	}

	return r.Save(ctx, pricing)
}
