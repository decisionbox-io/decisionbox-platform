// Package schema_retrieve is the Qdrant-backed retrieval layer for
// per-project schema blurbs. It sits between:
//
//   - the schema indexer (which generates blurbs, embeds them, and
//     upserts via Upsert)
//   - discovery / /ask (which embed a per-step query and call Search)
//
// One Qdrant collection per project: `decisionbox_schema_{projectID}`.
// Dropping and recreating is the only "change the embedding model"
// pathway — Qdrant collections are bound to a fixed vector dimension so
// swapping models means a full rebuild (plan §3.10).
//
// The package intentionally does NOT wrap libs/go-common/vectorstore's
// generic Provider — the two use cases (insight-dup-detection vs.
// per-project schema retrieval) have different collection lifecycles,
// naming schemes, and payload shapes. Sharing code would have forced one
// abstraction to grow warts the other doesn't need.
package schema_retrieve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	pb "github.com/qdrant/go-client/qdrant"
)

// CollectionPrefix is the stable prefix for schema-index collections.
// Schema indexer + retrieval + /reindex all route through CollectionName
// so a rename in one place can't drift from the others.
const CollectionPrefix = "decisionbox_schema_"

// CollectionName returns the per-project Qdrant collection name for
// schema blurbs. Kept exported so migration code can list candidates.
func CollectionName(projectID string) string {
	return CollectionPrefix + projectID
}

// Config is the Qdrant connection config. Mirrored on the existing
// libs/go-common/vectorstore/qdrant.Config so deployment env vars
// translate 1:1.
type Config struct {
	Host   string
	Port   int
	APIKey string
	UseTLS bool
}

// Client is the subset of qdrant go-client methods schema_retrieve uses.
// Defined as an interface so unit tests can inject a fake without needing
// a real Qdrant container.
type Client interface {
	CollectionExists(ctx context.Context, name string) (bool, error)
	CreateCollection(ctx context.Context, req *pb.CreateCollection) error
	DeleteCollection(ctx context.Context, name string) error
	Upsert(ctx context.Context, req *pb.UpsertPoints) (*pb.UpdateResult, error)
	Query(ctx context.Context, req *pb.QueryPoints) ([]*pb.ScoredPoint, error)
	HealthCheck(ctx context.Context) (*pb.HealthCheckReply, error)
	Close() error
}

// Retriever is the top-level handle discovery + the indexer use.
// Safe to share across goroutines — the underlying qdrant client is
// concurrency-safe.
type Retriever struct {
	client Client
}

// New opens a gRPC connection to Qdrant.
func New(cfg Config) (*Retriever, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("schema_retrieve: host is required")
	}
	port := cfg.Port
	if port == 0 {
		port = 6334
	}
	client, err := pb.NewClient(&pb.Config{
		Host:   cfg.Host,
		Port:   port,
		APIKey: cfg.APIKey,
		UseTLS: cfg.UseTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("schema_retrieve: connect Qdrant: %w", err)
	}
	return &Retriever{client: client}, nil
}

// NewWithClient injects a custom Client — test-only entry point.
func NewWithClient(c Client) *Retriever {
	return &Retriever{client: c}
}

// HealthCheck pings Qdrant. Callers use this as the "is the vector store
// reachable" probe before kicking off a discovery run (plan §7 — Qdrant
// outage fails the run with a clear message, no degraded mode).
func (r *Retriever) HealthCheck(ctx context.Context) error {
	_, err := r.client.HealthCheck(ctx)
	if err != nil {
		return fmt.Errorf("schema_retrieve: health check: %w", err)
	}
	return nil
}

// EnsureCollection creates the per-project schema collection with the
// given vector dimension if it doesn't exist. Idempotent. Cosine distance
// — matches the spike's winning combos.
func (r *Retriever) EnsureCollection(ctx context.Context, projectID string, dimensions int) error {
	if projectID == "" {
		return fmt.Errorf("schema_retrieve: projectID is required")
	}
	if dimensions <= 0 {
		return fmt.Errorf("schema_retrieve: dimensions must be positive, got %d", dimensions)
	}
	name := CollectionName(projectID)
	exists, err := r.client.CollectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("schema_retrieve: check collection %q: %w", name, err)
	}
	if exists {
		return nil
	}
	err = r.client.CreateCollection(ctx, &pb.CreateCollection{
		CollectionName: name,
		VectorsConfig: pb.NewVectorsConfig(&pb.VectorParams{
			Size:     uint64(dimensions),
			Distance: pb.Distance_Cosine,
		}),
	})
	if err != nil {
		return fmt.Errorf("schema_retrieve: create collection %q: %w", name, err)
	}
	return nil
}

// DropCollection removes the entire per-project schema index. Invoked on
// user-triggered re-index and on embedding-model change. Idempotent —
// deleting a missing collection is not an error.
func (r *Retriever) DropCollection(ctx context.Context, projectID string) error {
	if projectID == "" {
		return fmt.Errorf("schema_retrieve: projectID is required")
	}
	name := CollectionName(projectID)
	exists, err := r.client.CollectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("schema_retrieve: check collection %q: %w", name, err)
	}
	if !exists {
		return nil
	}
	if err := r.client.DeleteCollection(ctx, name); err != nil {
		return fmt.Errorf("schema_retrieve: delete collection %q: %w", name, err)
	}
	return nil
}

// TableBlurb is one row of the schema index — a per-table natural-language
// description plus the metadata the renderer needs at retrieval time.
type TableBlurb struct {
	Table          string   // fully-qualified: dataset.table
	Dataset        string   // just the dataset for filtering
	Blurb          string   // 2-4 sentence description, from the blurb LLM
	Keywords       []string // 1-3 domain-pack keywords, used by sparse re-rank
	RowCount       int64
	ColumnCount    int
	BlurbModel     string // provider/model used — for audit + future staleness checks
	EmbeddingModel string // e.g. openai/text-embedding-3-large
}

// UpsertItem is a (blurb, vector) pair — caller embeds the blurb using
// whichever embedding provider they picked, then hands the pair to Upsert.
// Decoupling vectorisation from storage keeps the package provider-neutral.
type UpsertItem struct {
	Blurb  TableBlurb
	Vector []float64
}

// Upsert writes (vector, blurb) points to the per-project schema
// collection. Batched server-side via the go-client's Upsert.
//
// The caller is responsible for ensuring:
//   - EnsureCollection has been called with the matching dimensions
//   - every Vector in items has the same length (Qdrant rejects mixed dims)
func (r *Retriever) Upsert(ctx context.Context, projectID string, items []UpsertItem) error {
	if projectID == "" {
		return fmt.Errorf("schema_retrieve: projectID is required")
	}
	if len(items) == 0 {
		return nil
	}

	var dims int
	points := make([]*pb.PointStruct, 0, len(items))
	for _, it := range items {
		if it.Blurb.Table == "" {
			return fmt.Errorf("schema_retrieve: item with empty Table")
		}
		if len(it.Vector) == 0 {
			return fmt.Errorf("schema_retrieve: item %q has empty vector", it.Blurb.Table)
		}
		if dims == 0 {
			dims = len(it.Vector)
		} else if len(it.Vector) != dims {
			return fmt.Errorf("schema_retrieve: mixed vector dimensions (%d vs %d) in batch", dims, len(it.Vector))
		}
		payload, err := pb.TryValueMap(payloadFromBlurb(it.Blurb, projectID))
		if err != nil {
			return fmt.Errorf("schema_retrieve: payload encode for %q: %w", it.Blurb.Table, err)
		}
		points = append(points, &pb.PointStruct{
			// Stable ID so upserts are idempotent across index runs: using
			// the fully-qualified table name means a re-index overwrites,
			// not duplicates.
			Id:      pb.NewID(pointID(projectID, it.Blurb.Table)),
			Vectors: pb.NewVectorsDense(float64sToFloat32s(it.Vector)),
			Payload: payload,
		})
	}

	wait := true
	_, err := r.client.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: CollectionName(projectID),
		Wait:           &wait,
		Points:         points,
	})
	if err != nil {
		return fmt.Errorf("schema_retrieve: upsert %d points: %w", len(points), err)
	}
	return nil
}

// SearchOpts parameterise a retrieval query. The query vector is required;
// TopK defaults to DefaultTopK when 0. DatasetFilter (optional) narrows
// results to a single dataset — useful when a project indexed multiple
// datasets but a specific analysis area only pertains to one.
type SearchOpts struct {
	TopK           int
	DatasetFilter  string
	KeywordBoost   []string // sparse-keyword re-rank anchors; empty = no boost
	MinRowCount    int64    // hard filter — skip tiny lookup tables
	// RowCountPrior nudges ranking toward larger tables (log10(row_count) × prior).
	// 0.0 disables. 0.05–0.1 is a reasonable range. Stays client-side so we
	// don't mutate the vector similarity score on the server.
	RowCountPrior float64
}

// DefaultTopK — spike §10 established 40 is comfortable for tool-capable
// models, 60 for tool-disabled models. Caller wires through project config.
const DefaultTopK = 40

// Hit is one retrieval result. Score is post-rerank (higher = better).
type Hit struct {
	Blurb      TableBlurb
	BaseScore  float64 // raw cosine similarity from Qdrant
	Score      float64 // after client-side re-rank (keyword + row-count)
}

// Search retrieves the top-K tables for the given query vector. Returns
// results sorted by final Score descending (post-rerank).
//
// Re-rank policy (plan §7):
//   - sparse-keyword boost: +0.02 per keyword present in the blurb or
//     in the table's own keyword list, capped at a +0.10 total.
//   - row-count prior: +RowCountPrior × log10(1 + row_count) so large
//     fact tables rise above tiny config tables with similar blurbs.
//   - dedup: one entry per table (Qdrant dedups at upsert by ID, so this
//     is a belt-and-braces guard).
func (r *Retriever) Search(ctx context.Context, projectID string, vector []float64, opts SearchOpts) ([]Hit, error) {
	if projectID == "" {
		return nil, fmt.Errorf("schema_retrieve: projectID is required")
	}
	if len(vector) == 0 {
		return nil, fmt.Errorf("schema_retrieve: query vector is empty")
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	// Fetch a few extra so client-side rerank has something to reorder.
	oversample := uint64(topK * 2)
	if oversample > 200 {
		oversample = 200
	}

	query := &pb.QueryPoints{
		CollectionName: CollectionName(projectID),
		Query:          pb.NewQueryDense(float64sToFloat32s(vector)),
		Limit:          &oversample,
		WithPayload:    pb.NewWithPayload(true),
		Filter:         buildSearchFilter(opts),
	}

	scored, err := r.client.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("schema_retrieve: qdrant query: %w", err)
	}

	hits := make([]Hit, 0, len(scored))
	seen := make(map[string]struct{}, len(scored))
	for _, sp := range scored {
		blurb := blurbFromPayload(sp.Payload)
		if blurb.Table == "" {
			// Corrupt or legacy point; skip.
			continue
		}
		if _, dup := seen[blurb.Table]; dup {
			continue
		}
		seen[blurb.Table] = struct{}{}
		base := float64(sp.Score)
		hits = append(hits, Hit{
			Blurb:     blurb,
			BaseScore: base,
			Score:     rerankScore(base, blurb, opts),
		})
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// Close releases the gRPC connection.
func (r *Retriever) Close() error { return r.client.Close() }

// --- internal helpers ---

// pointID returns a deterministic UUID-shaped point ID for (project, table).
// Qdrant's gRPC API rejects arbitrary strings — point IDs must either be
// unsigned integers or RFC 4122 UUIDs. We derive a stable UUID-v5-ish ID
// from SHA-256(projectID::table) so repeated upserts overwrite the same
// point (idempotent re-index) without carrying the uuid dep.
//
// The first 16 bytes of the SHA-256 digest are formatted as
// xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx. Version/variant bits are left
// alone — Qdrant accepts any 16-byte value in UUID notation.
func pointID(projectID, table string) string {
	sum := sha256.Sum256([]byte(projectID + "::" + table))
	b := sum[:16]
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func payloadFromBlurb(b TableBlurb, projectID string) map[string]interface{} {
	// qdrant's TryValueMap does not accept []string directly — convert
	// to []interface{} so the string-value conversion kicks in per item.
	kws := make([]interface{}, len(b.Keywords))
	for i, k := range b.Keywords {
		kws[i] = k
	}
	return map[string]interface{}{
		"project_id":      projectID,
		"table":           b.Table,
		"dataset":         b.Dataset,
		"blurb":           b.Blurb,
		"keywords":        kws,
		"row_count":       b.RowCount,
		"column_count":    int64(b.ColumnCount),
		"blurb_model":     b.BlurbModel,
		"embedding_model": b.EmbeddingModel,
	}
}

func blurbFromPayload(payload map[string]*pb.Value) TableBlurb {
	b := TableBlurb{
		Table:          strVal(payload, "table"),
		Dataset:        strVal(payload, "dataset"),
		Blurb:          strVal(payload, "blurb"),
		Keywords:       strListVal(payload, "keywords"),
		RowCount:       intVal(payload, "row_count"),
		ColumnCount:    int(intVal(payload, "column_count")),
		BlurbModel:     strVal(payload, "blurb_model"),
		EmbeddingModel: strVal(payload, "embedding_model"),
	}
	return b
}

func strVal(m map[string]*pb.Value, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.Kind.(*pb.Value_StringValue); ok {
		return s.StringValue
	}
	return ""
}

func intVal(m map[string]*pb.Value, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	if i, ok := v.Kind.(*pb.Value_IntegerValue); ok {
		return i.IntegerValue
	}
	return 0
}

func strListVal(m map[string]*pb.Value, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	lv, ok := v.Kind.(*pb.Value_ListValue)
	if !ok || lv.ListValue == nil {
		return nil
	}
	out := make([]string, 0, len(lv.ListValue.Values))
	for _, item := range lv.ListValue.Values {
		if s, ok := item.Kind.(*pb.Value_StringValue); ok {
			out = append(out, s.StringValue)
		}
	}
	return out
}

func float64sToFloat32s(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

// buildSearchFilter returns a Qdrant filter for dataset + min-row-count.
// Returns nil when no filter applies (Qdrant treats nil as "match all").
func buildSearchFilter(opts SearchOpts) *pb.Filter {
	var must []*pb.Condition
	if opts.DatasetFilter != "" {
		must = append(must, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key:   "dataset",
					Match: &pb.Match{MatchValue: &pb.Match_Keyword{Keyword: opts.DatasetFilter}},
				},
			},
		})
	}
	if opts.MinRowCount > 0 {
		min := float64(opts.MinRowCount)
		must = append(must, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key:   "row_count",
					Range: &pb.Range{Gte: &min},
				},
			},
		})
	}
	if len(must) == 0 {
		return nil
	}
	return &pb.Filter{Must: must}
}

// rerankScore applies the client-side rerank policy described in
// Search's docstring.
func rerankScore(base float64, b TableBlurb, opts SearchOpts) float64 {
	score := base

	// Keyword boost: each query keyword present in the blurb text or the
	// table's keyword list adds 0.02 to a max of 0.10. Case-insensitive
	// substring match against the blurb, exact-match against the keyword
	// list.
	if len(opts.KeywordBoost) > 0 {
		boost := 0.0
		lcBlurb := strings.ToLower(b.Blurb)
		tableKws := make(map[string]struct{}, len(b.Keywords))
		for _, k := range b.Keywords {
			tableKws[strings.ToLower(k)] = struct{}{}
		}
		for _, kw := range opts.KeywordBoost {
			if kw == "" {
				continue
			}
			lk := strings.ToLower(kw)
			if strings.Contains(lcBlurb, lk) {
				boost += 0.02
				continue
			}
			if _, ok := tableKws[lk]; ok {
				boost += 0.02
			}
		}
		if boost > 0.10 {
			boost = 0.10
		}
		score += boost
	}

	// Row-count prior: log10 because 1M-row tables shouldn't dominate
	// 10k-row tables 100× — just 2× in the prior.
	if opts.RowCountPrior > 0 && b.RowCount > 0 {
		score += opts.RowCountPrior * log10Plus1(b.RowCount)
	}

	return score
}

// log10Plus1 avoids math.Log10(0) → -Inf. Not enough table-count variance
// for log2 to matter; keeping it simple.
func log10Plus1(n int64) float64 {
	if n < 1 {
		return 0
	}
	// Hand-rolled — avoiding the math dep for a 6-line helper. Covers the
	// row-count range we see (1 → 1e12).
	x := float64(n)
	result := 0.0
	for x >= 10 {
		x /= 10
		result++
	}
	// Linear interpolation within the decade.
	result += (x - 1) / 9
	return result
}
