package database

import (
	"context"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ProjectRepository provides access to project documents.
type ProjectRepository struct {
	db *DB
}

// NewProjectRepository creates a new project repository.
func NewProjectRepository(db *DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

// GetByID returns a project by its hex ObjectId. Every project document
// in Mongo is stored with `_id: ObjectId(...)`; we accept only that shape
// (matching the API's ProjectRepository.GetByID).
func (r *ProjectRepository) GetByID(ctx context.Context, id string) (*models.Project, error) {
	col := r.db.Collection(CollectionProjects)

	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", id, err)
	}

	var project models.Project
	if err := col.FindOne(ctx, bson.M{"_id": oid}).Decode(&project); err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}

	return &project, nil
}
