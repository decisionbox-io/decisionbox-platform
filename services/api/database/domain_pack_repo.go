package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
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

// riskConfigHashField stores models.RiskConfigHash alongside the pack.
//
// Kept off the struct (`bson:"-"` on DomainPack.RiskConfigHash) and written
// explicitly, so a client that echoes the served value back cannot overwrite
// the stored one through this repository's whole-document write.
const riskConfigHashField = "risk_config_hash"

// setDocument renders a pack as the document a `$set` writes, so a caller can
// add a field the struct does not carry. The field set is exactly what
// `$set: pack` produced before, including omitempty.
func setDocument(pack *models.DomainPack) (bson.M, error) {
	raw, err := bson.Marshal(pack)
	if err != nil {
		return nil, fmt.Errorf("marshal domain pack: %w", err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode domain pack update: %w", err)
	}
	return doc, nil
}

func (r *DomainPackRepository) Create(ctx context.Context, pack *models.DomainPack) error {
	pack.CreatedAt = time.Now()
	pack.UpdatedAt = time.Now()

	// Stored at insert as well, so "the pack has a fingerprint" is the normal
	// case and the absent one means a document written before this existed.
	doc, err := setDocument(pack)
	if err != nil {
		return err
	}
	doc[riskConfigHashField] = models.RiskConfigHash(pack)

	result, err := r.col.InsertOne(ctx, doc)
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
	pack.RiskConfigHash = models.RiskConfigHash(&pack)
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
	pack.RiskConfigHash = models.RiskConfigHash(&pack)
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
	// Filled on every read path, not at the handler: a pack served without it
	// reads to a client as "this API offers no such check", and which of the
	// several handlers served it is not something a client can see.
	for _, pack := range packs {
		pack.RiskConfigHash = models.RiskConfigHash(pack)
	}
	return packs, nil
}

func (r *DomainPackRepository) Update(ctx context.Context, slug string, pack *models.DomainPack) error {
	pack.ID = ""
	pack.UpdatedAt = time.Now()

	// The whole model, as a document, so the risk-configuration fingerprint
	// can be written in the SAME update.
	//
	// This route rewrites the risk configuration like any other field, and it
	// is the one writer of a pack that knows nothing about who else is editing
	// one. Maintaining the fingerprint here is what lets a concurrent editor
	// of that configuration notice this write happened — without it, a save
	// built on the pack as it was before this one lands on top of it and
	// reports success. See models.RiskConfigHash.
	set, err := setDocument(pack)
	if err != nil {
		return err
	}
	set[riskConfigHashField] = models.RiskConfigHash(pack)

	update := bson.M{"$set": set}
	// `risk_taxonomy` is omitempty, so an empty one is absent from the $set
	// above and the stored value survives — which makes removing a pack's
	// last risk category impossible through this route. It passes validation
	// (a pack with no taxonomy is legal), answers 200, and leaves every
	// project created afterwards weighted by a taxonomy the pack no longer
	// declares.
	//
	// Unset explicitly instead. The same rule would apply to any other
	// omitempty field this route is expected to be able to clear; today this
	// is the only one, so it is named rather than generalised.
	if len(pack.RiskTaxonomy) == 0 {
		update["$unset"] = bson.M{"risk_taxonomy": ""}
	}

	result, err := r.col.UpdateOne(ctx, bson.M{"slug": slug}, update)
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

