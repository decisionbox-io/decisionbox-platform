//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/handler"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/decisionbox-io/decisionbox/services/api/internal/server"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
)

var testServer *httptest.Server
var testDB *database.DB

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer container.Terminate(ctx)

	uri, _ := container.ConnectionString(ctx)
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = uri
	mongoCfg.Database = "api_integration_test"

	client, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)

	testDB = database.New(client)

	// Initialize database (same as production startup)
	if err := database.InitDatabase(ctx, testDB); err != nil {
		fmt.Fprintf(os.Stderr, "InitDatabase failed: %v\n", err)
		os.Exit(1)
	}

	testServer = httptest.NewServer(server.New(testDB, nil, nil, auth.NewNoAuthProvider()))
	defer testServer.Close()

	os.Exit(m.Run())
}

func doRequest(t *testing.T, method, path string, body interface{}) *http.Response {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req, _ := http.NewRequest(method, testServer.URL+path, reqBody)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func decodeResponse(t *testing.T, resp *http.Response) handler.APIResponse {
	t.Helper()
	var r handler.APIResponse
	json.NewDecoder(resp.Body).Decode(&r)
	resp.Body.Close()
	return r
}

// --- Health ---

func TestInteg_Health(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/health", nil)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// --- Domains ---

func TestInteg_ListDomains(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/domains", nil)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	r := decodeResponse(t, resp)
	domains := r.Data.([]interface{})
	if len(domains) == 0 {
		t.Error("should return gaming domain")
	}
}

func TestInteg_ListCategories(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/domains/gaming/categories", nil)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestInteg_GetProfileSchema(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/domains/gaming/categories/match3/schema", nil)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestInteg_GetAnalysisAreas(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/domains/gaming/categories/match3/areas", nil)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestInteg_DomainNotFound(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/domains/nonexistent/categories", nil)
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// --- Projects CRUD ---

func TestInteg_ProjectCRUD(t *testing.T) {
	// Create
	project := models.Project{
		Name:     "Test Game",
		Domain:   "gaming",
		Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"test"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-20250514"},
	}

	resp := doRequest(t, "POST", "/api/v1/projects", project)
	if resp.StatusCode != 201 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	r := decodeResponse(t, resp)
	created := r.Data.(map[string]interface{})
	id := created["id"].(string)
	if id == "" {
		t.Fatal("should return ID")
	}

	// Get
	resp = doRequest(t, "GET", "/api/v1/projects/"+id, nil)
	if resp.StatusCode != 200 {
		t.Errorf("get status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	got := r.Data.(map[string]interface{})
	if got["name"] != "Test Game" {
		t.Errorf("name = %v", got["name"])
	}

	// List
	resp = doRequest(t, "GET", "/api/v1/projects", nil)
	if resp.StatusCode != 200 {
		t.Errorf("list status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	list := r.Data.([]interface{})
	if len(list) == 0 {
		t.Error("should have projects")
	}

	// Update
	project.Name = "Updated Game"
	resp = doRequest(t, "PUT", "/api/v1/projects/"+id, project)
	if resp.StatusCode != 200 {
		t.Errorf("update status = %d", resp.StatusCode)
	}

	// Delete
	resp = doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)
	if resp.StatusCode != 200 {
		t.Errorf("delete status = %d", resp.StatusCode)
	}

	// Get after delete — should be gone
	resp = doRequest(t, "GET", "/api/v1/projects/"+id, nil)
	if resp.StatusCode != 404 {
		t.Errorf("after delete status = %d, want 404", resp.StatusCode)
	}
}

func TestInteg_ProjectCreate_Validation(t *testing.T) {
	// Missing name
	resp := doRequest(t, "POST", "/api/v1/projects", models.Project{Domain: "gaming", Category: "match3"})
	if resp.StatusCode != 400 {
		t.Errorf("missing name: status = %d, want 400", resp.StatusCode)
	}

	// Missing domain
	resp = doRequest(t, "POST", "/api/v1/projects", models.Project{Name: "Test"})
	if resp.StatusCode != 400 {
		t.Errorf("missing domain: status = %d, want 400", resp.StatusCode)
	}
}

func TestInteg_ProjectNotFound(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/projects/000000000000000000000000", nil)
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// --- Discoveries ---

func TestInteg_DiscoveryEndpoints(t *testing.T) {
	// Create a project first
	project := models.Project{
		Name: "Disc Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"test"}},
		LLM: models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)

	// No discoveries yet
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries/latest", nil)
	if resp.StatusCode != 404 {
		t.Errorf("latest before discovery: status = %d, want 404", resp.StatusCode)
	}

	// List empty
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries", nil)
	if resp.StatusCode != 200 {
		t.Errorf("list status = %d", resp.StatusCode)
	}

	// Trigger discovery (202 if agent binary available, 500 if not)
	resp = doRequest(t, "POST", "/api/v1/projects/"+id+"/discover", nil)
	if resp.StatusCode != 202 && resp.StatusCode != 500 {
		t.Errorf("trigger status = %d, want 202 or 500", resp.StatusCode)
	}

	// Status
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/status", nil)
	if resp.StatusCode != 200 {
		t.Errorf("status endpoint = %d", resp.StatusCode)
	}

	// Invalid date
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries/not-a-date", nil)
	if resp.StatusCode != 400 {
		t.Errorf("invalid date: status = %d, want 400", resp.StatusCode)
	}

	// Insert a discovery directly to test retrieval
	discCol := testDB.Collection("discoveries")
	disc := models.DiscoveryResult{
		ProjectID: id, Domain: "gaming", Category: "match3",
		DiscoveryDate: time.Now(), TotalSteps: 50,
		Insights: []models.Insight{{ID: "i1", AnalysisArea: "churn", Name: "Test"}},
		Summary:  models.Summary{TotalInsights: 1},
		CreatedAt: time.Now(),
	}
	discCol.InsertOne(context.Background(), disc)

	// Now latest should work
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries/latest", nil)
	if resp.StatusCode != 200 {
		t.Errorf("latest after insert: status = %d", resp.StatusCode)
	}

	// By date
	today := time.Now().Format("2006-01-02")
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries/"+today, nil)
	if resp.StatusCode != 200 {
		t.Errorf("by date: status = %d", resp.StatusCode)
	}

	// Cleanup
	doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)
}

// --- Database Init ---

func TestInteg_InitDatabase_Collections(t *testing.T) {
	ctx := context.Background()

	// Verify all expected collections exist by listing them
	colNames, err := testDB.Collection("projects").Database().ListCollectionNames(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("ListCollectionNames error: %v", err)
	}

	expected := []string{"projects", "discoveries", "project_context", "discovery_debug_logs", "domain_packs"}
	for _, name := range expected {
		found := false
		for _, col := range colNames {
			if col == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collection %q not found in %v", name, colNames)
		}
	}
}

func TestInteg_InitDatabase_Idempotent(t *testing.T) {
	// Calling InitDatabase again should not fail
	err := database.InitDatabase(context.Background(), testDB)
	if err != nil {
		t.Errorf("second InitDatabase should be idempotent: %v", err)
	}
}

func TestInteg_InitDatabase_Indexes(t *testing.T) {
	ctx := context.Background()

	// Check that indexes exist on the discoveries collection
	cursor, err := testDB.Collection("discoveries").Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes error: %v", err)
	}
	defer cursor.Close(ctx)

	var indexes []map[string]interface{}
	if err := cursor.All(ctx, &indexes); err != nil {
		t.Fatalf("decode indexes error: %v", err)
	}

	// Should have at least 3 indexes (default _id + our 3)
	if len(indexes) < 3 {
		t.Errorf("discoveries indexes = %d, want >= 3", len(indexes))
	}
}

// --- Prompts ---

func TestInteg_ProjectPrompts_SeededOnCreate(t *testing.T) {
	project := models.Project{
		Name: "Prompt Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	if resp.StatusCode != 201 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Get prompts — should be seeded from domain pack
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/prompts", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get prompts status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	prompts := r.Data.(map[string]interface{})

	if prompts["exploration"] == nil || prompts["exploration"] == "" {
		t.Error("exploration prompt should be seeded")
	}
	if prompts["recommendations"] == nil || prompts["recommendations"] == "" {
		t.Error("recommendations prompt should be seeded")
	}

	areas, ok := prompts["analysis_areas"].(map[string]interface{})
	if !ok || len(areas) == 0 {
		t.Fatal("analysis_areas should be seeded")
	}
	if _, ok := areas["churn"]; !ok {
		t.Error("churn area should be seeded")
	}
	if _, ok := areas["levels"]; !ok {
		t.Error("levels area should be seeded (match3 category)")
	}
}

func TestInteg_ProjectPrompts_Update(t *testing.T) {
	project := models.Project{
		Name: "Prompt Update Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Update prompts
	updatedPrompts := models.ProjectPrompts{
		Exploration:     "custom exploration prompt",
		Recommendations: "custom recs",
		AnalysisAreas: map[string]models.AnalysisAreaConfig{
			"churn": {Name: "My Churn", Prompt: "custom churn", Enabled: true, Priority: 1},
		},
	}
	resp = doRequest(t, "PUT", "/api/v1/projects/"+id+"/prompts", updatedPrompts)
	if resp.StatusCode != 200 {
		t.Fatalf("update prompts status = %d", resp.StatusCode)
	}

	// Verify
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/prompts", nil)
	r = decodeResponse(t, resp)
	prompts := r.Data.(map[string]interface{})
	if prompts["exploration"] != "custom exploration prompt" {
		t.Errorf("exploration = %v", prompts["exploration"])
	}
}

func TestInteg_ProjectPrompts_NotFound(t *testing.T) {
	resp := doRequest(t, "GET", "/api/v1/projects/000000000000000000000000/prompts", nil)
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// --- Selective Discovery ---

func TestInteg_TriggerDiscovery_WithAreas(t *testing.T) {
	project := models.Project{
		Name: "Selective Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Trigger with specific areas
	resp = doRequest(t, "POST", "/api/v1/projects/"+id+"/discover",
		map[string]interface{}{"areas": []string{"churn", "levels"}})

	// Should return 202 (or 500 if agent binary not available — still accepted)
	if resp.StatusCode != 202 && resp.StatusCode != 500 {
		t.Errorf("trigger status = %d, want 202 or 500", resp.StatusCode)
	}
}

func TestInteg_TriggerDiscovery_NoAreas_FullRun(t *testing.T) {
	project := models.Project{
		Name: "Full Run Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Trigger without areas — should be full run
	resp = doRequest(t, "POST", "/api/v1/projects/"+id+"/discover", nil)
	if resp.StatusCode != 202 && resp.StatusCode != 500 {
		t.Errorf("trigger status = %d", resp.StatusCode)
	}
}

func TestInteg_TriggerDiscovery_AlreadyRunning(t *testing.T) {
	project := models.Project{
		Name: "Conflict Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Trigger first — may fail (agent binary not available) but run record is created
	firstResp := doRequest(t, "POST", "/api/v1/projects/"+id+"/discover", nil)

	// If the first trigger succeeded (created a run record), second should conflict
	if firstResp.StatusCode == 202 {
		resp = doRequest(t, "POST", "/api/v1/projects/"+id+"/discover", nil)
		if resp.StatusCode != 409 {
			t.Errorf("second trigger status = %d, want 409", resp.StatusCode)
		}
	}
	// If first failed (500 — no agent binary), the run was marked as failed,
	// so second trigger won't conflict. This is expected in test env.
}

// --- Discovery Result Fields ---

func TestInteg_DiscoveryResult_RunTypeAndAreas(t *testing.T) {
	project := models.Project{
		Name: "RunType Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Insert a full discovery
	discCol := testDB.Collection("discoveries")
	fullDisc := models.DiscoveryResult{
		ProjectID: id, Domain: "gaming", Category: "match3",
		RunType: "full", DiscoveryDate: time.Now().Add(-time.Hour),
		TotalSteps: 100, Insights: []models.Insight{
			{ID: "i1", AnalysisArea: "churn", Name: "Full Churn"},
			{ID: "i2", AnalysisArea: "engagement", Name: "Full Engagement"},
		},
		Summary: models.Summary{TotalInsights: 2}, CreatedAt: time.Now().Add(-time.Hour),
	}
	discCol.InsertOne(context.Background(), fullDisc)

	// Insert a partial discovery
	partialDisc := models.DiscoveryResult{
		ProjectID: id, Domain: "gaming", Category: "match3",
		RunType: "partial", AreasRequested: []string{"churn"},
		DiscoveryDate: time.Now(), TotalSteps: 20,
		Insights: []models.Insight{{ID: "i3", AnalysisArea: "churn", Name: "Partial Churn"}},
		Summary: models.Summary{TotalInsights: 1}, CreatedAt: time.Now(),
	}
	discCol.InsertOne(context.Background(), partialDisc)

	// Get latest — should be the partial one
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries/latest", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("latest status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	latest := r.Data.(map[string]interface{})

	if latest["run_type"] != "partial" {
		t.Errorf("run_type = %v, want partial", latest["run_type"])
	}

	areasReq, ok := latest["areas_requested"].([]interface{})
	if !ok || len(areasReq) != 1 || areasReq[0] != "churn" {
		t.Errorf("areas_requested = %v, want [churn]", latest["areas_requested"])
	}

	// List discoveries — should have both
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	list := r.Data.([]interface{})
	if len(list) < 2 {
		t.Errorf("discoveries = %d, want >= 2", len(list))
	}

	// Verify list includes run_type
	for _, d := range list {
		dm := d.(map[string]interface{})
		rt := dm["run_type"]
		if rt != "full" && rt != "partial" {
			t.Errorf("discovery run_type = %v, want full or partial", rt)
		}
	}
}

func TestInteg_DiscoveryResult_FullRunNoAreas(t *testing.T) {
	project := models.Project{
		Name: "Full NoAreas", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	discCol := testDB.Collection("discoveries")
	disc := models.DiscoveryResult{
		ProjectID: id, Domain: "gaming", Category: "match3",
		RunType: "full", DiscoveryDate: time.Now(), TotalSteps: 50,
		Summary: models.Summary{TotalInsights: 0}, CreatedAt: time.Now(),
	}
	discCol.InsertOne(context.Background(), disc)

	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries/latest", nil)
	r = decodeResponse(t, resp)
	latest := r.Data.(map[string]interface{})

	if latest["run_type"] != "full" {
		t.Errorf("run_type = %v, want full", latest["run_type"])
	}
	// areas_requested should be nil/empty for full runs
	if areasReq, ok := latest["areas_requested"].([]interface{}); ok && len(areasReq) > 0 {
		t.Errorf("areas_requested should be empty for full run, got %v", areasReq)
	}
}

func TestInteg_DiscoveryList_Ordering(t *testing.T) {
	project := models.Project{
		Name: "Order Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	discCol := testDB.Collection("discoveries")

	// Insert 3 discoveries with different dates
	for i, steps := range []int{10, 20, 30} {
		disc := models.DiscoveryResult{
			ProjectID: id, Domain: "gaming", Category: "match3",
			RunType: "full", DiscoveryDate: time.Now().Add(time.Duration(i) * time.Hour),
			TotalSteps: steps, Summary: models.Summary{TotalInsights: steps},
			CreatedAt: time.Now().Add(time.Duration(i) * time.Hour),
		}
		discCol.InsertOne(context.Background(), disc)
	}

	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries", nil)
	r = decodeResponse(t, resp)
	list := r.Data.([]interface{})

	if len(list) < 3 {
		t.Fatalf("discoveries = %d, want >= 3", len(list))
	}

	// Most recent should be first (sorted by discovery_date desc)
	first := list[0].(map[string]interface{})
	if int(first["total_steps"].(float64)) != 30 {
		t.Errorf("first discovery steps = %v, want 30 (most recent)", first["total_steps"])
	}
}

func TestInteg_TriggerDiscovery_WithMaxSteps(t *testing.T) {
	project := models.Project{
		Name: "MaxSteps Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Trigger with max_steps
	resp = doRequest(t, "POST", "/api/v1/projects/"+id+"/discover",
		map[string]interface{}{"max_steps": 25})

	// 202 if agent binary available, 500 if not
	if resp.StatusCode != 202 && resp.StatusCode != 500 {
		t.Errorf("trigger with max_steps status = %d", resp.StatusCode)
	}
}

func TestInteg_TriggerDiscovery_WithAreasAndMaxSteps(t *testing.T) {
	project := models.Project{
		Name: "Areas+Steps Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Trigger with both areas and max_steps
	resp = doRequest(t, "POST", "/api/v1/projects/"+id+"/discover",
		map[string]interface{}{
			"areas":     []string{"churn"},
			"max_steps": 15,
		})

	if resp.StatusCode != 202 && resp.StatusCode != 500 {
		t.Errorf("trigger with areas+steps status = %d", resp.StatusCode)
	}
}

func TestInteg_DiscoveryList_ExcludesHeavyFields(t *testing.T) {
	project := models.Project{
		Name: "Projection Test", Domain: "gaming", Category: "match3",
		Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}},
		LLM:       models.LLMConfig{Provider: "claude", Model: "test"},
	}
	resp := doRequest(t, "POST", "/api/v1/projects", project)
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	discCol := testDB.Collection("discoveries")
	disc := models.DiscoveryResult{
		ProjectID: id, Domain: "gaming", Category: "match3",
		RunType: "full", DiscoveryDate: time.Now(), TotalSteps: 10,
		Summary: models.Summary{TotalInsights: 1}, CreatedAt: time.Now(),
	}
	discCol.InsertOne(context.Background(), disc)

	// List should exclude heavy log fields
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/discoveries", nil)
	r = decodeResponse(t, resp)
	list := r.Data.([]interface{})
	if len(list) == 0 {
		t.Fatal("no discoveries")
	}

	first := list[0].(map[string]interface{})
	// These heavy fields should be excluded from list (projection in repo)
	if _, ok := first["exploration_log"]; ok {
		t.Error("exploration_log should be excluded from list endpoint")
	}
	if _, ok := first["analysis_log"]; ok {
		t.Error("analysis_log should be excluded from list endpoint")
	}
}

// --- Run Cancel ---

func TestInteg_CancelRun_NotFound(t *testing.T) {
	resp := doRequest(t, "DELETE", "/api/v1/runs/000000000000000000000000", nil)
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// --- Feedback ---

func TestInteg_Feedback_CRUD(t *testing.T) {
	// Create a project first
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "feedback-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)

	// Use a fake discovery ID
	discoveryID := "feed-disc-test-123"

	// 1. List feedback (empty)
	resp = doRequest(t, "GET", "/api/v1/discoveries/"+discoveryID+"/feedback", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	items := r.Data.([]interface{})
	if len(items) != 0 {
		t.Errorf("expected empty feedback, got %d", len(items))
	}

	// 2. Submit a like
	resp = doRequest(t, "POST", "/api/v1/discoveries/"+discoveryID+"/feedback", map[string]interface{}{
		"project_id":  projectID,
		"target_type": "insight",
		"target_id":   "0",
		"rating":      "like",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("submit like status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	fb := r.Data.(map[string]interface{})
	feedbackID := fb["id"].(string)
	if fb["rating"] != "like" {
		t.Errorf("rating = %v, want like", fb["rating"])
	}
	if fb["target_type"] != "insight" {
		t.Errorf("target_type = %v", fb["target_type"])
	}

	// 3. List feedback (1 item)
	resp = doRequest(t, "GET", "/api/v1/discoveries/"+discoveryID+"/feedback", nil)
	r = decodeResponse(t, resp)
	items = r.Data.([]interface{})
	if len(items) != 1 {
		t.Errorf("expected 1 feedback, got %d", len(items))
	}

	// 4. Upsert — change to dislike with comment
	resp = doRequest(t, "POST", "/api/v1/discoveries/"+discoveryID+"/feedback", map[string]interface{}{
		"project_id":  projectID,
		"target_type": "insight",
		"target_id":   "0",
		"rating":      "dislike",
		"comment":     "not actionable",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("upsert status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	fb = r.Data.(map[string]interface{})
	if fb["rating"] != "dislike" {
		t.Errorf("rating after upsert = %v, want dislike", fb["rating"])
	}
	if fb["comment"] != "not actionable" {
		t.Errorf("comment = %v", fb["comment"])
	}

	// 5. Still 1 item (upsert, not duplicate)
	resp = doRequest(t, "GET", "/api/v1/discoveries/"+discoveryID+"/feedback", nil)
	r = decodeResponse(t, resp)
	items = r.Data.([]interface{})
	if len(items) != 1 {
		t.Errorf("expected 1 feedback after upsert, got %d", len(items))
	}

	// 6. Add recommendation feedback
	resp = doRequest(t, "POST", "/api/v1/discoveries/"+discoveryID+"/feedback", map[string]interface{}{
		"project_id":  projectID,
		"target_type": "recommendation",
		"target_id":   "2",
		"rating":      "like",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("submit rec feedback status = %d", resp.StatusCode)
	}

	// 7. Now 2 items
	resp = doRequest(t, "GET", "/api/v1/discoveries/"+discoveryID+"/feedback", nil)
	r = decodeResponse(t, resp)
	items = r.Data.([]interface{})
	if len(items) != 2 {
		t.Errorf("expected 2 feedback, got %d", len(items))
	}

	// 8. Delete the first feedback
	resp = doRequest(t, "DELETE", "/api/v1/feedback/"+feedbackID, nil)
	if resp.StatusCode != 200 {
		t.Errorf("delete status = %d", resp.StatusCode)
	}

	// 9. Back to 1
	resp = doRequest(t, "GET", "/api/v1/discoveries/"+discoveryID+"/feedback", nil)
	r = decodeResponse(t, resp)
	items = r.Data.([]interface{})
	if len(items) != 1 {
		t.Errorf("expected 1 feedback after delete, got %d", len(items))
	}
}

func TestInteg_Feedback_Validation(t *testing.T) {
	// Invalid rating
	resp := doRequest(t, "POST", "/api/v1/discoveries/test-run/feedback", map[string]interface{}{
		"target_type": "insight", "target_id": "0", "rating": "meh",
	})
	if resp.StatusCode != 400 {
		t.Errorf("invalid rating: status = %d, want 400", resp.StatusCode)
	}

	// Invalid target type
	resp = doRequest(t, "POST", "/api/v1/discoveries/test-run/feedback", map[string]interface{}{
		"target_type": "sql_query", "target_id": "0", "rating": "like",
	})
	if resp.StatusCode != 400 {
		t.Errorf("invalid target_type: status = %d, want 400", resp.StatusCode)
	}

	// Missing fields
	resp = doRequest(t, "POST", "/api/v1/discoveries/test-run/feedback", map[string]interface{}{
		"rating": "like",
	})
	if resp.StatusCode != 400 {
		t.Errorf("missing fields: status = %d, want 400", resp.StatusCode)
	}
}

func TestInteg_Feedback_ExplorationStep(t *testing.T) {
	// exploration_step is a valid target type
	resp := doRequest(t, "POST", "/api/v1/discoveries/test-step-run/feedback", map[string]interface{}{
		"target_type": "exploration_step",
		"target_id":   "3",
		"rating":      "like",
	})
	if resp.StatusCode != 200 {
		t.Errorf("exploration_step feedback: status = %d, want 200", resp.StatusCode)
	}

	// Verify it's stored
	resp = doRequest(t, "GET", "/api/v1/discoveries/test-step-run/feedback", nil)
	r := decodeResponse(t, resp)
	items := r.Data.([]interface{})
	found := false
	for _, item := range items {
		fb := item.(map[string]interface{})
		if fb["target_type"] == "exploration_step" && fb["target_id"] == "3" {
			found = true
		}
	}
	if !found {
		t.Error("exploration_step feedback not found")
	}
}

func TestInteg_ProjectUpdate_PreservesPrompts(t *testing.T) {
	// Create project (prompts auto-seeded)
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "merge-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)

	// Verify prompts were seeded
	resp = doRequest(t, "GET", "/api/v1/projects/"+projectID+"/prompts", nil)
	r = decodeResponse(t, resp)
	prompts := r.Data.(map[string]interface{})
	if prompts["exploration"] == "" {
		t.Fatal("prompts not seeded on create")
	}
	areas := prompts["analysis_areas"].(map[string]interface{})
	if len(areas) == 0 {
		t.Fatal("analysis areas not seeded")
	}

	// Update project settings WITHOUT sending prompts
	resp = doRequest(t, "PUT", "/api/v1/projects/"+projectID, map[string]interface{}{
		"name": "merge-test-updated",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test2", "datasets": []string{"ds2"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test2"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("update status = %d", resp.StatusCode)
	}

	// Verify prompts are STILL present (not wiped)
	resp = doRequest(t, "GET", "/api/v1/projects/"+projectID+"/prompts", nil)
	r = decodeResponse(t, resp)
	prompts = r.Data.(map[string]interface{})
	if prompts["exploration"] == "" {
		t.Error("prompts wiped after settings update — merge logic broken")
	}
	areas = prompts["analysis_areas"].(map[string]interface{})
	if len(areas) == 0 {
		t.Error("analysis areas wiped after settings update")
	}

	// Verify name was updated
	resp = doRequest(t, "GET", "/api/v1/projects/"+projectID, nil)
	r = decodeResponse(t, resp)
	proj := r.Data.(map[string]interface{})
	if proj["name"] != "merge-test-updated" {
		t.Errorf("name = %v, want merge-test-updated", proj["name"])
	}
}

func TestInteg_ProjectUpdate_PreservesProfile(t *testing.T) {
	// Create project with profile
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "profile-merge", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
		"profile":   map[string]interface{}{"basic_info": map[string]interface{}{"genre": "puzzle"}},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)

	// Update only name (no profile in body)
	resp = doRequest(t, "PUT", "/api/v1/projects/"+projectID, map[string]interface{}{
		"name": "profile-merge-updated",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("update status = %d", resp.StatusCode)
	}

	// Verify profile preserved
	resp = doRequest(t, "GET", "/api/v1/projects/"+projectID, nil)
	r = decodeResponse(t, resp)
	proj := r.Data.(map[string]interface{})
	profile := proj["profile"].(map[string]interface{})
	if profile["basic_info"] == nil {
		t.Error("profile wiped after update without profile field")
	}
}

// --- Pricing ---

func TestInteg_Pricing_SeededFromProviders(t *testing.T) {
	// Pricing should be auto-seeded on startup from registered providers
	resp := doRequest(t, "GET", "/api/v1/pricing", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("pricing status = %d", resp.StatusCode)
	}
	r := decodeResponse(t, resp)
	data := r.Data.(map[string]interface{})

	// Should have LLM providers
	llm := data["llm"].(map[string]interface{})
	if len(llm) == 0 {
		t.Error("no LLM pricing seeded")
	}

	// Should have claude pricing
	if _, ok := llm["claude"]; !ok {
		t.Error("claude pricing not seeded")
	}

	// Should have warehouse pricing
	warehouse := data["warehouse"].(map[string]interface{})
	if len(warehouse) == 0 {
		t.Error("no warehouse pricing seeded")
	}
}

func TestInteg_Pricing_Update(t *testing.T) {
	resp := doRequest(t, "PUT", "/api/v1/pricing", map[string]interface{}{
		"llm": map[string]interface{}{
			"claude": map[string]interface{}{
				"custom-model": map[string]interface{}{
					"input_per_million": 99.0, "output_per_million": 199.0,
				},
			},
		},
		"warehouse": map[string]interface{}{
			"bigquery": map[string]interface{}{
				"cost_model": "per_byte_scanned", "cost_per_tb_scanned_usd": 5.0,
			},
		},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("update pricing status = %d", resp.StatusCode)
	}

	// Verify it was saved
	resp = doRequest(t, "GET", "/api/v1/pricing", nil)
	r := decodeResponse(t, resp)
	data := r.Data.(map[string]interface{})
	llm := data["llm"].(map[string]interface{})
	claude := llm["claude"].(map[string]interface{})
	custom := claude["custom-model"].(map[string]interface{})
	if custom["input_per_million"].(float64) != 99.0 {
		t.Errorf("custom model pricing not saved: %v", custom)
	}
}

// --- Insights & Recommendations ---

func TestInteg_Insights_CRUD(t *testing.T) {
	// Create a project
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "insights-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+projectID, nil)

	// Seed insights directly into DB
	insightsCol := testDB.Collection("insights")
	now := time.Now()
	for i, name := range []string{"High churn at L45", "Session length drop", "Revenue spike"} {
		insightsCol.InsertOne(context.Background(), map[string]interface{}{
			"_id":           fmt.Sprintf("ins-%d", i),
			"project_id":    projectID,
			"discovery_id":  "disc-1",
			"name":          name,
			"description":   "Test insight " + name,
			"severity":      "high",
			"analysis_area": "churn",
			"confidence":    0.85,
			"created_at":    now.Add(time.Duration(i) * time.Minute),
		})
	}

	// List insights
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/insights", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list insights status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	insights := r.Data.([]interface{})
	if len(insights) != 3 {
		t.Errorf("expected 3 insights, got %d", len(insights))
	}

	// List with limit
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/insights?limit=2", projectID), nil)
	r = decodeResponse(t, resp)
	insights = r.Data.([]interface{})
	if len(insights) != 2 {
		t.Errorf("expected 2 insights with limit, got %d", len(insights))
	}

	// Get single insight
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/insights/ins-0", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get insight status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	insight := r.Data.(map[string]interface{})
	if insight["name"] != "High churn at L45" {
		t.Errorf("insight name = %v", insight["name"])
	}

	// Get insight from wrong project → 404
	resp = doRequest(t, "GET", "/api/v1/projects/wrong-project/insights/ins-0", nil)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for wrong project, got %d", resp.StatusCode)
	}

	// Get nonexistent insight → 404
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/insights/nonexistent", projectID), nil)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for nonexistent insight, got %d", resp.StatusCode)
	}
}

func TestInteg_Recommendations_CRUD(t *testing.T) {
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "recs-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+projectID, nil)

	recsCol := testDB.Collection("recommendations")
	now := time.Now()
	for i, title := range []string{"Add retries", "Reduce difficulty"} {
		recsCol.InsertOne(context.Background(), map[string]interface{}{
			"_id":          fmt.Sprintf("rec-%d", i),
			"project_id":   projectID,
			"discovery_id": "disc-1",
			"title":        title,
			"description":  "Test rec " + title,
			"priority":     i + 1,
			"confidence":   0.78,
			"created_at":   now.Add(time.Duration(i) * time.Minute),
		})
	}

	// List
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/recommendations", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list recs status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	recs := r.Data.([]interface{})
	if len(recs) != 2 {
		t.Errorf("expected 2 recs, got %d", len(recs))
	}

	// Get single
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/recommendations/rec-0", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get rec status = %d", resp.StatusCode)
	}

	// Wrong project → 404
	resp = doRequest(t, "GET", "/api/v1/projects/wrong/recommendations/rec-0", nil)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for wrong project, got %d", resp.StatusCode)
	}
}

// --- Search (without Qdrant — graceful degradation) ---

func TestInteg_Search_NoQdrant(t *testing.T) {
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "search-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+projectID, nil)

	// Search without Qdrant configured → 503
	resp = doRequest(t, "POST", fmt.Sprintf("/api/v1/projects/%s/search", projectID),
		map[string]interface{}{"query": "test"})
	if resp.StatusCode != 503 {
		t.Errorf("expected 503 (no Qdrant), got %d", resp.StatusCode)
	}

	// Ask without Qdrant → 503
	resp = doRequest(t, "POST", fmt.Sprintf("/api/v1/projects/%s/ask", projectID),
		map[string]interface{}{"question": "test"})
	if resp.StatusCode != 503 {
		t.Errorf("expected 503 (no Qdrant), got %d", resp.StatusCode)
	}

	// Cross-project search without Qdrant → 503
	resp = doRequest(t, "POST", "/api/v1/search",
		map[string]interface{}{"query": "test", "embedding_model": "test"})
	if resp.StatusCode != 503 {
		t.Errorf("expected 503 (no Qdrant), got %d", resp.StatusCode)
	}
}

// --- Search History ---

func TestInteg_SearchHistory(t *testing.T) {
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "history-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+projectID, nil)

	// History should be empty initially
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/search/history", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("history status = %d", resp.StatusCode)
	}

	// Invalid limit → 400
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/search/history?limit=abc", projectID), nil)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for invalid limit, got %d", resp.StatusCode)
	}
}

// --- Ask Sessions ---

func TestInteg_AskSessions(t *testing.T) {
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "session-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "project_id": "test", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	r := decodeResponse(t, resp)
	projectID := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+projectID, nil)

	// List sessions (empty)
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/ask/sessions", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list sessions status = %d", resp.StatusCode)
	}

	// Seed a session directly
	sessionsCol := testDB.Collection("ask_sessions")
	sessionsCol.InsertOne(context.Background(), map[string]interface{}{
		"_id":           "test-session-1",
		"project_id":    projectID,
		"user_id":       "anonymous",
		"title":         "Why is churn high?",
		"messages":      []interface{}{},
		"message_count": 0,
		"created_at":    time.Now(),
		"updated_at":    time.Now(),
	})

	// List sessions (1 item)
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/ask/sessions", projectID), nil)
	r = decodeResponse(t, resp)
	sessions := r.Data.([]interface{})
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}

	// Get session
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/ask/sessions/test-session-1", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get session status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	session := r.Data.(map[string]interface{})
	if session["title"] != "Why is churn high?" {
		t.Errorf("session title = %v", session["title"])
	}

	// Get session from wrong project → 404
	resp = doRequest(t, "GET", "/api/v1/projects/wrong-proj/ask/sessions/test-session-1", nil)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for wrong project, got %d", resp.StatusCode)
	}

	// Delete session
	resp = doRequest(t, "DELETE", fmt.Sprintf("/api/v1/projects/%s/ask/sessions/test-session-1", projectID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete session status = %d", resp.StatusCode)
	}

	// Verify deleted
	resp = doRequest(t, "GET", fmt.Sprintf("/api/v1/projects/%s/ask/sessions/test-session-1", projectID), nil)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 after delete, got %d", resp.StatusCode)
	}
}

// --- InitDatabase — New Collections ---

func TestInteg_InitDatabase_VectorSearchCollections(t *testing.T) {
	ctx := context.Background()
	colNames, err := testDB.Collection("projects").Database().ListCollectionNames(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"insights", "recommendations", "search_history", "ask_sessions"} {
		found := false
		for _, col := range colNames {
			if col == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collection %q not found — InitDatabase should create it", name)
		}
	}
}

// --- Domain Pack CRUD ---

func TestInteg_DomainPack_CRUD(t *testing.T) {
	// List — should have seeded packs
	resp := doRequest(t, "GET", "/api/v1/domain-packs", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	r := decodeResponse(t, resp)
	packs := r.Data.([]interface{})
	if len(packs) < 3 {
		t.Errorf("seeded packs = %d, want >= 3 (gaming, ecommerce, social)", len(packs))
	}

	// Get gaming pack
	resp = doRequest(t, "GET", "/api/v1/domain-packs/gaming", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get gaming status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	gaming := r.Data.(map[string]interface{})
	if gaming["slug"] != "gaming" {
		t.Errorf("slug = %v", gaming["slug"])
	}
	cats := gaming["categories"].([]interface{})
	if len(cats) != 3 {
		t.Errorf("gaming categories = %d, want 3", len(cats))
	}

	// Get not found
	resp = doRequest(t, "GET", "/api/v1/domain-packs/nonexistent", nil)
	if resp.StatusCode != 404 {
		t.Errorf("get nonexistent status = %d, want 404", resp.StatusCode)
	}

	// Create a new pack
	newPack := map[string]interface{}{
		"slug":         "test-integ",
		"name":         "Integration Test Pack",
		"description":  "Pack for integration testing",
		"version":      "1.0.0",
		"is_published": false,
		"categories": []map[string]interface{}{
			{"id": "default", "name": "Default", "description": "Default category"},
		},
		"prompts": map[string]interface{}{
			"base": map[string]interface{}{
				"base_context":    "## Profile\n\n{{PROFILE}}\n\n{{PREVIOUS_CONTEXT}}",
				"exploration":     "Explore {{DATASET}} using {{SCHEMA_INFO}} {{FILTER}} {{FILTER_CONTEXT}} {{FILTER_RULE}} {{ANALYSIS_AREAS}}",
				"recommendations": "Recommend based on {{INSIGHTS_DATA}} {{INSIGHTS_SUMMARY}} {{DISCOVERY_DATE}}",
			},
			"categories": map[string]interface{}{},
		},
		"analysis_areas": map[string]interface{}{
			"base": []map[string]interface{}{
				{
					"id": "test-area", "name": "Test Area", "description": "Test",
					"keywords": []string{"test"}, "priority": 1,
					"prompt": "Analyze {{DATASET}} {{TOTAL_QUERIES}} {{QUERY_RESULTS}}",
				},
			},
			"categories": map[string]interface{}{},
		},
		"profile_schema": map[string]interface{}{
			"base":       map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			"categories": map[string]interface{}{},
		},
	}
	resp = doRequest(t, "POST", "/api/v1/domain-packs", newPack)
	if resp.StatusCode != 201 {
		body := decodeResponse(t, resp)
		t.Fatalf("create status = %d, error = %v", resp.StatusCode, body.Error)
	}
	r = decodeResponse(t, resp)
	created := r.Data.(map[string]interface{})
	if created["slug"] != "test-integ" {
		t.Errorf("created slug = %v", created["slug"])
	}
	if created["id"] == nil || created["id"] == "" {
		t.Error("created pack should have id")
	}

	// Duplicate create should fail
	resp = doRequest(t, "POST", "/api/v1/domain-packs", newPack)
	if resp.StatusCode != 409 {
		t.Errorf("duplicate create status = %d, want 409", resp.StatusCode)
	}

	// Update
	newPack["name"] = "Updated Pack"
	resp = doRequest(t, "PUT", "/api/v1/domain-packs/test-integ", newPack)
	if resp.StatusCode != 200 {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	updated := r.Data.(map[string]interface{})
	if updated["name"] != "Updated Pack" {
		t.Errorf("updated name = %v", updated["name"])
	}
	if updated["id"] == nil || updated["id"] == "" {
		t.Error("updated pack should have id preserved")
	}

	// Export
	resp = doRequest(t, "GET", "/api/v1/domain-packs/test-integ/export", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("export status = %d", resp.StatusCode)
	}
	r = decodeResponse(t, resp)
	exported := r.Data.(map[string]interface{})
	if exported["format"] != "decisionbox-domain-pack" {
		t.Errorf("export format = %v", exported["format"])
	}
	exportedPack := exported["pack"].(map[string]interface{})
	if exportedPack["slug"] != "test-integ" {
		t.Errorf("exported slug = %v", exportedPack["slug"])
	}

	// Delete
	resp = doRequest(t, "DELETE", "/api/v1/domain-packs/test-integ", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	// Verify deleted
	resp = doRequest(t, "GET", "/api/v1/domain-packs/test-integ", nil)
	if resp.StatusCode != 404 {
		t.Errorf("after delete status = %d, want 404", resp.StatusCode)
	}
}

func TestInteg_DomainPack_Import(t *testing.T) {
	portable := map[string]interface{}{
		"format":         "decisionbox-domain-pack",
		"format_version": 1,
		"pack": map[string]interface{}{
			"slug":         "test-import",
			"name":         "Imported Pack",
			"version":      "1.0.0",
			"is_published": false,
			"categories": []map[string]interface{}{
				{"id": "default", "name": "Default", "description": "Default"},
			},
			"prompts": map[string]interface{}{
				"base": map[string]interface{}{
					"base_context":    "{{PROFILE}} {{PREVIOUS_CONTEXT}}",
					"exploration":     "{{DATASET}} {{SCHEMA_INFO}} {{FILTER}} {{FILTER_CONTEXT}} {{FILTER_RULE}} {{ANALYSIS_AREAS}}",
					"recommendations": "{{INSIGHTS_DATA}} {{INSIGHTS_SUMMARY}} {{DISCOVERY_DATE}}",
				},
				"categories": map[string]interface{}{},
			},
			"analysis_areas": map[string]interface{}{
				"base": []map[string]interface{}{
					{"id": "area1", "name": "Area 1", "description": "Test", "keywords": []string{"test"}, "priority": 1, "prompt": "{{DATASET}} {{TOTAL_QUERIES}} {{QUERY_RESULTS}}"},
				},
				"categories": map[string]interface{}{},
			},
			"profile_schema": map[string]interface{}{
				"base":       map[string]interface{}{},
				"categories": map[string]interface{}{},
			},
		},
	}

	resp := doRequest(t, "POST", "/api/v1/domain-packs/import", portable)
	if resp.StatusCode != 201 {
		body := decodeResponse(t, resp)
		t.Fatalf("import status = %d, error = %v", resp.StatusCode, body.Error)
	}
	r := decodeResponse(t, resp)
	imported := r.Data.(map[string]interface{})
	if imported["slug"] != "test-import" {
		t.Errorf("imported slug = %v", imported["slug"])
	}

	// Cleanup
	doRequest(t, "DELETE", "/api/v1/domain-packs/test-import", nil)
}

func TestInteg_DomainPack_ValidationErrors(t *testing.T) {
	// Missing slug
	resp := doRequest(t, "POST", "/api/v1/domain-packs", map[string]interface{}{
		"name": "No Slug",
	})
	if resp.StatusCode != 400 {
		t.Errorf("missing slug: status = %d, want 400", resp.StatusCode)
	}

	// Invalid slug format
	resp = doRequest(t, "POST", "/api/v1/domain-packs", map[string]interface{}{
		"slug": "Bad Slug!", "name": "Test",
	})
	if resp.StatusCode != 400 {
		t.Errorf("invalid slug: status = %d, want 400", resp.StatusCode)
	}

	// Missing categories
	resp = doRequest(t, "POST", "/api/v1/domain-packs", map[string]interface{}{
		"slug": "test-valid", "name": "Test", "categories": []interface{}{},
	})
	if resp.StatusCode != 400 {
		t.Errorf("missing categories: status = %d, want 400", resp.StatusCode)
	}

	// Invalid import format
	resp = doRequest(t, "POST", "/api/v1/domain-packs/import", map[string]interface{}{
		"format": "wrong-format",
	})
	if resp.StatusCode != 400 {
		t.Errorf("wrong format: status = %d, want 400", resp.StatusCode)
	}
}

func TestInteg_DomainPack_SeededPacksAreReadable(t *testing.T) {
	// Verify each seeded pack has valid structure
	for _, slug := range []string{"gaming", "ecommerce", "social"} {
		resp := doRequest(t, "GET", "/api/v1/domain-packs/"+slug, nil)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status = %d", slug, resp.StatusCode)
			continue
		}
		r := decodeResponse(t, resp)
		pack := r.Data.(map[string]interface{})

		if pack["slug"] != slug {
			t.Errorf("%s: slug = %v", slug, pack["slug"])
		}
		cats := pack["categories"].([]interface{})
		if len(cats) == 0 {
			t.Errorf("%s: no categories", slug)
		}

		prompts := pack["prompts"].(map[string]interface{})
		base := prompts["base"].(map[string]interface{})
		if base["exploration"] == nil || base["exploration"] == "" {
			t.Errorf("%s: empty exploration prompt", slug)
		}
		if base["recommendations"] == nil || base["recommendations"] == "" {
			t.Errorf("%s: empty recommendations prompt", slug)
		}

		areas := pack["analysis_areas"].(map[string]interface{})
		baseAreas := areas["base"].([]interface{})
		if len(baseAreas) == 0 {
			t.Errorf("%s: no base analysis areas", slug)
		}
	}
}

func TestInteg_SearchHistory_TTLIndex(t *testing.T) {
	ctx := context.Background()
	cursor, err := testDB.Collection("search_history").Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close(ctx)

	var indexes []map[string]interface{}
	if err := cursor.All(ctx, &indexes); err != nil {
		t.Fatal(err)
	}

	foundTTL := false
	for _, idx := range indexes {
		if ttl, ok := idx["expireAfterSeconds"]; ok {
			ttlVal := int(ttl.(int32))
			expected := 90 * 24 * 60 * 60 // 90 days
			if ttlVal != expected {
				t.Errorf("TTL = %d, want %d (90 days)", ttlVal, expected)
			}
			foundTTL = true
		}
	}
	if !foundTTL {
		t.Error("search_history should have a TTL index")
	}
}

func TestInteg_DomainPack_ProjectCreationUsesDBPack(t *testing.T) {
	// Create a project with gaming domain — prompts should be seeded from DB pack
	resp := doRequest(t, "POST", "/api/v1/projects", map[string]interface{}{
		"name": "dp-seed-test", "domain": "gaming", "category": "match3",
		"warehouse": map[string]interface{}{"provider": "bigquery", "datasets": []string{"ds"}},
		"llm":       map[string]interface{}{"provider": "claude", "model": "test"},
	})
	if resp.StatusCode != 201 {
		body := decodeResponse(t, resp)
		t.Fatalf("create project status = %d, error = %v", resp.StatusCode, body.Error)
	}
	r := decodeResponse(t, resp)
	id := r.Data.(map[string]interface{})["id"].(string)
	defer doRequest(t, "DELETE", "/api/v1/projects/"+id, nil)

	// Get prompts — verify they came from the gaming domain pack
	resp = doRequest(t, "GET", "/api/v1/projects/"+id+"/prompts", nil)
	r = decodeResponse(t, resp)
	prompts := r.Data.(map[string]interface{})

	// Should have gaming-specific base areas
	areas := prompts["analysis_areas"].(map[string]interface{})
	for _, expected := range []string{"churn", "engagement", "monetization"} {
		if _, ok := areas[expected]; !ok {
			t.Errorf("missing base area %q from gaming pack", expected)
		}
	}
	// Should have match3-specific areas
	for _, expected := range []string{"levels", "boosters"} {
		if _, ok := areas[expected]; !ok {
			t.Errorf("missing match3 area %q from gaming pack", expected)
		}
	}

	// Exploration should include match3 context (appended)
	exploration := prompts["exploration"].(string)
	if len(exploration) < 100 {
		t.Error("exploration prompt seems too short — may not include category context")
	}
}

func TestInteg_InitDatabase_DomainPacksCollection(t *testing.T) {
	ctx := context.Background()

	// Verify domain_packs collection and indexes exist
	cursor, err := testDB.Collection("domain_packs").Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes error: %v", err)
	}
	defer cursor.Close(ctx)

	var indexes []map[string]interface{}
	if err := cursor.All(ctx, &indexes); err != nil {
		t.Fatalf("decode indexes error: %v", err)
	}

	// Should have _id + slug (unique) + is_published + created_at = 4
	if len(indexes) < 4 {
		t.Errorf("domain_packs indexes = %d, want >= 4", len(indexes))
	}

	// Verify slug index is unique
	for _, idx := range indexes {
		key := idx["key"].(map[string]interface{})
		if _, ok := key["slug"]; ok {
			unique, _ := idx["unique"].(bool)
			if !unique {
				t.Error("slug index should be unique")
			}
		}
	}
}

// --- CORS ---

func TestInteg_CORS(t *testing.T) {
	req, _ := http.NewRequest("OPTIONS", testServer.URL+"/api/v1/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}
