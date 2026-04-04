package qdrant

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/qdrant/go-client/qdrant"

	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
)

// Config holds the Qdrant connection settings.
type Config struct {
	Host   string // gRPC host (e.g., "localhost")
	Port   int    // gRPC port (e.g., 6334)
	APIKey string // optional API key for secured instances
	UseTLS bool   // use TLS for connection
}

// qdrantClient wraps the Qdrant operations we use.
// Exists for testability — the real client implements this, and tests use a mock.
type qdrantClient interface {
	CollectionExists(ctx context.Context, collectionName string) (bool, error)
	CreateCollection(ctx context.Context, request *pb.CreateCollection) error
	ListCollections(ctx context.Context) ([]string, error)
	Upsert(ctx context.Context, request *pb.UpsertPoints) (*pb.UpdateResult, error)
	Query(ctx context.Context, request *pb.QueryPoints) ([]*pb.ScoredPoint, error)
	Delete(ctx context.Context, request *pb.DeletePoints) (*pb.UpdateResult, error)
	HealthCheck(ctx context.Context) (*pb.HealthCheckReply, error)
	Close() error
}

// Provider implements vectorstore.Provider using Qdrant.
type Provider struct {
	client qdrantClient
}

// New creates a new Qdrant vectorstore provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("qdrant: host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 6334
	}

	client, err := pb.NewClient(&pb.Config{
		Host:   cfg.Host,
		Port:   cfg.Port,
		APIKey: cfg.APIKey,
		UseTLS: cfg.UseTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant: failed to create client: %w", err)
	}

	return &Provider{client: client}, nil
}

// newWithClient creates a Provider with an injected client (for testing).
func newWithClient(client qdrantClient) *Provider {
	return &Provider{client: client}
}

// collectionName returns the Qdrant collection name for a given vector dimension.
func collectionName(dimensions int) string {
	return fmt.Sprintf("decisionbox_%d", dimensions)
}

// EnsureCollection creates the collection if it doesn't exist.
func (p *Provider) EnsureCollection(ctx context.Context, dimensions int) error {
	if dimensions <= 0 {
		return fmt.Errorf("qdrant: dimensions must be positive, got %d", dimensions)
	}

	name := collectionName(dimensions)
	exists, err := p.client.CollectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("qdrant: failed to check collection %q: %w", name, err)
	}
	if exists {
		return nil
	}

	err = p.client.CreateCollection(ctx, &pb.CreateCollection{
		CollectionName: name,
		VectorsConfig: pb.NewVectorsConfig(&pb.VectorParams{
			Size:     uint64(dimensions),
			Distance: pb.Distance_Cosine,
		}),
	})
	if err != nil {
		return fmt.Errorf("qdrant: failed to create collection %q: %w", name, err)
	}

	return nil
}

// Upsert stores vectors with metadata. Idempotent by ID.
func (p *Provider) Upsert(ctx context.Context, points []vectorstore.Point) error {
	if len(points) == 0 {
		return nil
	}

	// Group points by vector dimension to route to the correct collection.
	byDims := make(map[int][]*pb.PointStruct)
	for _, pt := range points {
		dims := len(pt.Vector)
		if dims == 0 {
			return fmt.Errorf("qdrant: point %q has empty vector", pt.ID)
		}

		payload, err := pb.TryValueMap(pt.Payload)
		if err != nil {
			return fmt.Errorf("qdrant: failed to convert payload for point %q: %w", pt.ID, err)
		}

		byDims[dims] = append(byDims[dims], &pb.PointStruct{
			Id:      pb.NewID(pt.ID),
			Vectors: pb.NewVectorsDense(float64sToFloat32s(pt.Vector)),
			Payload: payload,
		})
	}

	waitUpsert := true
	for dims, pts := range byDims {
		name := collectionName(dims)
		_, err := p.client.Upsert(ctx, &pb.UpsertPoints{
			CollectionName: name,
			Wait:           &waitUpsert,
			Points:         pts,
		})
		if err != nil {
			return fmt.Errorf("qdrant: failed to upsert %d points to %q: %w", len(pts), name, err)
		}
	}

	return nil
}

// Search finds vectors similar to the query vector, with optional filters.
func (p *Provider) Search(ctx context.Context, vector []float64, opts vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("qdrant: search vector is empty")
	}

	name := collectionName(len(vector))
	limit := uint64(10)
	if opts.Limit > 0 {
		limit = uint64(opts.Limit) //nolint:gosec // Limit is a small positive int, no overflow risk
	}

	query := &pb.QueryPoints{
		CollectionName: name,
		Query:          pb.NewQueryDense(float64sToFloat32s(vector)),
		Filter:         buildFilter(opts),
		Limit:          &limit,
		WithPayload:    pb.NewWithPayload(true),
	}

	if opts.MinScore > 0 {
		threshold := float32(opts.MinScore)
		query.ScoreThreshold = &threshold
	}

	scored, err := p.client.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("qdrant: search failed in %q: %w", name, err)
	}

	results := make([]vectorstore.SearchResult, 0, len(scored))
	for _, sp := range scored {
		results = append(results, vectorstore.SearchResult{
			ID:      pointIDToString(sp.Id),
			Score:   float64(sp.Score),
			Payload: payloadToMap(sp.Payload),
		})
	}

	return results, nil
}

// FindDuplicates searches for existing vectors above the similarity threshold.
func (p *Provider) FindDuplicates(ctx context.Context, vector []float64, projectID string, docType string, excludeDiscoveryID string, threshold float64) ([]vectorstore.SearchResult, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("qdrant: duplicate search vector is empty")
	}

	name := collectionName(len(vector))
	scoreThreshold := float32(threshold)

	filter := &pb.Filter{
		Must: []*pb.Condition{
			pb.NewMatch("project_id", projectID),
			pb.NewMatch("type", docType),
		},
		MustNot: []*pb.Condition{
			pb.NewMatch("discovery_id", excludeDiscoveryID),
		},
	}

	dupLimit := uint64(1)
	scored, err := p.client.Query(ctx, &pb.QueryPoints{
		CollectionName: name,
		Query:          pb.NewQueryDense(float64sToFloat32s(vector)),
		Filter:         filter,
		Limit:          &dupLimit,
		ScoreThreshold: &scoreThreshold,
		WithPayload:    pb.NewWithPayload(true),
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant: duplicate search failed in %q: %w", name, err)
	}

	results := make([]vectorstore.SearchResult, 0, len(scored))
	for _, sp := range scored {
		results = append(results, vectorstore.SearchResult{
			ID:      pointIDToString(sp.Id),
			Score:   float64(sp.Score),
			Payload: payloadToMap(sp.Payload),
		})
	}

	return results, nil
}

// Delete removes vectors by ID.
func (p *Provider) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	// We don't know the dimensions of deleted points, so we need to try all known collections.
	// In practice, the caller knows the dimensions. For simplicity, we delete from all
	// collections that exist — Qdrant handles non-existent IDs gracefully.
	// Group by collection is not possible here, so we use a best-effort approach.
	// The caller should pass the collection implicitly via the vector dimension.
	// For now, we accept this limitation — Delete is rarely called.

	pointIDs := make([]*pb.PointId, len(ids))
	for i, id := range ids {
		pointIDs[i] = pb.NewID(id)
	}

	// List all DecisionBox collections and try deleting from each.
	// Qdrant silently ignores non-existent point IDs.
	collections, err := p.client.ListCollections(ctx)
	if err != nil {
		return fmt.Errorf("qdrant: failed to list collections: %w", err)
	}

	for _, name := range collections {
		if ParseCollectionDimensions(name) == 0 {
			continue // not a DecisionBox collection
		}
		_, err = p.client.Delete(ctx, &pb.DeletePoints{
			CollectionName: name,
			Points:         pb.NewPointsSelectorIDs(pointIDs),
		})
		if err != nil {
			return fmt.Errorf("qdrant: failed to delete from %q: %w", name, err)
		}
	}

	return nil
}

// HealthCheck verifies the vector store is reachable.
func (p *Provider) HealthCheck(ctx context.Context) error {
	_, err := p.client.HealthCheck(ctx)
	if err != nil {
		return fmt.Errorf("qdrant: health check failed: %w", err)
	}
	return nil
}

// Close closes the underlying gRPC connection.
func (p *Provider) Close() error {
	return p.client.Close()
}

// buildFilter constructs a Qdrant filter from SearchOpts.
func buildFilter(opts vectorstore.SearchOpts) *pb.Filter {
	var must []*pb.Condition
	var should []*pb.Condition

	// Project ID filter (required for scoped search).
	if len(opts.ProjectIDs) == 1 {
		must = append(must, pb.NewMatch("project_id", opts.ProjectIDs[0]))
	} else if len(opts.ProjectIDs) > 1 {
		must = append(must, pb.NewMatchKeywords("project_id", opts.ProjectIDs...))
	}

	// Type filter (insight/recommendation).
	if len(opts.Types) == 1 {
		must = append(must, pb.NewMatch("type", opts.Types[0]))
	} else if len(opts.Types) > 1 {
		for _, t := range opts.Types {
			should = append(should, pb.NewMatch("type", t))
		}
	}

	// Embedding model filter.
	if opts.EmbeddingModel != "" {
		must = append(must, pb.NewMatch("embedding_model", opts.EmbeddingModel))
	}

	// Severity filter.
	if opts.Severity != "" {
		must = append(must, pb.NewMatch("severity", opts.Severity))
	}

	// Analysis area filter.
	if opts.AnalysisArea != "" {
		must = append(must, pb.NewMatch("analysis_area", opts.AnalysisArea))
	}

	if len(must) == 0 && len(should) == 0 {
		return nil
	}

	return &pb.Filter{
		Must:   must,
		Should: should,
	}
}

// float64sToFloat32s converts a float64 slice to float32 for Qdrant.
func float64sToFloat32s(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

// float32sToFloat64s converts a float32 slice to float64 for our interface.
func float32sToFloat64s(in []float32) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = float64(v)
	}
	return out
}

// pointIDToString extracts the string representation from a Qdrant PointId.
func pointIDToString(id *pb.PointId) string {
	if id == nil {
		return ""
	}
	switch v := id.PointIdOptions.(type) {
	case *pb.PointId_Uuid:
		return v.Uuid
	case *pb.PointId_Num:
		return fmt.Sprintf("%d", v.Num)
	default:
		return ""
	}
}

// payloadToMap converts Qdrant payload values to a generic map.
func payloadToMap(payload map[string]*pb.Value) map[string]interface{} {
	if payload == nil {
		return nil
	}
	result := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		result[k] = valueToInterface(v)
	}
	return result
}

// valueToInterface converts a Qdrant Value to a Go interface.
func valueToInterface(v *pb.Value) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.Kind.(type) {
	case *pb.Value_StringValue:
		return val.StringValue
	case *pb.Value_IntegerValue:
		return val.IntegerValue
	case *pb.Value_DoubleValue:
		return val.DoubleValue
	case *pb.Value_BoolValue:
		return val.BoolValue
	case *pb.Value_NullValue:
		return nil
	case *pb.Value_ListValue:
		if val.ListValue == nil {
			return nil
		}
		items := make([]interface{}, len(val.ListValue.Values))
		for i, item := range val.ListValue.Values {
			items[i] = valueToInterface(item)
		}
		return items
	case *pb.Value_StructValue:
		if val.StructValue == nil {
			return nil
		}
		m := make(map[string]interface{}, len(val.StructValue.Fields))
		for k, fv := range val.StructValue.Fields {
			m[k] = valueToInterface(fv)
		}
		return m
	default:
		return fmt.Sprintf("%v", v)
	}
}

// CollectionName returns the Qdrant collection name for a given vector dimension.
// Exported for use by callers that need to reference the collection directly.
func CollectionName(dimensions int) string {
	return collectionName(dimensions)
}

// ParseCollectionDimensions extracts the dimension count from a collection name.
// Returns 0 if the name doesn't match the expected pattern.
func ParseCollectionDimensions(name string) int {
	prefix := "decisionbox_"
	if !strings.HasPrefix(name, prefix) {
		return 0
	}
	var dims int
	_, err := fmt.Sscanf(name[len(prefix):], "%d", &dims)
	if err != nil {
		return 0
	}
	return dims
}
