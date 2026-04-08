package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DomainPackRepository provides CRUD access to the domain_packs collection.
type DomainPackRepository struct {
	col *mongo.Collection
}

func NewDomainPackRepository(db *DB) *DomainPackRepository {
	return &DomainPackRepository{col: db.Collection("domain_packs")}
}

func (r *DomainPackRepository) Create(ctx context.Context, pack *models.DomainPack) error {
	pack.CreatedAt = time.Now()
	pack.UpdatedAt = time.Now()

	result, err := r.col.InsertOne(ctx, pack)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("domain pack with slug %q already exists", pack.Slug)
		}
		return fmt.Errorf("insert domain pack: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		pack.ID = oid.Hex()
	}

	return nil
}

func (r *DomainPackRepository) GetBySlug(ctx context.Context, slug string) (*models.DomainPack, error) {
	var pack models.DomainPack
	if err := r.col.FindOne(ctx, bson.M{"slug": slug}).Decode(&pack); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find domain pack: %w", err)
	}
	return &pack, nil
}

func (r *DomainPackRepository) GetByID(ctx context.Context, id string) (*models.DomainPack, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, nil
	}

	var pack models.DomainPack
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&pack); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find domain pack: %w", err)
	}
	return &pack, nil
}

func (r *DomainPackRepository) List(ctx context.Context, publishedOnly bool) ([]*models.DomainPack, error) {
	filter := bson.M{}
	if publishedOnly {
		filter["is_published"] = true
	}

	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list domain packs: %w", err)
	}
	defer cursor.Close(ctx)

	packs := make([]*models.DomainPack, 0)
	if err := cursor.All(ctx, &packs); err != nil {
		return nil, fmt.Errorf("decode domain packs: %w", err)
	}
	return packs, nil
}

func (r *DomainPackRepository) Update(ctx context.Context, slug string, pack *models.DomainPack) error {
	pack.ID = ""
	pack.UpdatedAt = time.Now()

	result, err := r.col.UpdateOne(ctx, bson.M{"slug": slug}, bson.M{"$set": pack})
	if err != nil {
		return fmt.Errorf("update domain pack: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("domain pack not found: %s", slug)
	}
	return nil
}

func (r *DomainPackRepository) Delete(ctx context.Context, slug string) error {
	result, err := r.col.DeleteOne(ctx, bson.M{"slug": slug})
	if err != nil {
		return fmt.Errorf("delete domain pack: %w", err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("domain pack not found: %s", slug)
	}
	return nil
}

