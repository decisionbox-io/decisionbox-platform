package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	goauth "github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrAskSessionNotFound is returned when no session matches the lookup key.
// Mirrors ErrBookmarkListNotFound: a session that does not exist, one in another
// project, and one belonging to another user are all the same answer, so the API
// cannot be used to probe for session ids.
var ErrAskSessionNotFound = errors.New("ask session not found")

// AskSessionRepository handles CRUD for the "ask_sessions" collection.
type AskSessionRepository struct {
	db *DB
}

// sessionFilter builds the (project_id, user_id, _id) lookup key every method
// scopes on. Ownership lives in the key rather than in a check the caller is
// expected to remember, so there is no un-scoped read path to forget.
//
// userID == "" means NO owner filter. It is for internal callers that have
// already established the caller's access (an ops collector over a whole
// project, a tool running inside a turn the API already authorized) — never for
// a request that arrived from a user.
//
// A session whose owner is the NoAuth subject is shared rather than owned: with
// authentication off there was one caller, so a deployment that has since turned
// authentication on has rows nobody in particular wrote. They stay readable by
// everyone, exactly as they were, instead of becoming invisible to the people
// already reading them — and nothing can add to that set, because a request
// carrying a real principal always writes that principal's subject. A missing or
// empty owner is NOT shared: that is a row we cannot explain, and the safe answer
// for one of those is invisible.
func sessionFilter(projectID, userID, sessionID string) bson.M {
	f := bson.M{}
	if sessionID != "" {
		f["_id"] = sessionID
	}
	if projectID != "" {
		f["project_id"] = projectID
	}
	switch userID {
	case "":
		// no owner filter
	case goauth.AnonymousSubject:
		// The caller IS the shared subject (authentication off), so plain
		// equality already matches every row it could own.
		f["user_id"] = userID
	default:
		f["user_id"] = bson.M{"$in": []string{userID, goauth.AnonymousSubject}}
	}
	return f
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

// AppendMessage appends a Q&A turn to an existing session, scoped by the
// (project, owner, id) key so a turn cannot be written into a conversation the
// caller does not own. userID == "" skips the owner filter — see sessionFilter.
//
// Uses $push for the steady-state O(1) append; falls back to an
// aggregation-pipeline rewrite once when the document was created by
// an older build whose Insert left messages as null (Mongo's $push
// refuses to apply to a non-array field with a "Cannot apply $push"
// error). The fallback rewrites the field once, after which every
// subsequent append takes the fast path.
//
// A write that matches nothing returns ErrAskSessionNotFound. It used to return
// nil, so appending to a session that did not exist reported success.
func (r *AskSessionRepository) AppendMessage(ctx context.Context, projectID, userID, sessionID string, msg commonmodels.AskSessionMessage) error {
	col := r.db.Collection("ask_sessions")
	filter := sessionFilter(projectID, userID, sessionID)
	now := time.Now()
	res, err := col.UpdateOne(ctx,
		filter,
		bson.M{
			"$push": bson.M{"messages": msg},
			"$inc":  bson.M{"message_count": 1},
			"$set":  bson.M{"updated_at": now},
		},
	)
	if err == nil {
		if res.MatchedCount == 0 {
			return ErrAskSessionNotFound
		}
		return nil
	}
	if !isLegacyNullFieldError(err) {
		return fmt.Errorf("append message to session %s: %w", sessionID, err)
	}
	// Legacy doc: messages == null. Repair via aggregation pipeline so
	// the array is coerced into existence; subsequent appends use $push
	// without re-tripping this branch. Same owner-scoped filter — the repair
	// path must not be a way around the key.
	res, err = col.UpdateOne(ctx,
		filter,
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
	if res.MatchedCount == 0 {
		return ErrAskSessionNotFound
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

// GetByID loads one session by the (project, owner, id) key. userID == "" skips
// the owner filter — see sessionFilter. Returns ErrAskSessionNotFound when
// nothing matches, so callers need no follow-up project or owner comparison.
func (r *AskSessionRepository) GetByID(ctx context.Context, projectID, userID, sessionID string) (*commonmodels.AskSession, error) {
	var session commonmodels.AskSession
	err := r.db.Collection("ask_sessions").
		FindOne(ctx, sessionFilter(projectID, userID, sessionID)).
		Decode(&session)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrAskSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get ask session %s: %w", sessionID, err)
	}
	return &session, nil
}

// ListByProject lists the caller's Ask sessions in a project, newest-first.
// userID == "" lists every session in the project — see sessionFilter. When
// seedType and seedID are both non-empty, the list is scoped to sessions seeded
// from that exact insight / recommendation ("previous conversations about this
// item"). The seed reference (type/id/label) is projected so the caller can label
// the list; the bulky hydrated seed text is deliberately excluded.
func (r *AskSessionRepository) ListByProject(ctx context.Context, projectID, userID string, limit int, seedType, seedID string) ([]*commonmodels.AskSession, error) {
	if limit <= 0 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"_id":                1,
			"project_id":         1,
			"user_id":            1,
			"title":              1,
			"message_count":      1,
			"created_at":         1,
			"updated_at":         1,
			"seed_context.type":  1,
			"seed_context.id":    1,
			"seed_context.label": 1,
		})

	filter := sessionFilter(projectID, userID, "")
	if seedType != "" && seedID != "" {
		filter["seed_context.type"] = seedType
		filter["seed_context.id"] = seedID
	}

	cursor, err := r.db.Collection("ask_sessions").Find(ctx, filter, opts)
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

// Delete removes one session by the (project, owner, id) key, so deleting a
// session is the same act as being able to see it. userID == "" skips the owner
// filter — see sessionFilter. Returns ErrAskSessionNotFound when nothing matched,
// which is what stops a silent no-op reading as success.
func (r *AskSessionRepository) Delete(ctx context.Context, projectID, userID, sessionID string) error {
	res, err := r.db.Collection("ask_sessions").DeleteOne(ctx, sessionFilter(projectID, userID, sessionID))
	if err != nil {
		return fmt.Errorf("delete ask session %s: %w", sessionID, err)
	}
	if res.DeletedCount == 0 {
		return ErrAskSessionNotFound
	}
	return nil
}
