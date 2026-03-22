package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// secretCollection abstracts the MongoDB collection API for testing.
// The real implementation is *mongo.Collection.
type secretCollection interface {
	FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult
	UpdateOne(ctx context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error)
	Find(ctx context.Context, filter interface{}, opts ...*options.FindOptions) (cur *mongo.Cursor, err error)
	Indexes() mongo.IndexView
}

// Compile-time check that the real collection satisfies the interface.
var _ secretCollection = (*mongo.Collection)(nil)
