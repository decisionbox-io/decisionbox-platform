package askserve

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// turnStore is the agent-side persistence for data-Q&A turns: it appends the
// incremental tool-event log, advances the turn status, finalizes the turn
// with its answer, and (on finalize) writes the durable AskSessionMessage so
// history feeds the next turn. It is the writer half of the persist-and-poll
// contract — the reader (the API) tails the same collections by cursor.
type turnStore struct {
	turns    *mongo.Collection // commonmodels.CollectionAskTurns
	events   *mongo.Collection // commonmodels.CollectionAskTurnEvents
	sessions *mongo.Collection // ask_sessions
	turnTTL  time.Duration
}

// askTurnEventDoc wraps the shared event with the Mongo _id that serves as the
// opaque, monotonic poll cursor — the same idiom discovery's RunStepDoc uses.
type askTurnEventDoc struct {
	ID                        primitive.ObjectID `bson:"_id,omitempty"`
	commonmodels.AskTurnEvent `bson:",inline"`
}

func newTurnStore(db *mongo.Database, turnTTL time.Duration) *turnStore {
	return &turnStore{
		turns:    db.Collection(commonmodels.CollectionAskTurns),
		events:   db.Collection(commonmodels.CollectionAskTurnEvents),
		sessions: db.Collection("ask_sessions"),
		turnTTL:  turnTTL,
	}
}

// EnsureIndexes creates the (turn_id, _id) cursor index on the event log and a
// terminal-only TTL on the turn records so completed turns prune themselves.
func (s *turnStore) EnsureIndexes(ctx context.Context) error {
	if _, err := s.events.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "turn_id", Value: 1}, {Key: "_id", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure ask_turn_events index: %w", err)
	}
	if s.turnTTL > 0 {
		// Expire the event log too — without this the parent-turn TTL would
		// leave orphaned tool-event documents behind indefinitely. Events are
		// created during the turn, so a created_at TTL prunes them on roughly
		// the same horizon as the completed turn (the durable transcript also
		// lives on the AskSession).
		if _, err := s.events.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "created_at", Value: 1}},
			Options: options.Index().
				SetName("ttl_ask_turn_event").
				SetExpireAfterSeconds(int32(s.turnTTL.Seconds())),
		}); err != nil {
			return fmt.Errorf("ensure ask_turn_events TTL index: %w", err)
		}
	}
	if s.turnTTL > 0 {
		if _, err := s.turns.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "completed_at", Value: 1}},
			Options: options.Index().
				SetName("ttl_ask_turn_terminal").
				SetExpireAfterSeconds(int32(s.turnTTL.Seconds())).
				SetPartialFilterExpression(bson.M{
					"status": bson.M{"$in": []string{
						commonmodels.AskTurnStatusDone,
						commonmodels.AskTurnStatusDeclined,
						commonmodels.AskTurnStatusTimedOut,
						commonmodels.AskTurnStatusFailed,
					}},
				}),
		}); err != nil {
			return fmt.Errorf("ensure ask_turns TTL index: %w", err)
		}
	}
	return nil
}

// CreateTurn inserts a fresh running turn. The API normally creates the turn
// before kicking the job; this is used by the agent's own tests and as a
// defensive upsert path.
func (s *turnStore) CreateTurn(ctx context.Context, t commonmodels.AskTurn) error {
	if t.Status == "" {
		t.Status = commonmodels.AskTurnStatusRunning
	}
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	_, err := s.turns.InsertOne(ctx, t)
	if err != nil {
		return fmt.Errorf("insert ask turn %s: %w", t.ID, err)
	}
	return nil
}

// EnsureTurn inserts the running turn only if it does not already exist
// (idempotent). The API normally creates the turn before kicking the job;
// this makes the serving side robust if it didn't, without clobbering the
// caller-supplied session_id / question on an existing record.
func (s *turnStore) EnsureTurn(ctx context.Context, t commonmodels.AskTurn) error {
	now := time.Now()
	_, err := s.turns.UpdateOne(ctx,
		bson.M{"_id": t.ID},
		bson.M{"$setOnInsert": bson.M{
			"session_id": t.SessionID,
			"project_id": t.ProjectID,
			"question":   t.Question,
			"status":     commonmodels.AskTurnStatusRunning,
			"created_at": now,
			"updated_at": now,
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("ensure ask turn %s: %w", t.ID, err)
	}
	return nil
}

// AppendEvent persists one tool event to the turn's log. The Mongo-assigned
// _id is the cursor the reader feeds back via since=.
func (s *turnStore) AppendEvent(ctx context.Context, turnID, projectID string, ev commonmodels.ToolEvent) error {
	doc := askTurnEventDoc{
		AskTurnEvent: commonmodels.AskTurnEvent{
			TurnID:    turnID,
			ProjectID: projectID,
			CreatedAt: time.Now(),
			ToolEvent: ev,
		},
	}
	if _, err := s.events.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("append ask turn event (turn %s): %w", turnID, err)
	}
	// Touch the parent so updated_at reflects progress for ops visibility.
	_, _ = s.turns.UpdateByID(ctx, turnID, bson.M{"$set": bson.M{"updated_at": time.Now()}})
	return nil
}

// SetStatus advances a non-terminal turn to another status. It refuses to
// overwrite a turn that has already reached a terminal status.
func (s *turnStore) SetStatus(ctx context.Context, turnID, status string) error {
	_, err := s.turns.UpdateOne(ctx,
		bson.M{"_id": turnID, "status": commonmodels.AskTurnStatusRunning},
		bson.M{"$set": bson.M{"status": status, "updated_at": time.Now()}},
	)
	if err != nil {
		return fmt.Errorf("set ask turn %s status=%s: %w", turnID, status, err)
	}
	return nil
}

// Finalize records the terminal outcome on the turn and, for answered/declined
// turns, appends the durable AskSessionMessage carrying the same tool-event
// transcript so the next turn's history sees it. It refuses to finalize a turn
// that is already terminal (last-writer-wins is the wrong semantics here).
func (s *turnStore) Finalize(ctx context.Context, fin TurnFinal) error {
	now := time.Now()
	set := bson.M{
		"status":       fin.Status,
		"disposition":  fin.Disposition,
		"answer":       fin.Answer,
		"model":        fin.Model,
		"updated_at":   now,
		"completed_at": now,
	}
	if fin.Error != "" {
		set["error"] = fin.Error
	}
	if fin.InputTokens > 0 {
		set["input_tokens"] = fin.InputTokens
	}
	if fin.OutputTokens > 0 {
		set["output_tokens"] = fin.OutputTokens
	}
	if len(fin.RoutedDatasourceIDs) > 0 {
		set["routed_datasource_ids"] = fin.RoutedDatasourceIDs
	}
	res, err := s.turns.UpdateOne(ctx,
		bson.M{"_id": fin.TurnID, "status": commonmodels.AskTurnStatusRunning},
		bson.M{"$set": set},
	)
	if err != nil {
		return fmt.Errorf("finalize ask turn %s: %w", fin.TurnID, err)
	}
	if res.MatchedCount == 0 {
		// Already terminal (e.g. swept as orphan by a racing restart). Do not
		// also write a session message — avoid a duplicate.
		return nil
	}

	if fin.SessionID == "" {
		return nil
	}
	msg := commonmodels.AskSessionMessage{
		Question:     fin.Question,
		Answer:       fin.Answer,
		Model:        fin.Model,
		InputTokens:  fin.InputTokens,
		OutputTokens: fin.OutputTokens,
		ToolEvents:   fin.ToolEvents,
		Sources:      fin.Sources,
		CreatedAt:    now,
	}
	// Append via an aggregation pipeline with $ifNull so a legacy session whose
	// `messages` / `message_count` are null (pre-dating those fields) is
	// coerced into existence rather than failing the $push — the turn has
	// already been marked terminal above, so a failed append here would never
	// be retried and would silently drop the message.
	_, err = s.sessions.UpdateOne(ctx,
		bson.M{"_id": fin.SessionID},
		mongo.Pipeline{
			{{Key: "$set", Value: bson.M{
				"messages": bson.M{"$concatArrays": bson.A{
					bson.M{"$ifNull": bson.A{"$messages", bson.A{}}},
					// $literal so a field whose value begins with '$' (e.g. a
					// monetary answer like "$1.2M") is stored verbatim rather
					// than evaluated as a field path by the aggregation pipeline.
					bson.A{bson.M{"$literal": msg}},
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
		return fmt.Errorf("append session message (session %s, turn %s): %w", fin.SessionID, fin.TurnID, err)
	}
	return nil
}

// SweepOrphans marks any still-running turn as failed. Called on startup and
// graceful shutdown so a turn stranded by a pod restart reaches a terminal
// status with its partial transcript preserved — the same semantics a dead
// discovery run gets. Returns the number of turns swept.
//
// NOTE: this sweeps ALL running turns, which is correct for the single-replica
// deployment ask-serve runs in for Phase 1 (helm askServe.replicaCount: 1). A
// multi-replica deployment would need per-turn ownership/lease so one pod's
// boot doesn't fail another pod's live turns; that hardening is tracked as a
// follow-up and is why the chart documents single-replica for now.
func (s *turnStore) SweepOrphans(ctx context.Context) (int, error) {
	res, err := s.turns.UpdateMany(ctx,
		bson.M{"status": commonmodels.AskTurnStatusRunning},
		bson.M{"$set": bson.M{
			"status":       commonmodels.AskTurnStatusFailed,
			"error":        "ask-serve restarted while the turn was in progress",
			"updated_at":   time.Now(),
			"completed_at": time.Now(),
		}},
	)
	if err != nil {
		return 0, fmt.Errorf("sweep orphan ask turns: %w", err)
	}
	return int(res.ModifiedCount), nil
}

// GetTurn loads a turn by id (used by the status endpoint + tests).
func (s *turnStore) GetTurn(ctx context.Context, turnID string) (*commonmodels.AskTurn, error) {
	var t commonmodels.AskTurn
	err := s.turns.FindOne(ctx, bson.M{"_id": turnID}).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ask turn %s: %w", turnID, err)
	}
	return &t, nil
}

// TurnFinal carries everything Finalize needs to close out a turn and write
// its session message.
type TurnFinal struct {
	TurnID       string
	SessionID    string
	Question     string
	Status       string
	Disposition  string
	Answer       string
	Error        string
	Model        string
	InputTokens  int
	OutputTokens int
	ToolEvents   []commonmodels.ToolEvent
	// Sources are the insights/recommendations the turn cited (from
	// search_insights), persisted on the message so the dashboard renders them
	// as citations on history reload.
	Sources []commonmodels.AskSessionSource
	// RoutedDatasourceIDs are the datasources this turn queried (multi-warehouse
	// routing telemetry). Empty on a single-datasource / pinned turn.
	RoutedDatasourceIDs []string
}
