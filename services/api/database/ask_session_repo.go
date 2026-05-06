package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AskSessionRepository handles CRUD for the "ask_sessions" collection.
type AskSessionRepository struct {
	db *DB
}

func NewAskSessionRepository(db *DB) *AskSessionRepository {
	return &AskSessionRepository{db: db}
}

// Create inserts a new ask session. Normalises Messages to an empty
// slice when the caller passes nil so BSON serialises the field as
// `[]` rather than `null` — Mongo's `$push` refuses to append to a
// null field, and AppendMessage relied on the field already being an
// array. This was a footgun for any caller that wanted to create a
// session before knowing the first message (e.g. an agentic flow that
// runs a tool loop and only persists the message at the end).
func (r *AskSessionRepository) Create(ctx context.Context, session *commonmodels.AskSession) error {
	if session.Messages == nil {
		session.Messages = []commonmodels.AskSessionMessage{}
	}
	session.MessageCount = len(session.Messages)
	session.CreatedAt = time.Now()
	session.UpdatedAt = time.Now()
	_, err := r.db.Collection("ask_sessions").InsertOne(ctx, session)
	if err != nil {
		return fmt.Errorf("insert ask session: %w", err)
	}
	return nil
}

// AppendMessage appends a Q&A turn to an existing session. Uses
// $push for the steady-state O(1) append; falls back to an
// aggregation-pipeline rewrite once when the document was created by
// an older build whose Insert left messages as null (Mongo's $push
// refuses to apply to a non-array field with a "Cannot apply $push"
// error). The fallback rewrites the field once, after which every
// subsequent append takes the fast path.
func (r *AskSessionRepository) AppendMessage(ctx context.Context, sessionID string, msg commonmodels.AskSessionMessage) error {
	col := r.db.Collection("ask_sessions")
	now := time.Now()
	_, err := col.UpdateOne(ctx,
		bson.M{"_id": sessionID},
		bson.M{
			"$push": bson.M{"messages": msg},
			"$inc":  bson.M{"message_count": 1},
			"$set":  bson.M{"updated_at": now},
		},
	)
	if err == nil {
		return nil
	}
	if !isLegacyNullFieldError(err) {
		return fmt.Errorf("append message to session %s: %w", sessionID, err)
	}
	// Legacy doc: messages == null. Repair via aggregation pipeline so
	// the array is coerced into existence; subsequent appends use $push
	// without re-tripping this branch.
	_, err = col.UpdateOne(ctx,
		bson.M{"_id": sessionID},
		mongo.Pipeline{
			{{Key: "$set", Value: bson.M{
				"messages": bson.M{"$concatArrays": bson.A{
					bson.M{"$ifNull": bson.A{"$messages", bson.A{}}},
					bson.A{msg},
				}},
				"message_count": bson.M{"$add": bson.A{
					bson.M{"$ifNull": bson.A{"$message_count", 0}},
					1,
				}},
				"updated_at": now,
			}}},
		},
	)
	if err != nil {
		return fmt.Errorf("append message (legacy repair) to session %s: %w", sessionID, err)
	}
	return nil
}

// isLegacyNullFieldError matches the Mongo write errors that surface
// on a legacy session document whose Insert left messages and / or
// message_count as null. Two operator paths can trip first depending
// on validation order:
//
//   - $push refuses to apply to a non-array field
//     ("Cannot apply $push to a non-array field", code 2 BadValue).
//   - $inc refuses to apply to a non-numeric field
//     ("Cannot apply $inc to a value of non-numeric type", code 14
//     TypeMismatch).
//
// Both are recoverable in the same way: rewrite the document via the
// aggregation pipeline repair branch. Pinning on message text is
// brittle but the wire surface here is stable across recent Mongo
// majors and an unexpected match just causes the caller to fall
// through to the already-tested aggregation path.
func isLegacyNullFieldError(err error) bool {
	if err == nil {
		return false
	}
	var we mongo.WriteException
	if !errors.As(err, &we) {
		return false
	}
	for _, e := range we.WriteErrors {
		if strings.Contains(e.Message, "Cannot apply $push") ||
			strings.Contains(e.Message, "Cannot apply $inc") {
			return true
		}
	}
	return false
}

func (r *AskSessionRepository) GetByID(ctx context.Context, sessionID string) (*commonmodels.AskSession, error) {
	var session commonmodels.AskSession
	err := r.db.Collection("ask_sessions").FindOne(ctx, bson.M{"_id": sessionID}).Decode(&session)
	if err != nil {
		return nil, fmt.Errorf("get ask session %s: %w", sessionID, err)
	}
	return &session, nil
}

func (r *AskSessionRepository) ListByProject(ctx context.Context, projectID string, limit int) ([]*commonmodels.AskSession, error) {
	if limit <= 0 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"_id":           1,
			"project_id":    1,
			"user_id":       1,
			"title":         1,
			"message_count": 1,
			"created_at":    1,
			"updated_at":    1,
		})

	cursor, err := r.db.Collection("ask_sessions").Find(ctx, bson.M{"project_id": projectID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list ask sessions for project %s: %w", projectID, err)
	}
	defer cursor.Close(ctx)

	var sessions []*commonmodels.AskSession
	if err := cursor.All(ctx, &sessions); err != nil {
		return nil, fmt.Errorf("decode ask sessions: %w", err)
	}
	return sessions, nil
}

func (r *AskSessionRepository) Delete(ctx context.Context, sessionID string) error {
	_, err := r.db.Collection("ask_sessions").DeleteOne(ctx, bson.M{"_id": sessionID})
	if err != nil {
		return fmt.Errorf("delete ask session %s: %w", sessionID, err)
	}
	return nil
}
