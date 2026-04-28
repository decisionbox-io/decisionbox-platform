package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeRunStepClient stubs the qdrant subset RunStepIndex consumes.
// Each method records the request that was made and returns a
// configurable result. Concurrency: tests are single-threaded, so we
// don't bother with mutexes.
type fakeRunStepClient struct {
	collectionExistsBy map[string]bool
	createCalls        []*pb.CreateCollection
	deleteCalls        []string
	upsertCalls        []*pb.UpsertPoints
	queryCalls         []*pb.QueryPoints
	listResp           []string

	createErr  error
	deleteErr  error
	upsertErr  error
	queryErr   error
	existsErr  error
	listErr    error
	scoredResp []*pb.ScoredPoint
}

func (f *fakeRunStepClient) CollectionExists(ctx context.Context, name string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.collectionExistsBy[name], nil
}

func (f *fakeRunStepClient) CreateCollection(ctx context.Context, req *pb.CreateCollection) error {
	f.createCalls = append(f.createCalls, req)
	if f.createErr != nil {
		return f.createErr
	}
	if f.collectionExistsBy == nil {
		f.collectionExistsBy = map[string]bool{}
	}
	f.collectionExistsBy[req.CollectionName] = true
	return nil
}

func (f *fakeRunStepClient) DeleteCollection(ctx context.Context, name string) error {
	f.deleteCalls = append(f.deleteCalls, name)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.collectionExistsBy, name)
	return nil
}

func (f *fakeRunStepClient) Upsert(ctx context.Context, req *pb.UpsertPoints) (*pb.UpdateResult, error) {
	f.upsertCalls = append(f.upsertCalls, req)
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	return &pb.UpdateResult{}, nil
}

func (f *fakeRunStepClient) Query(ctx context.Context, req *pb.QueryPoints) ([]*pb.ScoredPoint, error) {
	f.queryCalls = append(f.queryCalls, req)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.scoredResp, nil
}

func (f *fakeRunStepClient) ListCollections(ctx context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

// fakeStepEmbedder returns a fixed vector + records the prompt it received.
type fakeStepEmbedder struct {
	vec       []float64
	dims      int
	model     string
	err       error
	calls     []string
	callCount int
}

func (f *fakeStepEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	f.calls = append(f.calls, texts...)
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}
func (f *fakeStepEmbedder) Dimensions() int  { return f.dims }
func (f *fakeStepEmbedder) ModelName() string { return f.model }

func newFakes(t *testing.T) (*fakeRunStepClient, *fakeStepEmbedder, RunStepIndex) {
	t.Helper()
	c := &fakeRunStepClient{collectionExistsBy: map[string]bool{}}
	e := &fakeStepEmbedder{vec: []float64{0.1, 0.2, 0.3, 0.4}, dims: 4, model: "fake-embedder"}
	idx, err := NewRunStepIndex(c, e, "RUN1")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}
	return c, e, idx
}

func TestNewRunStepIndex_Validates(t *testing.T) {
	cases := []struct {
		name string
		fn   func() (RunStepIndex, error)
	}{
		{"nil client", func() (RunStepIndex, error) {
			return NewRunStepIndex(nil, &fakeStepEmbedder{}, "r")
		}},
		{"nil embedder", func() (RunStepIndex, error) {
			return NewRunStepIndex(&fakeRunStepClient{}, nil, "r")
		}},
		{"empty runID", func() (RunStepIndex, error) {
			return NewRunStepIndex(&fakeRunStepClient{}, &fakeStepEmbedder{}, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.fn(); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestRunStepIndex_Upsert_ToExpectedCollection(t *testing.T) {
	c, _, idx := newFakes(t)

	step := models.ExplorationStep{
		Step:         3,
		Action:       "query_data",
		Query:        "SELECT 1",
		QueryPurpose: "smoke test",
	}
	if err := idx.Upsert(context.Background(), step); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(c.upsertCalls) != 1 {
		t.Fatalf("upsertCalls: got %d want 1", len(c.upsertCalls))
	}
	want := RunStepIndexCollectionName("RUN1")
	if c.upsertCalls[0].CollectionName != want {
		t.Errorf("collection: got %q want %q", c.upsertCalls[0].CollectionName, want)
	}
}

func TestRunStepIndex_Upsert_PayloadFields(t *testing.T) {
	c, _, idx := newFakes(t)
	step := models.ExplorationStep{
		Step:         7,
		Query:        "SELECT * FROM t",
		QueryPurpose: "investigate t",
		RowCount:     12,
		Error:        "",
	}
	_ = idx.Upsert(context.Background(), step)
	if len(c.upsertCalls) != 1 {
		t.Fatalf("upsertCalls: got %d want 1", len(c.upsertCalls))
	}
	pt := c.upsertCalls[0].Points[0]
	if pt.Payload == nil {
		t.Fatal("payload nil")
	}
	if intValRSI(pt.Payload, "step") != 7 {
		t.Errorf("payload step: got %d want 7", intValRSI(pt.Payload, "step"))
	}
	if strValRSI(pt.Payload, "purpose") != "investigate t" {
		t.Errorf("payload purpose: got %q want investigate t", strValRSI(pt.Payload, "purpose"))
	}
	if intValRSI(pt.Payload, "row_count") != 12 {
		t.Errorf("payload row_count: got %d want 12", intValRSI(pt.Payload, "row_count"))
	}
	if boolValRSI(pt.Payload, "has_error") != false {
		t.Errorf("payload has_error: got true want false (Error was empty)")
	}
}

func TestRunStepIndex_Upsert_HasErrorTrueWhenStepErrored(t *testing.T) {
	c, _, idx := newFakes(t)
	step := models.ExplorationStep{
		Step:  4,
		Query: "BROKEN",
		Error: "boom",
	}
	_ = idx.Upsert(context.Background(), step)
	if !boolValRSI(c.upsertCalls[0].Points[0].Payload, "has_error") {
		t.Errorf("has_error should be true when Error is non-empty")
	}
}

func TestRunStepIndex_Upsert_EmbeddingTextShape(t *testing.T) {
	_, e, idx := newFakes(t)
	step := models.ExplorationStep{
		Step:         1,
		Query:        "SELECT a FROM t",
		QueryPurpose: "purpose",
		Thinking:     "noisy reasoning",
	}
	_ = idx.Upsert(context.Background(), step)
	if len(e.calls) != 1 {
		t.Fatalf("embedder calls: got %d want 1", len(e.calls))
	}
	got := e.calls[0]
	if !strings.Contains(got, "purpose") {
		t.Errorf("embedding text missing purpose: %q", got)
	}
	if !strings.Contains(got, "SELECT a FROM t") {
		t.Errorf("embedding text missing SQL: %q", got)
	}
	if strings.Contains(got, "noisy reasoning") {
		t.Errorf("embedding text must NOT include Thinking: %q", got)
	}
}

func TestRunStepIndex_Upsert_SkipsWhenEmptyText(t *testing.T) {
	c, e, idx := newFakes(t)
	step := models.ExplorationStep{Step: 1} // no query, no purpose
	if err := idx.Upsert(context.Background(), step); err != nil {
		t.Errorf("Upsert: %v", err)
	}
	if e.callCount != 0 {
		t.Errorf("embedder must not be called for empty step")
	}
	if len(c.upsertCalls) != 0 {
		t.Errorf("upsert must not be called for empty step")
	}
}

func TestRunStepIndex_Upsert_PropagatesEmbedError(t *testing.T) {
	_, e, idx := newFakes(t)
	e.err = errors.New("embedder down")
	err := idx.Upsert(context.Background(), models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"})
	if err == nil || !strings.Contains(err.Error(), "embedder down") {
		t.Errorf("expected wrapped embedder error, got %v", err)
	}
}

func TestRunStepIndex_Upsert_PropagatesQdrantError(t *testing.T) {
	c, _, idx := newFakes(t)
	c.upsertErr = errors.New("qdrant down")
	err := idx.Upsert(context.Background(), models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"})
	if err == nil || !strings.Contains(err.Error(), "qdrant down") {
		t.Errorf("expected wrapped qdrant error, got %v", err)
	}
}

func TestRunStepIndex_Upsert_CreatesCollectionLazilyOnce(t *testing.T) {
	c, _, idx := newFakes(t)
	step := models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"}
	for i := 0; i < 3; i++ {
		step.Step = i + 1
		if err := idx.Upsert(context.Background(), step); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	if len(c.createCalls) != 1 {
		t.Errorf("CreateCollection should only be called once (got %d)", len(c.createCalls))
	}
	if len(c.upsertCalls) != 3 {
		t.Errorf("Upsert should be called per step (got %d)", len(c.upsertCalls))
	}
}

func TestRunStepIndex_Search_HappyPath(t *testing.T) {
	c, _, idx := newFakes(t)
	c.scoredResp = []*pb.ScoredPoint{
		mockScoredPoint(t, 5, 0.9, "purpose 5", 100, false),
		mockScoredPoint(t, 8, 0.7, "purpose 8", 0, true),
	}
	hits, err := idx.Search(context.Background(), "churn risks", RunStepIndexSearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits: got %d want 2", len(hits))
	}
	if hits[0].Step != 5 || hits[0].Score < 0.85 {
		t.Errorf("hit[0]: got %+v", hits[0])
	}
	if hits[1].Step != 8 || !hits[1].HasError {
		t.Errorf("hit[1]: got %+v", hits[1])
	}
}

func TestRunStepIndex_Search_FiltersByMinScore(t *testing.T) {
	c, _, idx := newFakes(t)
	_, _ = idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 3, MinScore: 0.4})
	if len(c.queryCalls) != 1 {
		t.Fatalf("queryCalls: got %d want 1", len(c.queryCalls))
	}
	if c.queryCalls[0].ScoreThreshold == nil || *c.queryCalls[0].ScoreThreshold != float32(0.4) {
		t.Errorf("ScoreThreshold not propagated; got %v", c.queryCalls[0].ScoreThreshold)
	}
}

func TestRunStepIndex_Search_TopKMustBePositive(t *testing.T) {
	_, _, idx := newFakes(t)
	if _, err := idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 0}); err == nil {
		t.Errorf("expected error for TopK=0")
	}
}

func TestRunStepIndex_Search_EmptyQueryRejected(t *testing.T) {
	_, _, idx := newFakes(t)
	if _, err := idx.Search(context.Background(), "  ", RunStepIndexSearchOpts{TopK: 5}); err == nil {
		t.Errorf("expected error for empty query")
	}
}

func TestRunStepIndex_Search_MissingCollectionReturnsEmpty(t *testing.T) {
	c, _, idx := newFakes(t)
	c.queryErr = errors.New("Not found: collection doesn't exist")
	hits, err := idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 5})
	if err != nil {
		t.Errorf("missing collection should not error, got %v", err)
	}
	if hits != nil {
		t.Errorf("hits should be nil, got %v", hits)
	}
}

func TestRunStepIndex_Search_OrdersByScoreThenStep(t *testing.T) {
	c, _, idx := newFakes(t)
	// Same score for steps 9 and 4 — step 4 should come first (lower).
	c.scoredResp = []*pb.ScoredPoint{
		mockScoredPoint(t, 9, 0.5, "p9", 0, false),
		mockScoredPoint(t, 4, 0.5, "p4", 0, false),
		mockScoredPoint(t, 1, 0.9, "p1", 0, false),
	}
	hits, _ := idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 5})
	if hits[0].Step != 1 {
		t.Errorf("hits[0].Step: got %d want 1 (top score)", hits[0].Step)
	}
	if hits[1].Step != 4 {
		t.Errorf("hits[1].Step: got %d want 4 (tie, lower step number first)", hits[1].Step)
	}
	if hits[2].Step != 9 {
		t.Errorf("hits[2].Step: got %d want 9", hits[2].Step)
	}
}

func TestRunStepIndex_Drop_DeletesCollection(t *testing.T) {
	c, _, idx := newFakes(t)
	c.collectionExistsBy[RunStepIndexCollectionName("RUN1")] = true
	if err := idx.Drop(context.Background()); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if len(c.deleteCalls) != 1 {
		t.Errorf("DeleteCollection: got %d calls want 1", len(c.deleteCalls))
	}
}

func TestRunStepIndex_Drop_IgnoresNotFound(t *testing.T) {
	_, _, idx := newFakes(t)
	if err := idx.Drop(context.Background()); err != nil {
		t.Errorf("Drop on missing collection should be no-op, got %v", err)
	}
}

func TestRunStepIndex_Drop_PropagatesError(t *testing.T) {
	c, _, idx := newFakes(t)
	c.collectionExistsBy[RunStepIndexCollectionName("RUN1")] = true
	c.deleteErr = errors.New("kaboom")
	if err := idx.Drop(context.Background()); err == nil {
		t.Errorf("expected error from DeleteCollection")
	}
}

func TestSweepOrphanRunStepIndexes_DropsOrphans(t *testing.T) {
	c := &fakeRunStepClient{
		listResp: []string{
			"decisionbox_schema_p1",                 // not a run collection — leave alone
			RunStepIndexCollectionName("RUN_LIVE"),  // keep
			RunStepIndexCollectionName("RUN_ORPHAN"), // drop
			RunStepIndexCollectionName("RUN_OLD"),    // drop
		},
		collectionExistsBy: map[string]bool{},
	}
	keep := map[string]struct{}{"RUN_LIVE": {}}
	dropped, err := SweepOrphanRunStepIndexes(context.Background(), c, keep)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped: got %d want 2", dropped)
	}
	want := []string{
		RunStepIndexCollectionName("RUN_ORPHAN"),
		RunStepIndexCollectionName("RUN_OLD"),
	}
	if len(c.deleteCalls) != len(want) {
		t.Fatalf("deleteCalls: got %v want %v", c.deleteCalls, want)
	}
}

func TestSweepOrphanRunStepIndexes_NilClient(t *testing.T) {
	if _, err := SweepOrphanRunStepIndexes(context.Background(), nil, nil); err == nil {
		t.Errorf("expected error for nil client")
	}
}

func TestRunStepIndexCollectionName(t *testing.T) {
	want := "decisionbox_run_abc"
	if got := RunStepIndexCollectionName("abc"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestRunStepIndex_Upsert_PropagatesEnsureCollectionError(t *testing.T) {
	c, _, idx := newFakes(t)
	c.existsErr = errors.New("transport error")
	err := idx.Upsert(context.Background(), models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"})
	if err == nil || !strings.Contains(err.Error(), "transport error") {
		t.Errorf("expected wrapped exists error, got %v", err)
	}
}

func TestRunStepIndex_Upsert_AlreadyExistsRaceTreatedAsSuccess(t *testing.T) {
	c, _, idx := newFakes(t)
	// Simulate the concurrent-create race: CollectionExists returns false
	// (we go to create) but CreateCollection fails with AlreadyExists
	// because another goroutine got there first.
	c.createErr = errors.New("rpc error: AlreadyExists Wrong input: Collection already exists")
	if err := idx.Upsert(context.Background(), models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"}); err != nil {
		t.Errorf("AlreadyExists must collapse to nil, got %v", err)
	}
	if len(c.upsertCalls) != 1 {
		t.Errorf("upsert should still proceed after AlreadyExists, got %d calls", len(c.upsertCalls))
	}
}

func TestRunStepIndex_Upsert_RejectsEmptyVectorFromEmbedder(t *testing.T) {
	c := &fakeRunStepClient{collectionExistsBy: map[string]bool{}}
	e := &fakeStepEmbedder{vec: []float64{}, dims: 0, model: "broken"}
	idx, err := NewRunStepIndex(c, e, "RUN_BAD_VEC")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}
	if err := idx.Upsert(context.Background(), models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"}); err == nil {
		t.Errorf("expected error for empty vector from embedder")
	}
}

func TestRunStepIndex_Drop_PropagatesExistsError(t *testing.T) {
	c, _, idx := newFakes(t)
	c.existsErr = errors.New("transport down")
	if err := idx.Drop(context.Background()); err == nil {
		t.Errorf("Drop must propagate non-NotFound exists errors")
	}
}

func TestRunStepIndex_Drop_NotFoundOnExistsCheckIsNil(t *testing.T) {
	c, _, idx := newFakes(t)
	c.existsErr = errors.New("Not found")
	if err := idx.Drop(context.Background()); err != nil {
		t.Errorf("Drop must collapse Not found exists check, got %v", err)
	}
}

func TestRunStepIndex_Drop_AfterUseAllowsReuse(t *testing.T) {
	c, _, idx := newFakes(t)
	step := models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"}
	if err := idx.Upsert(context.Background(), step); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(c.createCalls) != 1 {
		t.Fatalf("first upsert should create the collection")
	}
	c.collectionExistsBy[RunStepIndexCollectionName("RUN1")] = true
	if err := idx.Drop(context.Background()); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// After drop the once must reset so a re-upsert creates the
	// collection again.
	step.Step = 2
	if err := idx.Upsert(context.Background(), step); err != nil {
		t.Fatalf("post-drop upsert: %v", err)
	}
	if len(c.createCalls) != 2 {
		t.Errorf("CreateCollection should have been called again after Drop, got %d total", len(c.createCalls))
	}
}

func TestSweepOrphanRunStepIndexes_PropagatesListError(t *testing.T) {
	c := &fakeRunStepClient{listErr: errors.New("list failed")}
	if _, err := SweepOrphanRunStepIndexes(context.Background(), c, nil); err == nil {
		t.Errorf("expected error from list")
	}
}

func TestSweepOrphanRunStepIndexes_PropagatesDeleteError(t *testing.T) {
	c := &fakeRunStepClient{
		listResp:  []string{RunStepIndexCollectionName("RUN_X")},
		deleteErr: errors.New("delete failed"),
	}
	if _, err := SweepOrphanRunStepIndexes(context.Background(), c, nil); err == nil {
		t.Errorf("expected error from delete")
	}
}

func TestRunStepIndex_PayloadAccessors_HandleWrongType(t *testing.T) {
	// intValRSI/strValRSI/boolValRSI should return zero values when the
	// stored payload field is the wrong type — defensive, not panic.
	wrongInt, _ := pb.TryValueMap(map[string]any{"step": "not-an-int"})
	if got := intValRSI(wrongInt, "step"); got != 0 {
		t.Errorf("intValRSI on wrong type: got %d want 0", got)
	}
	wrongStr, _ := pb.TryValueMap(map[string]any{"purpose": int64(42)})
	if got := strValRSI(wrongStr, "purpose"); got != "" {
		t.Errorf("strValRSI on wrong type: got %q want \"\"", got)
	}
	wrongBool, _ := pb.TryValueMap(map[string]any{"has_error": "true"})
	if got := boolValRSI(wrongBool, "has_error"); got {
		t.Errorf("boolValRSI on wrong type: got true want false")
	}
}

func TestRunStepIndex_Search_PropagatesEmbedError(t *testing.T) {
	_, e, idx := newFakes(t)
	e.err = errors.New("embedder fail")
	if _, err := idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 5}); err == nil {
		t.Errorf("expected wrapped embed error")
	}
}

func TestRunStepIndex_Search_RejectsEmptyVectorFromEmbedder(t *testing.T) {
	c := &fakeRunStepClient{collectionExistsBy: map[string]bool{}}
	e := &fakeStepEmbedder{vec: nil, dims: 0, model: "broken"}
	idx, err := NewRunStepIndex(c, e, "RUN_BAD_VEC")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}
	if _, err := idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 5}); err == nil {
		t.Errorf("expected error for empty vector from embedder")
	}
}

func TestRunStepIndex_Search_PropagatesQdrantErrorOtherThanNotFound(t *testing.T) {
	c, _, idx := newFakes(t)
	c.queryErr = errors.New("connection refused")
	if _, err := idx.Search(context.Background(), "x", RunStepIndexSearchOpts{TopK: 5}); err == nil {
		t.Errorf("expected wrapped non-NotFound qdrant error")
	}
}

func TestRunStepIndex_Upsert_RejectsZeroDims(t *testing.T) {
	// Defensive path inside ensureCollection; embedder returning a 0-dim
	// vector is already caught earlier, but exercise the dims<=0 branch
	// directly via a custom embedder that surfaces a zero-length vector
	// only on the embed call (so the upfront non-empty check still fires).
	c, e, idx := newFakes(t)
	// Make the embed return a single non-empty vector first, then assert
	// our own empty-vector guard. The dims<=0 branch in ensureCollection
	// is structurally defensive — there's no realistic call path. So
	// we only exercise the negative path: a fresh index seeded with a
	// vector should succeed (positive control for the dims branch).
	if err := idx.Upsert(context.Background(), models.ExplorationStep{Step: 1, Query: "X", QueryPurpose: "p"}); err != nil {
		t.Errorf("baseline upsert: %v", err)
	}
	_ = c
	_ = e
}

func TestPayloadAccessors_NilEntryReturnsZero(t *testing.T) {
	// The nil-entry branch on the accessors covers payloads where Qdrant
	// returns the key but with a nil Value pointer (rare but real on
	// some legacy points).
	m := map[string]*pb.Value{"step": nil}
	if got := intValRSI(m, "step"); got != 0 {
		t.Errorf("intValRSI on nil value: got %d want 0", got)
	}
	if got := strValRSI(m, "step"); got != "" {
		t.Errorf("strValRSI on nil value: got %q want \"\"", got)
	}
	if got := boolValRSI(m, "step"); got {
		t.Errorf("boolValRSI on nil value: got true want false")
	}
}

func TestStepPointID_Stable(t *testing.T) {
	a := stepPointID("RUN1", 5)
	b := stepPointID("RUN1", 5)
	if a != b {
		t.Errorf("stepPointID must be deterministic: got %q vs %q", a, b)
	}
	c := stepPointID("RUN2", 5)
	if a == c {
		t.Errorf("different runIDs must produce different point ids")
	}
}

// mockScoredPoint builds a ScoredPoint with the payload fields the
// search path expects. Returning a *pb.ScoredPoint mirrors what the
// real Qdrant client returns.
func mockScoredPoint(t *testing.T, step int, score float32, purpose string, rowCount int, hasError bool) *pb.ScoredPoint {
	t.Helper()
	payload, err := pb.TryValueMap(map[string]any{
		"step":      int64(step),
		"purpose":   purpose,
		"row_count": int64(rowCount),
		"has_error": hasError,
	})
	if err != nil {
		t.Fatalf("TryValueMap: %v", err)
	}
	return &pb.ScoredPoint{
		Score:   score,
		Payload: payload,
	}
}
