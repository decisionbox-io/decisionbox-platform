//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	bq "cloud.google.com/go/bigquery"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	bqprovider "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"

	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/modules/gcloud"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/social/go"
)

var (
	testDB        *database.DB
	testWarehouse gowarehouse.Provider
)

const testBQProjectID = "test-project"
const testBQDataset   = "test_dataset"

func TestMain(m *testing.M) {
	bgCtx := context.Background()
	code := 1

	applog.Init("agent-integration-test", "warn")

	// Start MongoDB
	mongoContainer, err := tcmongo.Run(bgCtx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer mongoContainer.Terminate(bgCtx)

	mongoURI, _ := mongoContainer.ConnectionString(bgCtx)
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = mongoURI
	mongoCfg.Database = "agent_integration_test"
	mongoClient, err := gomongo.NewClient(bgCtx, mongoCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer mongoClient.Disconnect(bgCtx)
	testDB = database.New(mongoClient)

	// Start BigQuery emulator
	bqContainer, err := gcloud.RunBigQuery(bgCtx,
		"ghcr.io/goccy/bigquery-emulator:0.6.1",
		gcloud.WithProjectID(testBQProjectID),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "BigQuery emulator start failed: %v\n", err)
		os.Exit(1)
	}
	defer bqContainer.Terminate(bgCtx)

	// Create BigQuery client for seeding
	bqClient, err := bq.NewClient(bgCtx, testBQProjectID,
		option.WithEndpoint(bqContainer.URI),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithoutAuthentication(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "BigQuery client failed: %v\n", err)
		os.Exit(1)
	}

	if err := seedBigQuery(bgCtx, bqClient); err != nil {
		fmt.Fprintf(os.Stderr, "BigQuery seed failed: %v\n", err)
		os.Exit(1)
	}
	bqClient.Close()

	// Create warehouse provider pointing to emulator
	testWarehouse, err = bqprovider.NewBigQueryProvider(bgCtx, bqprovider.BigQueryConfig{
		ProjectID: testBQProjectID,
		Dataset:   testBQDataset,
		Timeout:   30 * time.Second,
		ClientOptions: []option.ClientOption{
			option.WithEndpoint(bqContainer.URI),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
			option.WithoutAuthentication(),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "BigQuery provider failed: %v\n", err)
		os.Exit(1)
	}
	defer testWarehouse.Close()

	code = m.Run()
	os.Exit(code)
}

func seedBigQuery(ctx context.Context, client *bq.Client) error {
	ds := client.Dataset(testBQDataset)
	if err := ds.Create(ctx, &bq.DatasetMetadata{Location: "US"}); err != nil {
		return fmt.Errorf("create dataset: %w", err)
	}

	schema := bq.Schema{
		{Name: "app_id", Type: bq.StringFieldType, Required: true},
		{Name: "user_id", Type: bq.StringFieldType, Required: true},
		{Name: "session_id", Type: bq.StringFieldType, Required: true},
		{Name: "start_time", Type: bq.TimestampFieldType},
		{Name: "duration_seconds", Type: bq.IntegerFieldType},
		{Name: "level", Type: bq.IntegerFieldType},
	}

	table := ds.Table("sessions")
	if err := table.Create(ctx, &bq.TableMetadata{Schema: schema}); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	ins := table.Inserter()
	rows := []*bq.ValuesSaver{
		{Schema: schema, Row: []bq.Value{"test-app", "user1", "s1", time.Now(), 120, 5}},
		{Schema: schema, Row: []bq.Value{"test-app", "user2", "s2", time.Now(), 300, 10}},
		{Schema: schema, Row: []bq.Value{"test-app", "user3", "s3", time.Now(), 60, 3}},
		{Schema: schema, Row: []bq.Value{"test-app", "user4", "s4", time.Now(), 450, 15}},
		{Schema: schema, Row: []bq.Value{"test-app", "user5", "s5", time.Now(), 180, 8}},
	}
	if err := ins.Put(ctx, rows); err != nil {
		return fmt.Errorf("insert rows: %w", err)
	}

	return nil
}

// =====================================================================
// Provider Registry Tests
// =====================================================================

func TestInteg_ProviderRegistries(t *testing.T) {
	t.Run("claude LLM registered", func(t *testing.T) {
		found := false
		for _, p := range gollm.RegisteredProviders() {
			if p == "claude" { found = true }
		}
		if !found { t.Fatal("claude not registered") }
	})

	t.Run("bigquery warehouse registered", func(t *testing.T) {
		found := false
		for _, p := range gowarehouse.RegisteredProviders() {
			if p == "bigquery" { found = true }
		}
		if !found { t.Fatal("bigquery not registered") }
	})

	t.Run("unknown LLM provider errors", func(t *testing.T) {
		_, err := gollm.NewProvider("nonexistent", gollm.ProviderConfig{"api_key": "x"})
		if err == nil { t.Error("expected error") }
	})

	t.Run("unknown warehouse provider errors", func(t *testing.T) {
		_, err := gowarehouse.NewProvider("nonexistent", gowarehouse.ProviderConfig{"project_id": "x"})
		if err == nil { t.Error("expected error") }
	})
}

// =====================================================================
// BigQuery Warehouse Provider Tests (real emulator)
// =====================================================================

func TestInteg_BigQuery_Query(t *testing.T) {
	result, err := testWarehouse.Query(context.Background(),
		fmt.Sprintf("SELECT * FROM `%s.sessions` WHERE app_id = 'test-app'", testBQDataset), nil)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(result.Rows) != 5 {
		t.Errorf("rows = %d, want 5", len(result.Rows))
	}
}

func TestInteg_BigQuery_QueryWithFilter(t *testing.T) {
	result, err := testWarehouse.Query(context.Background(),
		fmt.Sprintf("SELECT user_id FROM `%s.sessions` WHERE app_id = 'test-app' AND duration_seconds > 100", testBQDataset), nil)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(result.Rows) != 4 {
		t.Errorf("rows = %d, want 4 (users 1,2,4,5)", len(result.Rows))
	}
}

func TestInteg_BigQuery_ListTables(t *testing.T) {
	tables, err := testWarehouse.ListTables(context.Background())
	if err != nil {
		t.Fatalf("ListTables error: %v", err)
	}
	found := false
	for _, tbl := range tables {
		if tbl == "sessions" { found = true }
	}
	if !found {
		t.Errorf("sessions not found in %v", tables)
	}
}

func TestInteg_BigQuery_GetTableSchema(t *testing.T) {
	schema, err := testWarehouse.GetTableSchema(context.Background(), "sessions")
	if err != nil {
		t.Fatalf("GetTableSchema error: %v", err)
	}
	if schema.Name != "sessions" {
		t.Errorf("name = %q", schema.Name)
	}
	if len(schema.Columns) == 0 {
		t.Fatal("no columns")
	}

	colMap := make(map[string]string)
	for _, col := range schema.Columns {
		colMap[col.Name] = col.Type
	}
	if colMap["app_id"] != "STRING" {
		t.Errorf("app_id type = %q", colMap["app_id"])
	}
	if colMap["duration_seconds"] != "INTEGER" {
		t.Errorf("duration_seconds type = %q", colMap["duration_seconds"])
	}
}

func TestInteg_BigQuery_SQLDialect(t *testing.T) {
	dialect := testWarehouse.SQLDialect()
	if dialect != "BigQuery Standard SQL" {
		t.Errorf("dialect = %q", dialect)
	}
}

func TestInteg_BigQuery_SQLFixPrompt(t *testing.T) {
	prompt := testWarehouse.SQLFixPrompt()
	if prompt == "" {
		t.Error("SQLFixPrompt should not be empty")
	}
}

func TestInteg_BigQuery_HealthCheck(t *testing.T) {
	if err := testWarehouse.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck error: %v", err)
	}
}

// =====================================================================
// Query Executor with BigQuery Emulator
// =====================================================================

func TestInteg_QueryExecutor_WithBigQuery(t *testing.T) {
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   testWarehouse,
		FilterField: "app_id",
		FilterValue: "test-app",
	})

	result, err := executor.Execute(context.Background(),
		fmt.Sprintf("SELECT COUNT(DISTINCT user_id) as cnt FROM `%s.sessions` WHERE app_id = 'test-app'", testBQDataset),
		"count users")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.RowCount != 1 {
		t.Errorf("RowCount = %d, want 1", result.RowCount)
	}
}

func TestInteg_QueryExecutor_FilterEnforcement(t *testing.T) {
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   testWarehouse,
		FilterField: "app_id",
		FilterValue: "test-app",
	})

	_, err := executor.Execute(context.Background(),
		fmt.Sprintf("SELECT * FROM `%s.sessions`", testBQDataset),
		"missing filter")
	if err == nil {
		t.Error("should reject query without filter field")
	}
}

func TestInteg_QueryExecutor_NoFilterRequired(t *testing.T) {
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse: testWarehouse,
		// No FilterField — no enforcement
	})

	result, err := executor.Execute(context.Background(),
		fmt.Sprintf("SELECT COUNT(*) as cnt FROM `%s.sessions`", testBQDataset),
		"no filter needed")
	if err != nil {
		t.Fatalf("should pass without filter: %v", err)
	}
	if result.RowCount != 1 {
		t.Errorf("RowCount = %d", result.RowCount)
	}
}

// =====================================================================
// UserCountValidator with BigQuery Emulator
// =====================================================================

func TestInteg_UserCountValidator_WithBigQuery(t *testing.T) {
	v := validation.NewUserCountValidator(validation.UserCountValidatorOptions{
		Warehouse: testWarehouse,
		Dataset:   testBQDataset,
		Filter:    "WHERE app_id = 'test-app'",
	})

	total, err := v.GetTotalUsers(context.Background())
	if err != nil {
		t.Fatalf("GetTotalUsers error: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}

	// Validate an insight with count within total
	insights := []models.Insight{
		{ID: "1", Name: "Test", AffectedCount: 3, AnalysisArea: "churn"},
	}
	results := v.ValidateInsights(context.Background(), insights)
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].Status != "confirmed" {
		t.Errorf("status = %q, want confirmed", results[0].Status)
	}
}

// =====================================================================
// InsightValidator with BigQuery + Mock LLM
// =====================================================================

func TestInteg_InsightValidator_WithBigQuery(t *testing.T) {
	llmProvider := testutil.NewMockLLMProvider()
	// LLM generates a valid verification query
	llmProvider.DefaultResponse.Content = fmt.Sprintf(
		"SELECT COUNT(DISTINCT user_id) as count FROM `%s.sessions` WHERE app_id = 'test-app'",
		testBQDataset)

	aiClient, _ := ai.New(llmProvider, "test-model")

	v := validation.NewInsightValidator(validation.InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: testWarehouse,
		Dataset:   testBQDataset,
		Filter:    "WHERE app_id = 'test-app'",
	})

	insights := []models.Insight{
		{ID: "1", Name: "Test Churn", AffectedCount: 5, AnalysisArea: "churn"},
	}

	results := v.ValidateInsights(context.Background(), insights)
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].Status != "confirmed" {
		t.Errorf("status = %q, want confirmed (5 users match 5 in warehouse)", results[0].Status)
	}
	if results[0].Query == "" {
		t.Error("verification query should be captured")
	}
	if results[0].VerifiedCount != 5 {
		t.Errorf("verified = %d, want 5", results[0].VerifiedCount)
	}
}

// =====================================================================
// MongoDB Repository Tests
// =====================================================================

func TestInteg_ProjectRepo(t *testing.T) {
	col := testDB.Collection("projects")
	_, err := col.InsertOne(context.Background(), models.Project{
		ID: "integ-proj-1", Name: "Test Game", Domain: "gaming", Category: "match3",
		Status: "active", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	repo := database.NewProjectRepository(testDB)
	got, err := repo.GetByID(context.Background(), "integ-proj-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Test Game" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestInteg_ContextRepo(t *testing.T) {
	repo := database.NewContextRepository(testDB)
	repo.EnsureIndexes(context.Background())

	pctx := models.NewProjectContext("integ-ctx-1")
	pctx.RecordDiscovery(true)
	pctx.AddNote("test", "integration note", 0.9)

	if err := repo.Save(context.Background(), pctx); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.GetByProjectID(context.Background(), "integ-ctx-1")
	if err != nil {
		t.Fatalf("GetByProjectID: %v", err)
	}
	if got.TotalDiscoveries != 1 {
		t.Errorf("TotalDiscoveries = %d", got.TotalDiscoveries)
	}
}

func TestInteg_DiscoveryRepo(t *testing.T) {
	repo := database.NewDiscoveryRepository(testDB)
	repo.EnsureIndexes(context.Background())

	result := &models.DiscoveryResult{
		ProjectID: "integ-disc-1", Domain: "gaming", Category: "match3",
		DiscoveryDate: time.Now(), TotalSteps: 42,
		Insights: []models.Insight{{ID: "i1", AnalysisArea: "churn", Name: "Test"}},
		AnalysisLog: []models.AnalysisStep{
			{AreaID: "churn", Prompt: "test prompt", Response: "{}", TokensIn: 500, TokensOut: 200},
		},
	}

	if err := repo.Save(context.Background(), result); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.GetLatest(context.Background(), "integ-disc-1")
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if got.TotalSteps != 42 {
		t.Errorf("TotalSteps = %d", got.TotalSteps)
	}
	if len(got.AnalysisLog) != 1 {
		t.Errorf("AnalysisLog = %d", len(got.AnalysisLog))
	}
	if got.AnalysisLog[0].TokensIn != 500 {
		t.Errorf("TokensIn = %d", got.AnalysisLog[0].TokensIn)
	}
}
