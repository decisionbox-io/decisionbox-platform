//go:build integration

// Integration test for `agent --mode=ask-serve`. It boots the real runAskServe
// entrypoint (the always-up data-Q&A HTTP service) against a testcontainer
// Mongo (project + credential secret + durable turn store) and a testcontainer
// Postgres (the project's warehouse), with the reasoning LLM driven by a
// scripted local Ollama-API stub. Ollama has no native tool-calling, so the
// server takes the JSON-text loop and the stub simply returns one action object
// per round. One turn is driven end-to-end:
//
//	round 1: the model issues a query_data action -> real SQL runs against
//	         Postgres. This exercises ask_serve.go's lazy per-datasource
//	         warehouseBuild closure — the warehouse connect, the ValidateReadOnly
//	         security boundary, and the self-healing executor wiring — none of
//	         which any unit test covers.
//	round 2: the model, now grounded, answers -> the turn is finalized to
//	         status "done" with the answer, read back over GET /turns/{id}.
//
// This is the entrypoint-level companion to the internal/askserve unit tests
// (which cover the loop/router/pool logic with a faked provider): here the whole
// wiring in runAskServe is what's under test.
package agentserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
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

func TestInteg_AskServe_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// The agent's initSecretProvider reads gosecrets.LoadConfig(): pin the
	// namespace + plaintext mode so the warehouse-credentials secret we seed
	// below is read back unchanged regardless of the ambient environment.
	t.Setenv("SECRET_PROVIDER", "mongodb")
	t.Setenv("SECRET_NAMESPACE", "decisionbox")
	t.Setenv("SECRET_ENCRYPTION_KEY", "")

	// --- Mongo testcontainer (project + secret + turn store) ---
	mongoC, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	defer func() { _ = mongoC.Terminate(ctx) }()
	mongoURI, err := mongoC.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mongo conn string: %v", err)
	}

	const dbName = "agentserver_ask_serve_test"
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

	// Seed a tiny table the model will count, through a provider built the same
	// way the agent builds it.
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

	// --- scripted Ollama /api/chat stub (the reasoning LLM) ---
	// Round 1 -> a query_data action (grounds the turn on real SQL); round 2+
	// -> a grounded answer. Any extra call still terminates, so the turn can
	// never hang waiting on the model.
	const whID = "wh_pg"
	const answerText = "There are exactly 3 events in the warehouse."
	var calls int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var content string
		if atomic.AddInt32(&calls, 1) == 1 {
			content = `{"query":"SELECT count(*) AS n FROM events","datasource_id":"` + whID + `","purpose":"count events"}`
		} else {
			content = `{"answer":"` + answerText + `"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":             "qwen2.5:7b",
			"created_at":        time.Now().UTC().Format(time.RFC3339),
			"message":           map[string]any{"role": "assistant", "content": content},
			"done":              true,
			"done_reason":       "stop",
			"prompt_eval_count": 10,
			"eval_count":        10,
		})
	}))
	defer stub.Close()

	// --- project + per-warehouse credential secret ---
	oid := primitive.NewObjectID()
	projectID := oid.Hex()
	_, err = mongoClient.Collection("projects").InsertOne(ctx, bson.M{
		"_id":      oid,
		"name":     "ask-serve-it",
		"domain":   "test",
		"category": "test",
		"warehouses": bson.A{bson.M{
			"id":       whID,
			"label":    "PG",
			"provider": "postgres",
			"datasets": bson.A{"public"},
			"config":   bson.M{"auth_method": "connection_string"},
		}},
		"primary_warehouse_id": whID,
		// Ollama has no native tool-calling, so the server takes the JSON-text
		// loop; the host points at our scripted stub.
		"llm": bson.M{
			"provider": "ollama",
			"model":    "qwen2.5:7b",
			"config":   bson.M{"host": stub.URL},
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
	if err := secretProvider.Set(ctx, projectID, gowarehouse.CredentialsKey(whID), connStr); err != nil {
		t.Fatalf("seed warehouse-credentials: %v", err)
	}

	// --- serve config: an ephemeral port and short budgets so nothing inherits
	// the 12h production defaults. ---
	port := freeTCPPort(t)
	t.Setenv("ASK_SERVE_PORT", strconv.Itoa(port))
	t.Setenv("ASK_SERVE_WALLCLOCK_SECONDS", "60")
	t.Setenv("ASK_SERVE_QUERY_TIMEOUT_SECONDS", "60")
	t.Setenv("ASK_SERVE_CONNECT_TIMEOUT_SECONDS", "30")
	t.Setenv("ASK_SERVE_MAX_ROUNDS", "6")

	// --- run the mode (blocks until SIGTERM) ---
	cfg := &config.Config{}
	cfg.Service.Name = "ask-serve-it"
	cfg.Service.LogLevel = "warn"
	cfg.MongoDB.URI = mongoURI
	cfg.MongoDB.Database = dbName

	errCh := make(chan error, 1)
	go func() { errCh <- runAskServe(cfg) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHealthz(t, base)

	// --- POST /turns ---
	turnID := uuid.New().String()
	reqBody, _ := json.Marshal(map[string]any{
		"project_id": projectID,
		"turn_id":    turnID,
		"session_id": uuid.New().String(),
		"question":   "How many events are there?",
	})
	postResp, err := http.Post(base+"/turns", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /turns: %v", err)
	}
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /turns status = %d, want 202", postResp.StatusCode)
	}
	_ = postResp.Body.Close()

	// --- poll GET /turns/{id} until terminal ---
	var turn struct {
		Status string `json:"status"`
		Answer string `json:"answer"`
		Error  string `json:"error"`
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		getResp, gErr := http.Get(base + "/turns/" + turnID)
		if gErr == nil && getResp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(getResp.Body).Decode(&turn)
			_ = getResp.Body.Close()
			if turn.Status != "" && turn.Status != "running" {
				break
			}
		} else if getResp != nil {
			_ = getResp.Body.Close()
		}
		time.Sleep(300 * time.Millisecond)
	}

	// --- assert the persisted, grounded answer ---
	// "done" is only reachable after a SUCCESSFUL query grounded the turn — so a
	// pass proves the whole entrypoint path (Mongo/secret wiring, the per-project
	// builder, the lazy Postgres connect + ValidateReadOnly, the executor, and
	// turn persistence) held together end-to-end.
	if turn.Status != "done" {
		t.Fatalf("turn status = %q (error=%q, answer=%q), want done", turn.Status, turn.Error, turn.Answer)
	}
	if turn.Answer != answerText {
		t.Errorf("turn answer = %q, want %q", turn.Answer, answerText)
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("model was called %d times, want >= 2 (query then answer)", got)
	}

	// --- graceful shutdown: SIGTERM is the only stop signal runAskServe wires
	// (signal.NotifyContext); the server is up, so it is caught, not fatal. ---
	if proc, pErr := os.FindProcess(os.Getpid()); pErr == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runAskServe returned error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Error("ask-serve did not shut down within 20s of SIGTERM")
	}
}

// freeTCPPort reserves an ephemeral localhost port and releases it, returning
// the number for the server to bind. The brief gap is a standard test-time
// trade-off; collisions are vanishingly unlikely on a CI host.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// waitHealthz blocks until the serve-mode HTTP server answers GET /healthz, so
// the test never races the goroutine's ListenAndServe (and so the SIGTERM
// handler is registered before we ever raise it).
func waitHealthz(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("ask-serve /healthz did not come up within 30s")
}
