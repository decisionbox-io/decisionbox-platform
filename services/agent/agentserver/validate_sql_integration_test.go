//go:build integration

// Integration test for `agent --mode=validate-sql`. Spins up a real Mongo
// (testcontainer) for the job + project + secret rows and a real
// PostgreSQL (testcontainer) as the project's warehouse, then runs the
// mode end-to-end and asserts:
//   - a valid statement round-trips to ok:true,
//   - an invalid statement round-trips to ok:false carrying the
//     warehouse's own error text,
//   - a DELETE-shaped statement *compiles* (ok:true) but is NEVER
//     executed — the seeded row count is unchanged afterwards, proving
//     ValidateSQL is compile-only.
package agentserver

import (
	"context"
	"strings"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestInteg_ValidateSQL_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// The agent's initSecretProvider reads gosecrets.LoadConfig(): pin
	// the namespace + plaintext mode so the secret we seed below is read
	// back unchanged regardless of the ambient environment.
	t.Setenv("SECRET_PROVIDER", "mongodb")
	t.Setenv("SECRET_NAMESPACE", "decisionbox")
	t.Setenv("SECRET_ENCRYPTION_KEY", "")

	// --- Mongo testcontainer ---
	mongoC, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	defer func() { _ = mongoC.Terminate(ctx) }()
	mongoURI, err := mongoC.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mongo conn string: %v", err)
	}

	const dbName = "agentserver_validate_sql_test"
	mcfg := gomongo.DefaultConfig()
	mcfg.URI = mongoURI
	mcfg.Database = dbName
	mongoClient, err := gomongo.NewClient(ctx, mcfg)
	if err != nil {
		t.Fatalf("mongo client: %v", err)
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	// --- Postgres testcontainer (the project's warehouse) ---
	pgC, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() { _ = pgC.Terminate(ctx) }()
	connStr, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres conn string: %v", err)
	}

	// Seed a tiny table with three rows through a provider built the same
	// way the agent builds it. This provider is also used to count rows
	// before / after, to prove the DELETE never executes.
	pg, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"auth_method":      "connection_string",
		"credentials_json": connStr,
		"dataset":          "public",
	})
	if err != nil {
		t.Fatalf("build seed postgres provider: %v", err)
	}
	defer pg.Close()
	if _, err := pg.Query(ctx, "CREATE TABLE events (id INTEGER)", nil); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := pg.Query(ctx, "INSERT INTO events (id) VALUES (1), (2), (3)", nil); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if got := countEvents(ctx, t, pg); got != 3 {
		t.Fatalf("seeded row count = %d, want 3", got)
	}

	// --- project + warehouse-credentials secret ---
	oid := primitive.NewObjectID()
	projectID := oid.Hex()
	_, err = mongoClient.Collection("projects").InsertOne(ctx, bson.M{
		"_id":      oid,
		"name":     "validate-sql-it",
		"domain":   "test",
		"category": "test",
		"warehouse": bson.M{
			"provider": "postgres",
			"datasets": []string{"public"},
			"config":   bson.M{"auth_method": "connection_string"},
		},
		"status":     "active",
		"created_at": time.Now(),
		"updated_at": time.Now(),
	})
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	secretProvider, err := mongoSecrets.NewMongoProvider(mongoClient.Collection("secrets"), "decisionbox", "")
	if err != nil {
		t.Fatalf("secret provider: %v", err)
	}
	if err := secretProvider.Set(ctx, projectID, "warehouse-credentials", connStr); err != nil {
		t.Fatalf("seed warehouse-credentials: %v", err)
	}

	// --- enqueue the SQL validation job (pending; the agent claims it) ---
	const (
		validSQL   = "SELECT id FROM events"
		invalidSQL = "SELEC id FRMO events"            // deliberate syntax errors
		deleteSQL  = "DELETE FROM events WHERE id = 1" // compiles, must NOT run
	)
	jobID := uuid.New().String()
	_, err = mongoClient.Collection(sqlValidationJobsCollection).InsertOne(ctx, bson.M{
		"_id":         jobID,
		"project_id":  projectID,
		"statements":  []string{validSQL, invalidSQL, deleteSQL},
		"status":      "pending",
		"attempt":     0,
		"enqueued_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	// --- run the mode ---
	cfg := &config.Config{}
	cfg.Service.Name = "validate-sql-it"
	cfg.Service.LogLevel = "warn"
	cfg.MongoDB.URI = mongoURI
	cfg.MongoDB.Database = dbName

	if err := runValidateSQL(cfg, jobID); err != nil {
		t.Fatalf("runValidateSQL: %v", err)
	}

	// --- assert the persisted verdicts ---
	var got struct {
		Status   string `bson:"status"`
		Error    string `bson:"error"`
		WorkerID string `bson:"worker_id"`
		Results  []struct {
			SQL   string `bson:"sql"`
			OK    bool   `bson:"ok"`
			Error string `bson:"error"`
		} `bson:"results"`
	}
	if err := mongoClient.Collection(sqlValidationJobsCollection).
		FindOne(ctx, bson.M{"_id": jobID}).Decode(&got); err != nil {
		t.Fatalf("reload job: %v", err)
	}

	if got.Status != "completed" {
		t.Fatalf("job status = %q (error=%q), want completed", got.Status, got.Error)
	}
	// The claim stamps worker_id so a stuck row is traceable to its run.
	if got.WorkerID == "" {
		t.Errorf("worker_id was not stamped on claim")
	}
	if len(got.Results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(got.Results), got.Results)
	}

	// Valid SELECT compiles.
	if r := got.Results[0]; r.SQL != validSQL || !r.OK || r.Error != "" {
		t.Errorf("valid statement verdict = %+v, want ok:true no error", r)
	}
	// Invalid SQL is rejected with the warehouse's own error text.
	if r := got.Results[1]; r.SQL != invalidSQL || r.OK || r.Error == "" {
		t.Errorf("invalid statement verdict = %+v, want ok:false with error", r)
	} else if !strings.Contains(r.Error, "postgres") {
		t.Errorf("invalid statement error %q should carry the warehouse's own message", r.Error)
	}
	// DELETE compiles (ok:true) — EXPLAIN plans without running it.
	if r := got.Results[2]; r.SQL != deleteSQL || !r.OK || r.Error != "" {
		t.Errorf("delete statement verdict = %+v, want ok:true (compiles, not executed)", r)
	}

	// The clincher: the seeded rows are untouched, proving ValidateSQL
	// compiled the DELETE without executing it.
	if got := countEvents(ctx, t, pg); got != 3 {
		t.Errorf("row count after validation = %d, want 3 — the DELETE must NOT have executed", got)
	}
}

func countEvents(ctx context.Context, t *testing.T, pg gowarehouse.Provider) int64 {
	t.Helper()
	res, err := pg.Query(ctx, "SELECT count(*) AS n FROM events", nil)
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("count returned %d rows, want 1", len(res.Rows))
	}
	n, ok := res.Rows[0]["n"].(int64)
	if !ok {
		t.Fatalf("count value %v (%T) is not int64", res.Rows[0]["n"], res.Rows[0]["n"])
	}
	return n
}
