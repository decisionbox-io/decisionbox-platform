package qdrant

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/qdrant/go-client/qdrant"
)

// mockClient implements qdrantClient for testing.
type mockClient struct {
	mu          sync.Mutex
	collections map[string]bool
	points      map[string]map[string]*pb.PointStruct // collection -> pointID -> point
	healthy     bool
	err         error // if set, all operations return this error
}

func newMockClient() *mockClient {
	return &mockClient{
		collections: make(map[string]bool),
		points:      make(map[string]map[string]*pb.PointStruct),
		healthy:     true,
	}
}

func (m *mockClient) CollectionExists(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	return m.collections[name], nil
}

func (m *mockClient) CreateCollection(_ context.Context, req *pb.CreateCollection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.collections[req.CollectionName] {
		return fmt.Errorf("collection %q already exists", req.CollectionName)
	}
	m.collections[req.CollectionName] = true
	m.points[req.CollectionName] = make(map[string]*pb.PointStruct)
	return nil
}

func (m *mockClient) ListCollections(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	names := make([]string, 0, len(m.collections))
	for name := range m.collections {
		names = append(names, name)
	}
	return names, nil
}

func (m *mockClient) Upsert(_ context.Context, req *pb.UpsertPoints) (*pb.UpdateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	coll, ok := m.points[req.CollectionName]
	if !ok {
		return nil, fmt.Errorf("collection %q does not exist", req.CollectionName)
	}
	for _, pt := range req.Points {
		id := pointIDToString(pt.Id)
		coll[id] = pt
	}
	return &pb.UpdateResult{}, nil
}

func (m *mockClient) Query(_ context.Context, req *pb.QueryPoints) ([]*pb.ScoredPoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	coll, ok := m.points[req.CollectionName]
	if !ok {
		return nil, fmt.Errorf("collection %q does not exist", req.CollectionName)
	}

	var results []*pb.ScoredPoint
	limit := uint64(10)
	if req.Limit != nil {
		limit = *req.Limit
	}

	for _, pt := range coll {
		if !matchesFilter(pt, req.Filter) {
			continue
		}
		score := float32(0.85) // mock score
		if req.ScoreThreshold != nil && score < *req.ScoreThreshold {
			continue
		}
		results = append(results, &pb.ScoredPoint{
			Id:      pt.Id,
			Score:   score,
			Payload: pt.Payload,
		})
		if uint64(len(results)) >= limit {
			break
		}
	}

	return results, nil
}

func (m *mockClient) Delete(_ context.Context, req *pb.DeletePoints) (*pb.UpdateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	coll, ok := m.points[req.CollectionName]
	if !ok {
		return nil, fmt.Errorf("collection %q does not exist", req.CollectionName)
	}
	if selector := req.Points; selector != nil {
		if ids := selector.GetPoints(); ids != nil {
			for _, id := range ids.Ids {
				delete(coll, pointIDToString(id))
			}
		}
	}
	return &pb.UpdateResult{}, nil
}

func (m *mockClient) HealthCheck(_ context.Context) (*pb.HealthCheckReply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if !m.healthy {
		return nil, fmt.Errorf("qdrant is unhealthy")
	}
	return &pb.HealthCheckReply{Version: "1.13.0"}, nil
}

func (m *mockClient) Close() error {
	return nil
}

// matchesFilter is a simplified filter matching for the mock.
// It only supports Must conditions with string keyword matching.
func matchesFilter(pt *pb.PointStruct, filter *pb.Filter) bool {
	if filter == nil {
		return true
	}

	// Check Must conditions (AND)
	for _, cond := range filter.Must {
		if !matchCondition(pt, cond) {
			return false
		}
	}

	// Check MustNot conditions (NOT)
	for _, cond := range filter.MustNot {
		if matchCondition(pt, cond) {
			return false
		}
	}

	// Check Should conditions (OR) — at least one must match
	if len(filter.Should) > 0 {
		anyMatch := false
		for _, cond := range filter.Should {
			if matchCondition(pt, cond) {
				anyMatch = true
				break
			}
		}
		if !anyMatch {
			return false
		}
	}

	return true
}

// matchCondition checks if a single condition matches a point.
func matchCondition(pt *pb.PointStruct, cond *pb.Condition) bool {
	if cond == nil {
		return true
	}

	fc := cond.GetField()
	if fc == nil {
		return true
	}

	fieldName := fc.Key
	payloadVal, exists := pt.Payload[fieldName]
	if !exists {
		return false
	}

	match := fc.GetMatch()
	if match == nil {
		return true
	}

	// String keyword match
	if kw := match.GetKeyword(); kw != "" {
		return payloadVal.GetStringValue() == kw
	}

	// Keywords (any-of) match
	if keywords := match.GetKeywords(); keywords != nil {
		sv := payloadVal.GetStringValue()
		for _, k := range keywords.Strings {
			if sv == k {
				return true
			}
		}
		return false
	}

	return true
}

// Helper: check if a string contains a substring (for error messages)
func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}
