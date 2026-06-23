//go:build integration

package askserve

import (
	"context"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func setupStore(t *testing.T) (*turnStore, *mongo.Database, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	cfg := gomongo.DefaultConfig()
	cfg.URI = connStr
	cfg.Database = "test_askserve"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	db := client.Database()
	store := newTurnStore(db, time.Hour)
	if err := store.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	return store, db, func() {
		_ = client.Disconnect(ctx)
		_ = container.Terminate(ctx)
	}
}

// eventsSince mirrors the cursor read the API will perform against the same
// collection: events for a turn with _id strictly greater than `since`,
// ordered ascending. Validates the persist-and-poll contract end-to-end.
func eventsSince(t *testing.T, db *mongo.Database, turnID, since string) []askTurnEventDoc {
	t.Helper()
	filter := bson.M{"turn_id": turnID}
	if since != "" {
		oid, err := primitive.ObjectIDFromHex(since)
		if err != nil {
			t.Fatalf("bad cursor %q: %v", since, err)
		}
		filter["_id"] = bson.M{"$gt": oid}
	}
	cur, err := db.Collection(commonmodels.CollectionAskTurnEvents).
		Find(context.Background(), filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		t.Fatalf("find events: %v", err)
	}
	var docs []askTurnEventDoc
	if err := cur.All(context.Background(), &docs); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return docs
}

func TestIntegration_TurnStore_AppendCursorFinalize(t *testing.T) {
	store, db, cleanup := setupStore(t)
	defer cleanup()
	ctx := context.Background()

	// A pre-existing session, as the API would have created.
	_, err := db.Collection("ask_sessions").InsertOne(ctx, commonmodels.AskSession{
		ID: "s1", ProjectID: "p1", Messages: []commonmodels.AskSessionMessage{}, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := store.CreateTurn(ctx, commonmodels.AskTurn{ID: "t1", SessionID: "s1", ProjectID: "p1", Question: "how many?"}); err != nil {
		t.Fatalf("create turn: %v", err)
	}

	for i := 0; i < 3; i++ {
		ev := commonmodels.ToolEvent{Round: i + 1, Name: "query_data"}
		if err := store.AppendEvent(ctx, "t1", "p1", ev); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	all := eventsSince(t, db, "t1", "")
	if len(all) != 3 {
		t.Fatalf("events = %d, want 3", len(all))
	}
	// Monotonic cursor: reading since the first id returns only the later two.
	tail := eventsSince(t, db, "t1", all[0].ID.Hex())
	if len(tail) != 2 {
		t.Fatalf("cursor read = %d, want 2", len(tail))
	}

	// Finalize: terminal status + session message with the transcript.
	err = store.Finalize(ctx, TurnFinal{
		TurnID: "t1", SessionID: "s1", Question: "how many?",
		Status: commonmodels.AskTurnStatusDone, Disposition: commonmodels.AskTurnDispositionAnswer,
		Answer: "100", Model: "m", ToolEvents: []commonmodels.ToolEvent{{Round: 1, Name: "query_data"}},
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	turn, err := store.GetTurn(ctx, "t1")
	if err != nil || turn == nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != commonmodels.AskTurnStatusDone || turn.Answer != "100" || !turn.Terminal() {
		t.Fatalf("turn not finalized correctly: %+v", turn)
	}

	var sess commonmodels.AskSession
	if err := db.Collection("ask_sessions").FindOne(ctx, bson.M{"_id": "s1"}).Decode(&sess); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if len(sess.Messages) != 1 || sess.Messages[0].Answer != "100" || len(sess.Messages[0].ToolEvents) != 1 {
		t.Fatalf("session message not written: %+v", sess.Messages)
	}

	// Idempotency: a second Finalize must not double-write the session message.
	_ = store.Finalize(ctx, TurnFinal{TurnID: "t1", SessionID: "s1", Status: commonmodels.AskTurnStatusDone, Answer: "100"})
	_ = db.Collection("ask_sessions").FindOne(ctx, bson.M{"_id": "s1"}).Decode(&sess)
	if len(sess.Messages) != 1 {
		t.Fatalf("second finalize double-wrote: %d messages", len(sess.Messages))
	}
}

func TestIntegration_TurnStore_SweepOrphans(t *testing.T) {
	store, _, cleanup := setupStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := store.CreateTurn(ctx, commonmodels.AskTurn{ID: "running", SessionID: "s", ProjectID: "p", Question: "q"}); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	n, err := store.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept = %d, want 1", n)
	}
	turn, _ := store.GetTurn(ctx, "running")
	if turn == nil || turn.Status != commonmodels.AskTurnStatusFailed {
		t.Fatalf("orphan not failed: %+v", turn)
	}
	// A second sweep is a no-op (already terminal).
	if n2, _ := store.SweepOrphans(ctx); n2 != 0 {
		t.Fatalf("second sweep = %d, want 0", n2)
	}
}

func TestIntegration_TurnStore_EnsureTurnIdempotent(t *testing.T) {
	store, _, cleanup := setupStore(t)
	defer cleanup()
	ctx := context.Background()

	tn := commonmodels.AskTurn{ID: "t", SessionID: "s1", ProjectID: "p", Question: "original"}
	if err := store.EnsureTurn(ctx, tn); err != nil {
		t.Fatalf("ensure 1: %v", err)
	}
	// A second EnsureTurn with a different session must not clobber the first.
	tn2 := commonmodels.AskTurn{ID: "t", SessionID: "s2", ProjectID: "p", Question: "changed"}
	if err := store.EnsureTurn(ctx, tn2); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	turn, _ := store.GetTurn(ctx, "t")
	if turn.SessionID != "s1" || turn.Question != "original" {
		t.Fatalf("EnsureTurn clobbered existing record: %+v", turn)
	}
}
