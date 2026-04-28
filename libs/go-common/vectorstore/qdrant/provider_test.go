package qdrant

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"

	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
)

// testUUID generates a random UUID v4 string for test IDs.
func testUUID() string {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		panic(err)
	}
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func TestCollectionName(t *testing.T) {
	tests := []struct {
		dims int
		want string
	}{
		{768, "decisionbox_768"},
		{1024, "decisionbox_1024"},
		{1536, "decisionbox_1536"},
		{3072, "decisionbox_3072"},
	}
	for _, tt := range tests {
		got := CollectionName(tt.dims)
		if got != tt.want {
			t.Errorf("CollectionName(%d) = %q, want %q", tt.dims, got, tt.want)
		}
	}
}

func TestParseCollectionDimensions(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"decisionbox_1536", 1536},
		{"decisionbox_768", 768},
		{"decisionbox_1024", 1024},
		{"other_collection", 0},
		{"decisionbox_", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := ParseCollectionDimensions(tt.name)
		if got != tt.want {
			t.Errorf("ParseCollectionDimensions(%q) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestFloat64sToFloat32s(t *testing.T) {
	in := []float64{1.0, 2.5, 3.14}
	out := float64sToFloat32s(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(out))
	}
	if out[0] != 1.0 || out[1] != 2.5 {
		t.Errorf("unexpected conversion: %v", out)
	}
}

func TestFloat32sToFloat64s(t *testing.T) {
	in := []float32{1.0, 2.5, 3.14}
	out := float32sToFloat64s(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(out))
	}
	if out[0] != 1.0 || out[1] != 2.5 {
		t.Errorf("unexpected conversion: %v", out)
	}
}

func TestEnsureCollection(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	// First call creates the collection
	err := p.EnsureCollection(ctx, 1536)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}
	if !mock.collections["decisionbox_1536"] {
		t.Fatal("expected collection to be created")
	}

	// Second call is a no-op (idempotent)
	err = p.EnsureCollection(ctx, 1536)
	if err != nil {
		t.Fatalf("EnsureCollection (idempotent) failed: %v", err)
	}
}

func TestEnsureCollectionInvalidDimensions(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 0)
	if err == nil {
		t.Fatal("expected error for zero dimensions")
	}

	err = p.EnsureCollection(ctx, -1)
	if err == nil {
		t.Fatal("expected error for negative dimensions")
	}
}

func TestEnsureCollectionError(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	mock.err = fmt.Errorf("connection refused")
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 1536)
	if err == nil {
		t.Fatal("expected error")
	}
	if !containsStr(err.Error(), "connection refused") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertAndSearch(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	// Create collection first
	err := p.EnsureCollection(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	id1 := testUUID()
	id2 := testUUID()

	// Upsert points
	points := []vectorstore.Point{
		{
			ID:     id1,
			Vector: []float64{0.1, 0.2, 0.3},
			Payload: map[string]interface{}{
				"type":       "insight",
				"project_id": "proj-1",
				"severity":   "high",
			},
		},
		{
			ID:     id2,
			Vector: []float64{0.4, 0.5, 0.6},
			Payload: map[string]interface{}{
				"type":       "insight",
				"project_id": "proj-1",
				"severity":   "medium",
			},
		},
	}

	err = p.Upsert(ctx, points)
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Verify points stored
	coll := mock.points["decisionbox_3"]
	if len(coll) != 2 {
		t.Fatalf("expected 2 points, got %d", len(coll))
	}

	// Search with project filter
	results, err := p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Search with severity filter
	results, err = p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		Severity:   "high",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search with severity failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with severity=high, got %d", len(results))
	}
}

func TestUpsertEmptyPoints(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.Upsert(ctx, nil)
	if err != nil {
		t.Fatalf("Upsert nil should be no-op, got: %v", err)
	}

	err = p.Upsert(ctx, []vectorstore.Point{})
	if err != nil {
		t.Fatalf("Upsert empty should be no-op, got: %v", err)
	}
}

func TestUpsertEmptyVector(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.Upsert(ctx, []vectorstore.Point{
		{ID: "00000000-0000-4000-8000-000000000000", Vector: []float64{}},
	})
	if err == nil {
		t.Fatal("expected error for empty vector")
	}
}

func TestSearchEmptyVector(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	_, err := p.Search(ctx, []float64{}, vectorstore.SearchOpts{})
	if err == nil {
		t.Fatal("expected error for empty search vector")
	}
}

func TestSearchWithMinScore(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	err = p.Upsert(ctx, []vectorstore.Point{
		{
			ID:     "p1",
			Vector: []float64{0.1, 0.2, 0.3},
			Payload: map[string]interface{}{
				"project_id": "proj-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Mock returns score 0.85, so min_score 0.9 should filter it out
	results, err := p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		MinScore:   0.9,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results with min_score=0.9, got %d", len(results))
	}

	// min_score 0.5 should return the point (mock score is 0.85)
	results, err = p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		MinScore:   0.5,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with min_score=0.5, got %d", len(results))
	}
}

func TestSearchMultipleProjectIDs(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	err = p.Upsert(ctx, []vectorstore.Point{
		{
			ID:     "p1",
			Vector: []float64{0.1, 0.2, 0.3},
			Payload: map[string]interface{}{
				"project_id": "proj-1",
			},
		},
		{
			ID:     "p2",
			Vector: []float64{0.4, 0.5, 0.6},
			Payload: map[string]interface{}{
				"project_id": "proj-2",
			},
		},
		{
			ID:     "p3",
			Vector: []float64{0.7, 0.8, 0.9},
			Payload: map[string]interface{}{
				"project_id": "proj-3",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Search for two projects
	results, err := p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1", "proj-2"},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for proj-1,proj-2, got %d", len(results))
	}
}

func TestSearchWithTypeFilter(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	err = p.Upsert(ctx, []vectorstore.Point{
		{
			ID:     "i1",
			Vector: []float64{0.1, 0.2, 0.3},
			Payload: map[string]interface{}{
				"type":       "insight",
				"project_id": "proj-1",
			},
		},
		{
			ID:     "r1",
			Vector: []float64{0.4, 0.5, 0.6},
			Payload: map[string]interface{}{
				"type":       "recommendation",
				"project_id": "proj-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Search for insights only
	results, err := p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		Types:      []string{"insight"},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 insight, got %d", len(results))
	}

	// Search for both types (should use Should conditions)
	results, err = p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		Types:      []string{"insight", "recommendation"},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for both types, got %d", len(results))
	}
}

func TestFindDuplicates(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	// Insert an existing insight from discovery-1
	err = p.Upsert(ctx, []vectorstore.Point{
		{
			ID:     "existing-insight",
			Vector: []float64{0.1, 0.2, 0.3},
			Payload: map[string]interface{}{
				"type":         "insight",
				"project_id":   "proj-1",
				"discovery_id": "disc-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Search for duplicates from discovery-2 (should find the existing one)
	results, err := p.FindDuplicates(ctx, []float64{0.1, 0.2, 0.3}, "proj-1", "insight", "disc-2", 0.5)
	if err != nil {
		t.Fatalf("FindDuplicates failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 duplicate, got %d", len(results))
	}
	if results[0].ID != "existing-insight" {
		t.Fatalf("expected existing-insight, got %s", results[0].ID)
	}

	// Search with same discovery ID (should exclude it)
	results, err = p.FindDuplicates(ctx, []float64{0.1, 0.2, 0.3}, "proj-1", "insight", "disc-1", 0.5)
	if err != nil {
		t.Fatalf("FindDuplicates failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results (excluded same discovery), got %d", len(results))
	}
}

func TestFindDuplicatesEmptyVector(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	_, err := p.FindDuplicates(ctx, []float64{}, "proj-1", "insight", "disc-2", 0.95)
	if err == nil {
		t.Fatal("expected error for empty vector")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	// Create a collection and insert a point
	err := p.EnsureCollection(ctx, 1536)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	deleteID := testUUID()
	vec := make([]float64, 1536)
	vec[0] = 0.1
	err = p.Upsert(ctx, []vectorstore.Point{
		{
			ID:      deleteID,
			Vector:  vec,
			Payload: map[string]interface{}{"project_id": "proj-1"},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	if len(mock.points["decisionbox_1536"]) != 1 {
		t.Fatal("expected 1 point before delete")
	}

	// Delete the point
	err = p.Delete(ctx, []string{deleteID})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if len(mock.points["decisionbox_1536"]) != 0 {
		t.Fatal("expected 0 points after delete")
	}
}

func TestDeleteEmpty(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.Delete(ctx, nil)
	if err != nil {
		t.Fatalf("Delete nil should be no-op, got: %v", err)
	}

	err = p.Delete(ctx, []string{})
	if err != nil {
		t.Fatalf("Delete empty should be no-op, got: %v", err)
	}
}

func TestHealthCheck(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.HealthCheck(ctx)
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
}

func TestHealthCheckError(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	mock.err = fmt.Errorf("connection refused")
	p := newWithClient(mock)

	err := p.HealthCheck(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	if !containsStr(err.Error(), "health check failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildFilter(t *testing.T) {
	// Empty opts => nil filter
	filter := buildFilter(vectorstore.SearchOpts{})
	if filter != nil {
		t.Fatal("expected nil filter for empty opts")
	}

	// Single project ID
	filter = buildFilter(vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
	})
	if filter == nil {
		t.Fatal("expected non-nil filter")
	}
	if len(filter.Must) != 1 {
		t.Fatalf("expected 1 must condition, got %d", len(filter.Must))
	}

	// Multiple project IDs
	filter = buildFilter(vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1", "proj-2"},
	})
	if filter == nil {
		t.Fatal("expected non-nil filter")
	}
	if len(filter.Must) != 1 {
		t.Fatalf("expected 1 must condition (keywords), got %d", len(filter.Must))
	}

	// All filters
	filter = buildFilter(vectorstore.SearchOpts{
		ProjectIDs:     []string{"proj-1"},
		Types:          []string{"insight"},
		EmbeddingModel: "text-embedding-3-small",
		Severity:       "high",
		AnalysisArea:   "churn",
	})
	if filter == nil {
		t.Fatal("expected non-nil filter")
	}
	// project_id + type + embedding_model + severity + analysis_area = 5
	if len(filter.Must) != 5 {
		t.Fatalf("expected 5 must conditions, got %d", len(filter.Must))
	}

	// Multiple types => should conditions
	filter = buildFilter(vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
		Types:      []string{"insight", "recommendation"},
	})
	if len(filter.Must) != 1 { // just project_id
		t.Fatalf("expected 1 must condition, got %d", len(filter.Must))
	}
	if len(filter.Should) != 2 { // insight OR recommendation
		t.Fatalf("expected 2 should conditions, got %d", len(filter.Should))
	}
}

func TestUpsertError(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	// No collection created => upsert should fail
	err := p.Upsert(ctx, []vectorstore.Point{
		{
			ID:      "p1",
			Vector:  []float64{0.1, 0.2, 0.3},
			Payload: map[string]interface{}{"project_id": "proj-1"},
		},
	})
	if err == nil {
		t.Fatal("expected error when collection doesn't exist")
	}
}

func TestSearchError(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	mock.err = fmt.Errorf("connection reset")
	p := newWithClient(mock)

	_, err := p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.EnsureCollection(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	// Insert more than 10 points
	for i := range 15 {
		err = p.Upsert(ctx, []vectorstore.Point{
			{
				ID:      fmt.Sprintf("p%d", i),
				Vector:  []float64{float64(i) * 0.1, 0.2, 0.3},
				Payload: map[string]interface{}{"project_id": "proj-1"},
			},
		})
		if err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Search with limit=0 (should default to 10)
	results, err := p.Search(ctx, []float64{0.1, 0.2, 0.3}, vectorstore.SearchOpts{
		ProjectIDs: []string{"proj-1"},
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) > 10 {
		t.Fatalf("expected at most 10 results (default limit), got %d", len(results))
	}
}

func TestPayloadToMap(t *testing.T) {
	// nil payload
	result := payloadToMap(nil)
	if result != nil {
		t.Fatal("expected nil for nil payload")
	}

	// empty payload
	result = payloadToMap(map[string]*pb.Value{})
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %v", result)
	}
}

func TestValueToInterface_AllTypes(t *testing.T) {
	tests := []struct {
		name  string
		value *pb.Value
		want  interface{}
	}{
		{"nil", nil, nil},
		{"string", &pb.Value{Kind: &pb.Value_StringValue{StringValue: "hello"}}, "hello"},
		{"integer", &pb.Value{Kind: &pb.Value_IntegerValue{IntegerValue: 42}}, int64(42)},
		{"double", &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: 3.14}}, 3.14},
		{"bool", &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: true}}, true},
		{"null", &pb.Value{Kind: &pb.Value_NullValue{}}, nil},
		{"list", &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{
			Values: []*pb.Value{
				{Kind: &pb.Value_StringValue{StringValue: "a"}},
				{Kind: &pb.Value_IntegerValue{IntegerValue: 1}},
			},
		}}}, []interface{}{"a", int64(1)}},
		{"nil list", &pb.Value{Kind: &pb.Value_ListValue{ListValue: nil}}, nil},
		{"struct", &pb.Value{Kind: &pb.Value_StructValue{StructValue: &pb.Struct{
			Fields: map[string]*pb.Value{
				"key": {Kind: &pb.Value_StringValue{StringValue: "val"}},
			},
		}}}, map[string]interface{}{"key": "val"}},
		{"nil struct", &pb.Value{Kind: &pb.Value_StructValue{StructValue: nil}}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valueToInterface(tt.value)
			// Deep comparison for slices and maps
			gotStr := fmt.Sprintf("%v", got)
			wantStr := fmt.Sprintf("%v", tt.want)
			if gotStr != wantStr {
				t.Errorf("valueToInterface(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestPointIDToString_Numeric(t *testing.T) {
	numID := &pb.PointId{PointIdOptions: &pb.PointId_Num{Num: 12345}}
	got := pointIDToString(numID)
	if got != "12345" {
		t.Errorf("pointIDToString(num=12345) = %q, want %q", got, "12345")
	}

	nilID := pointIDToString(nil)
	if nilID != "" {
		t.Errorf("pointIDToString(nil) = %q, want empty", nilID)
	}
}

func TestClose(t *testing.T) {
	mock := newMockClient()
	p := newWithClient(mock)

	err := p.Close()
	if err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// SearchSchemaIndex tests cover the per-project schema-blurb collection
// (decisionbox_schema_{projectID}) lookup added for pack-gen. Without
// these the unit-test signal for the new method was zero (the
// integration test against a real qdrant container exercised it but
// codecov runs unit tests only).

func TestSearchSchemaIndex_HappyPath(t *testing.T) {
	mock := newMockClient()
	p := newWithClient(mock)
	ctx := context.Background()

	const projectID = "p1"
	collName := schemaCollectionName(projectID)
	mock.collections[collName] = true
	mock.points[collName] = map[string]*pb.PointStruct{
		"pt-1": {
			Id: pb.NewID("pt-1"),
			Payload: map[string]*pb.Value{
				"schema_key": pb.NewValueString("dbo.orders"),
				"blurb":      pb.NewValueString("Customer orders."),
			},
		},
	}

	hits, err := p.SearchSchemaIndex(ctx, projectID, []float64{1, 2, 3}, 5)
	if err != nil {
		t.Fatalf("SearchSchemaIndex: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].Payload["schema_key"] != "dbo.orders" {
		t.Errorf("schema_key payload = %v, want dbo.orders", hits[0].Payload["schema_key"])
	}
}

func TestSearchSchemaIndex_RejectsMissingProjectID(t *testing.T) {
	p := newWithClient(newMockClient())
	if _, err := p.SearchSchemaIndex(context.Background(), "", []float64{1, 2, 3}, 5); err == nil {
		t.Fatal("expected error for empty projectID")
	}
}

func TestSearchSchemaIndex_RejectsEmptyVector(t *testing.T) {
	p := newWithClient(newMockClient())
	if _, err := p.SearchSchemaIndex(context.Background(), "p1", nil, 5); err == nil {
		t.Fatal("expected error for empty vector")
	}
}

func TestSearchSchemaIndex_DefaultsTopK(t *testing.T) {
	mock := newMockClient()
	p := newWithClient(mock)
	const projectID = "p1"
	mock.collections[schemaCollectionName(projectID)] = true
	mock.points[schemaCollectionName(projectID)] = map[string]*pb.PointStruct{}
	// topK=0 should default to 20 inside SearchSchemaIndex; just verify
	// the call returns without error and does not panic on the empty
	// collection.
	if _, err := p.SearchSchemaIndex(context.Background(), projectID, []float64{1, 2, 3}, 0); err != nil {
		t.Fatalf("topK=0 should default; got error %v", err)
	}
}

func TestSearchSchemaIndex_ReturnsNilWhenCollectionAbsent(t *testing.T) {
	// Project hasn't built a schema index yet — the per-project
	// collection doesn't exist. Method must return (nil, nil) so
	// callers fall back to a non-semantic heuristic instead of
	// surfacing the underlying "not found" error.
	mock := newMockClient()
	p := newWithClient(mock)
	hits, err := p.SearchSchemaIndex(context.Background(), "missing-proj", []float64{1, 2, 3}, 5)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if hits != nil {
		t.Errorf("expected nil hits when collection is absent, got %v", hits)
	}
}

func TestSearchSchemaIndex_PropagatesUnexpectedErrors(t *testing.T) {
	// A non-"not found" client error must surface to the caller.
	mock := newMockClient()
	mock.err = errors.New("transient: connection refused")
	p := newWithClient(mock)
	if _, err := p.SearchSchemaIndex(context.Background(), "p1", []float64{1, 2, 3}, 5); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestSchemaCollectionName(t *testing.T) {
	want := "decisionbox_schema_abc123"
	if got := schemaCollectionName("abc123"); got != want {
		t.Errorf("schemaCollectionName(%q) = %q, want %q", "abc123", got, want)
	}
}

// Verify Provider satisfies vectorstore.Provider interface.
var _ vectorstore.Provider = (*Provider)(nil)
