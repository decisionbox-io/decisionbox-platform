package database

import "context"

// MongoHealthChecker implements health.Checker for MongoDB.
type MongoHealthChecker struct {
	db *DB
}

func NewMongoHealthChecker(db *DB) *MongoHealthChecker {
	return &MongoHealthChecker{db: db}
}

func (c *MongoHealthChecker) Name() string {
	return "mongodb"
}

func (c *MongoHealthChecker) Check(ctx context.Context) error {
	return c.db.client.Ping(ctx)
}
