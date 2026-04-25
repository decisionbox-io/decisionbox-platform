package schema_retrieve

import (
	"context"
	"errors"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
)

// --- fakeClient: a minimal Client stub to exercise all non-network paths ---

type fakeClient struct {
	// inputs recorded per call
	createRequests []*pb.CreateCollection
	upsertRequests []*pb.UpsertPoints
	queryRequests  []*pb.QueryPoints
	deletedNames   []string

	// state
	existing map[string]bool // collection name -> exists

	// stubbed behaviour
	existsErr error
	createErr error
	deleteErr error
	upsertErr error
	queryErr  error
	queryHits []*pb.ScoredPoint
	healthErr error
	closeErr  error
}

func newFakeClient() *fakeClient {
	return &fakeClient{existing: map[string]bool{}}
}

func (f *fakeClient) CollectionExists(ctx context.Context, name string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.existing[name], nil
}
func (f *fakeClient) CreateCollection(ctx context.Context, req *pb.CreateCollection) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createRequests = append(f.createRequests, req)
	f.existing[req.CollectionName] = true
	return nil
}
func (f *fakeClient) DeleteCollection(ctx context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedNames = append(f.deletedNames, name)
	delete(f.existing, name)
	return nil
}
func (f *fakeClient) Upsert(ctx context.Context, req *pb.UpsertPoints) (*pb.UpdateResult, error) {
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	f.upsertRequests = append(f.upsertRequests, req)
	return &pb.UpdateResult{}, nil
}
func (f *fakeClient) Query(ctx context.Context, req *pb.QueryPoints) ([]*pb.ScoredPoint, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	f.queryRequests = append(f.queryRequests, req)
	return f.queryHits, nil
}
func (f *fakeClient) HealthCheck(ctx context.Context) (*pb.HealthCheckReply, error) {
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return &pb.HealthCheckReply{}, nil
}
func (f *fakeClient) Close() error { return f.closeErr }

// --- tests ---

func TestCollectionName_Format(t *testing.T) {
	if got := CollectionName("abc"); got != "decisionbox_schema_abc" {
		t.Errorf("got %q", got)
	}
}

func TestNew_RequiresHost(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty host should error")
	}
}

func TestHealthCheck_HappyAndError(t *testing.T) {
	c := newFakeClient()
	r := NewWithClient(c)
	if err := r.HealthCheck(context.Background()); err != nil {
		t.Fatalf("healthy path: %v", err)
	}
	c.healthErr = errors.New("boom")
	if err := r.HealthCheck(context.Background()); err == nil {
		t.Fatal("error path: expected error")
	}
}

func TestEnsureCollection_CreatesWhenMissing(t *testing.T) {
	c := newFakeClient()
	r := NewWithClient(c)

	if err := r.EnsureCollection(context.Background(), "p1", 1536); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if len(c.createRequests) != 1 {
		t.Fatalf("create calls = %d, want 1", len(c.createRequests))
	}
	if c.createRequests[0].CollectionName != "decisionbox_schema_p1" {
		t.Errorf("collection name = %q", c.createRequests[0].CollectionName)
	}
}

func TestEnsureCollection_Idempotent(t *testing.T) {
	c := newFakeClient()
	c.existing["decisionbox_schema_p1"] = true
	r := NewWithClient(c)

	if err := r.EnsureCollection(context.Background(), "p1", 768); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if len(c.createRequests) != 0 {
		t.Error("should skip create when collection exists")
	}
}

func TestEnsureCollection_Validation(t *testing.T) {
	r := NewWithClient(newFakeClient())
	if err := r.EnsureCollection(context.Background(), "", 1536); err == nil {
		t.Error("empty projectID should error")
	}
	if err := r.EnsureCollection(context.Background(), "p", 0); err == nil {
		t.Error("zero dimensions should error")
	}
	if err := r.EnsureCollection(context.Background(), "p", -1); err == nil {
		t.Error("negative dimensions should error")
	}
}

func TestEnsureCollection_ExistsErrorPropagated(t *testing.T) {
	c := newFakeClient()
	c.existsErr = errors.New("qdrant down")
	if err := NewWithClient(c).EnsureCollection(context.Background(), "p", 128); err == nil {
		t.Error("expected error from exists check")
	}
}

func TestEnsureCollection_CreateErrorPropagated(t *testing.T) {
	c := newFakeClient()
	c.createErr = errors.New("create failed")
	if err := NewWithClient(c).EnsureCollection(context.Background(), "p", 128); err == nil {
		t.Error("expected error from create")
	}
}

func TestDropCollection_DeletesWhenExists(t *testing.T) {
	c := newFakeClient()
	c.existing["decisionbox_schema_p"] = true
	r := NewWithClient(c)
	if err := r.DropCollection(context.Background(), "p"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
	if len(c.deletedNames) != 1 || c.deletedNames[0] != "decisionbox_schema_p" {
		t.Errorf("deleted = %v", c.deletedNames)
	}
}

func TestDropCollection_MissingIsNoop(t *testing.T) {
	c := newFakeClient()
	// no existing entry
	if err := NewWithClient(c).DropCollection(context.Background(), "p"); err != nil {
		t.Fatalf("DropCollection noop: %v", err)
	}
	if len(c.deletedNames) != 0 {
		t.Error("should not call Delete when collection missing")
	}
}

func TestDropCollection_EmptyProjectID(t *testing.T) {
	if err := NewWithClient(newFakeClient()).DropCollection(context.Background(), ""); err == nil {
		t.Error("empty projectID should error")
	}
}

func TestUpsert_EmptyBatchIsNoop(t *testing.T) {
	c := newFakeClient()
	if err := NewWithClient(c).Upsert(context.Background(), "p", nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if len(c.upsertRequests) != 0 {
		t.Error("should skip Qdrant call for empty batch")
	}
}

func TestUpsert_RequiresVector(t *testing.T) {
	c := newFakeClient()
	err := NewWithClient(c).Upsert(context.Background(), "p", []UpsertItem{
		{Blurb: TableBlurb{Table: "t"}, Vector: nil},
	})
	if err == nil {
		t.Error("empty vector should error")
	}
}

func TestUpsert_RequiresTableName(t *testing.T) {
	c := newFakeClient()
	err := NewWithClient(c).Upsert(context.Background(), "p", []UpsertItem{
		{Blurb: TableBlurb{}, Vector: []float64{0.1, 0.2}},
	})
	if err == nil {
		t.Error("empty table should error")
	}
}

func TestUpsert_RejectsMixedDimensions(t *testing.T) {
	c := newFakeClient()
	err := NewWithClient(c).Upsert(context.Background(), "p", []UpsertItem{
		{Blurb: TableBlurb{Table: "a"}, Vector: []float64{0.1, 0.2}},
		{Blurb: TableBlurb{Table: "b"}, Vector: []float64{0.3, 0.4, 0.5}},
	})
	if err == nil {
		t.Error("mixed dims should error")
	}
}

func TestUpsert_SendsCorrectCollectionAndPoints(t *testing.T) {
	c := newFakeClient()
	err := NewWithClient(c).Upsert(context.Background(), "myproj", []UpsertItem{
		{
			Blurb: TableBlurb{
				Table: "sales.orders", Dataset: "sales", Blurb: "orders",
				Keywords: []string{"sales", "orders"}, RowCount: 100_000,
				ColumnCount: 10, BlurbModel: "bedrock/qwen", EmbeddingModel: "openai/t3l",
			},
			Vector: []float64{0.1, 0.2, 0.3},
		},
		{
			Blurb:  TableBlurb{Table: "sales.users", Dataset: "sales", Blurb: "users"},
			Vector: []float64{0.4, 0.5, 0.6},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(c.upsertRequests) != 1 {
		t.Fatalf("upsert calls = %d", len(c.upsertRequests))
	}
	req := c.upsertRequests[0]
	if req.CollectionName != "decisionbox_schema_myproj" {
		t.Errorf("collection = %q", req.CollectionName)
	}
	if len(req.Points) != 2 {
		t.Fatalf("points = %d", len(req.Points))
	}
	// Payload shape: bare table + dataset (see payloadFromBlurb — we
	// deliberately split the qualified name so operator tooling that
	// renders {dataset}.{table} doesn't show "sales.sales.orders").
	p0 := req.Points[0].Payload
	if s, ok := p0["table"].Kind.(*pb.Value_StringValue); !ok || s.StringValue != "orders" {
		t.Errorf("payload[table] wrong: %+v", p0["table"])
	}
	if s, ok := p0["dataset"].Kind.(*pb.Value_StringValue); !ok || s.StringValue != "sales" {
		t.Errorf("payload[dataset] wrong: %+v", p0["dataset"])
	}
	if _, ok := p0["keywords"].Kind.(*pb.Value_ListValue); !ok {
		t.Errorf("payload[keywords] should be list, got %+v", p0["keywords"])
	}
	if i, ok := p0["row_count"].Kind.(*pb.Value_IntegerValue); !ok || i.IntegerValue != 100_000 {
		t.Errorf("payload[row_count] wrong: %+v", p0["row_count"])
	}
}

func TestUpsert_PropagatesError(t *testing.T) {
	c := newFakeClient()
	c.upsertErr = errors.New("qdrant timeout")
	err := NewWithClient(c).Upsert(context.Background(), "p", []UpsertItem{
		{Blurb: TableBlurb{Table: "t"}, Vector: []float64{0.1}},
	})
	if err == nil {
		t.Error("expected error from upsert")
	}
}

func TestSearch_Validation(t *testing.T) {
	r := NewWithClient(newFakeClient())
	if _, err := r.Search(context.Background(), "", []float64{0.1}, SearchOpts{}); err == nil {
		t.Error("empty projectID should error")
	}
	if _, err := r.Search(context.Background(), "p", nil, SearchOpts{}); err == nil {
		t.Error("empty vector should error")
	}
}

func TestSearch_DefaultTopK(t *testing.T) {
	c := newFakeClient()
	// Return 100 fake points so we can see TopK=40 trim.
	for i := 0; i < 100; i++ {
		c.queryHits = append(c.queryHits, fakeScored(uniqueTable(i), float64(100-i)/100))
	}
	r := NewWithClient(c)
	hits, err := r.Search(context.Background(), "p", []float64{0.1, 0.2, 0.3}, SearchOpts{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != DefaultTopK {
		t.Errorf("len(hits) = %d, want %d", len(hits), DefaultTopK)
	}
}

func TestSearch_TopKOverride(t *testing.T) {
	c := newFakeClient()
	for i := 0; i < 30; i++ {
		c.queryHits = append(c.queryHits, fakeScored(uniqueTable(i), float64(30-i)/30))
	}
	hits, _ := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{TopK: 5})
	if len(hits) != 5 {
		t.Errorf("got %d, want 5", len(hits))
	}
}

func TestSearch_DedupsDuplicateTables(t *testing.T) {
	c := newFakeClient()
	c.queryHits = []*pb.ScoredPoint{
		fakeScored("same_table", 0.9),
		fakeScored("same_table", 0.8),
		fakeScored("other", 0.7),
	}
	hits, _ := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{})
	if len(hits) != 2 {
		t.Errorf("expected 2 deduped hits, got %d", len(hits))
	}
}

func TestSearch_KeywordBoostMovesSmallerCosineUp(t *testing.T) {
	c := newFakeClient()
	c.queryHits = []*pb.ScoredPoint{
		fakeScoredWithKW("irrelevant", 0.85, []string{"weather"}, "a boring table about weather"),
		fakeScoredWithKW("sales_orders", 0.82, []string{"sales", "orders"}, "table of sales orders"),
	}
	hits, _ := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{
		KeywordBoost: []string{"sales", "orders"},
	})
	// 0.82 + 2*0.02 = 0.86 > 0.85 → sales_orders wins.
	if hits[0].Blurb.Table != "sales_orders" {
		t.Errorf("expected sales_orders to top rank, got %q", hits[0].Blurb.Table)
	}
}

func TestSearch_KeywordBoostCappedAt10Pct(t *testing.T) {
	c := newFakeClient()
	c.queryHits = []*pb.ScoredPoint{
		fakeScoredWithKW("a", 0.5, []string{"k1", "k2", "k3", "k4", "k5", "k6"}, ""),
	}
	// Supplying 6 matching keywords: 6 * 0.02 = 0.12, capped at 0.10.
	hits, _ := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{
		KeywordBoost: []string{"k1", "k2", "k3", "k4", "k5", "k6"},
	})
	if got := hits[0].Score; got < 0.599 || got > 0.601 {
		t.Errorf("score = %f, expected 0.6 (cap at 0.10 boost)", got)
	}
}

func TestSearch_RowCountPriorNudgesLargeTables(t *testing.T) {
	c := newFakeClient()
	c.queryHits = []*pb.ScoredPoint{
		fakeScoredWithRows("small", 0.86, 10),
		fakeScoredWithRows("huge", 0.80, 10_000_000),
	}
	// Prior of 0.05 × log10(10M+1) ≈ 0.05 × 7 = 0.35 → huge wins.
	hits, _ := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{
		RowCountPrior: 0.05,
	})
	if hits[0].Blurb.Table != "huge" {
		t.Errorf("large-table prior not applied, got %q", hits[0].Blurb.Table)
	}
}

func TestSearch_DatasetFilterAttached(t *testing.T) {
	c := newFakeClient()
	_, _ = NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{DatasetFilter: "sales"})
	if len(c.queryRequests) != 1 {
		t.Fatalf("queryRequests = %d", len(c.queryRequests))
	}
	f := c.queryRequests[0].Filter
	if f == nil {
		t.Fatal("dataset filter dropped")
	}
	if len(f.Must) != 1 {
		t.Errorf("must conditions = %d", len(f.Must))
	}
}

func TestSearch_MinRowCountFilterAttached(t *testing.T) {
	c := newFakeClient()
	_, _ = NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{MinRowCount: 1000})
	f := c.queryRequests[0].Filter
	if f == nil || len(f.Must) != 1 {
		t.Fatalf("min row-count filter missing")
	}
}

func TestSearch_NoFilterWhenOptsEmpty(t *testing.T) {
	c := newFakeClient()
	_, _ = NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{})
	if c.queryRequests[0].Filter != nil {
		t.Error("no filter should be attached")
	}
}

func TestSearch_PropagatesQueryError(t *testing.T) {
	c := newFakeClient()
	c.queryErr = errors.New("qdrant timeout")
	if _, err := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{}); err == nil {
		t.Error("expected error")
	}
}

func TestSearch_SkipsPointsWithEmptyTablePayload(t *testing.T) {
	c := newFakeClient()
	c.queryHits = []*pb.ScoredPoint{
		fakeScored("", 0.95),       // corrupt point, no table
		fakeScored("good", 0.80),
	}
	hits, err := NewWithClient(c).Search(context.Background(), "p", []float64{1}, SearchOpts{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Blurb.Table != "good" {
		t.Errorf("expected 1 good hit, got %+v", hits)
	}
}

// --- payload (de)serialisation ---

func TestPayloadRoundTrip(t *testing.T) {
	in := TableBlurb{
		Table:          "dataset.orders",
		Dataset:        "dataset",
		Blurb:          "orders table",
		Keywords:       []string{"sales", "orders"},
		RowCount:       1_234_567,
		ColumnCount:    14,
		BlurbModel:     "bedrock/qwen.qwen3-32b-v1:0",
		EmbeddingModel: "openai/text-embedding-3-large",
	}
	pv, err := pb.TryValueMap(payloadFromBlurb(in, "p"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := blurbFromPayload(pv)
	if out.Table != in.Table || out.Dataset != in.Dataset || out.Blurb != in.Blurb {
		t.Errorf("core fields lost: %+v", out)
	}
	if out.RowCount != in.RowCount || out.ColumnCount != in.ColumnCount {
		t.Errorf("counts lost: got %d/%d", out.RowCount, out.ColumnCount)
	}
	if len(out.Keywords) != 2 || out.Keywords[0] != "sales" {
		t.Errorf("keywords lost: %v", out.Keywords)
	}
}

// Payload stores the bare table name so operator tooling that renders
// {dataset}.{table} doesn't see a doubled prefix. blurbFromPayload
// rehydrates the qualified form the rest of the agent expects.
func TestPayloadStoresBareTableNotQualified(t *testing.T) {
	in := TableBlurb{
		Table:   "dbo.FINP_PERSONEL",
		Dataset: "dbo",
	}
	raw := payloadFromBlurb(in, "p")
	if got := raw["table"]; got != "FINP_PERSONEL" {
		t.Errorf("payload.table = %q, want bare %q", got, "FINP_PERSONEL")
	}
	if got := raw["dataset"]; got != "dbo" {
		t.Errorf("payload.dataset = %q, want %q", got, "dbo")
	}
}

func TestPayloadRoundTrip_EmptyDataset(t *testing.T) {
	// Providers that don't use a multi-dataset namespace (e.g. BigQuery
	// single-project mode) write Dataset="" and Table=bare. The payload
	// should faithfully round-trip that shape without inventing a dot.
	in := TableBlurb{Table: "events", Dataset: ""}
	pv, err := pb.TryValueMap(payloadFromBlurb(in, "p"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := blurbFromPayload(pv)
	if out.Table != "events" {
		t.Errorf("empty-dataset Table = %q, want %q", out.Table, "events")
	}
	if out.Dataset != "" {
		t.Errorf("empty-dataset Dataset = %q, want empty", out.Dataset)
	}
}

func TestPayloadRoundTrip_UnqualifiedTableWithDataset(t *testing.T) {
	// Defensive: if a caller writes TableBlurb{Table: "orders",
	// Dataset: "dbo"} instead of the documented qualified form, the
	// payload still rehydrates sensibly — Table comes back as
	// "dbo.orders" so downstream Schemas[table] lookups hit.
	in := TableBlurb{Table: "orders", Dataset: "dbo"}
	pv, err := pb.TryValueMap(payloadFromBlurb(in, "p"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := blurbFromPayload(pv)
	if out.Table != "dbo.orders" {
		t.Errorf("rehydrated Table = %q, want %q", out.Table, "dbo.orders")
	}
}

func TestBlurbFromPayload_LegacyQualifiedStillWorks(t *testing.T) {
	// Forward-compat with points written by pre-fix agents: payload
	// already contains "dbo.orders" in the "table" field. The prefix
	// guard in blurbFromPayload must NOT double it to "dbo.dbo.orders".
	legacy := map[string]interface{}{
		"table":   "dbo.orders",
		"dataset": "dbo",
	}
	pv, err := pb.TryValueMap(legacy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := blurbFromPayload(pv)
	if out.Table != "dbo.orders" {
		t.Errorf("legacy-qualified Table = %q, want %q (no double prefix)", out.Table, "dbo.orders")
	}
}

// --- log10Plus1 ---

func TestLog10Plus1(t *testing.T) {
	cases := []struct {
		n    int64
		wantLow, wantHigh float64
	}{
		{0, 0, 0},
		{1, 0, 0},
		{10, 1, 1},
		{100, 2, 2},
		{1000, 3, 3},
		{1_000_000, 6, 6.01},
		{5_000_000, 6.4, 6.5},
	}
	for _, c := range cases {
		got := log10Plus1(c.n)
		if got < c.wantLow || got > c.wantHigh {
			t.Errorf("log10Plus1(%d) = %f, want [%f, %f]", c.n, got, c.wantLow, c.wantHigh)
		}
	}
}

// --- helpers ---

func uniqueTable(i int) string {
	return "table_" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	n := i
	if n < 0 {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if i < 0 {
		return "-" + s
	}
	return s
}

func fakeScored(table string, score float64) *pb.ScoredPoint {
	return fakeScoredWithKW(table, score, nil, "")
}

func fakeScoredWithKW(table string, score float64, kws []string, blurb string) *pb.ScoredPoint {
	kwIfs := make([]interface{}, len(kws))
	for i, k := range kws {
		kwIfs[i] = k
	}
	payload := map[string]interface{}{
		"table":    table,
		"keywords": kwIfs,
		"blurb":    blurb,
	}
	pv, _ := pb.TryValueMap(payload)
	return &pb.ScoredPoint{
		Score:   float32(score),
		Payload: pv,
	}
}

func fakeScoredWithRows(table string, score float64, rows int64) *pb.ScoredPoint {
	payload := map[string]interface{}{
		"table":     table,
		"row_count": rows,
	}
	pv, _ := pb.TryValueMap(payload)
	return &pb.ScoredPoint{Score: float32(score), Payload: pv}
}
