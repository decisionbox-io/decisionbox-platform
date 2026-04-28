// Package discovery — RunStepIndex is a per-run Qdrant collection of
// exploration steps. Built inline as exploration progresses (one
// upsert per completed step), queried during analysis to pick the
// steps most relevant to each area, and dropped at run completion.
//
// The collection is per-run rather than per-project because:
//   - the population is small and cheap to rebuild from scratch each run
//   - cross-run contamination would mix this run's freshly-explored
//     data with last week's stale steps
//   - dropping is a single-call cleanup rather than a per-step
//     "delete by run_id" sweep
//
// On agent crash we rely on the agentserver crash-recovery sweep
// (see RunStepIndexCollectionPrefix and SweepOrphanRunStepIndexes) to
// drop orphaned collections older than 24h.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/qdrant/go-client/qdrant"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// RunStepIndexCollectionPrefix is the stable prefix every per-run
// collection name shares. Exported so the crash-recovery sweep can
// list candidates without re-deriving the convention.
const RunStepIndexCollectionPrefix = "decisionbox_run_"

// RunStepIndexCollectionName returns the Qdrant collection name for a
// given run id. Centralised so a rename here forces the sweep to
// agree.
func RunStepIndexCollectionName(runID string) string {
	return RunStepIndexCollectionPrefix + runID
}

// RunStepIndex is the contract the orchestrator + exploration engine
// program against. The Qdrant-backed implementation below is the
// production wiring; tests inject a fake implementation directly.
type RunStepIndex interface {
	// Upsert embeds the step's text and writes (vector, payload) to
	// the per-run collection. Idempotent by step number. Steps with
	// empty Query are still indexed — search_tables / lookup_schema
	// steps occasionally surface useful analysis text.
	Upsert(ctx context.Context, step models.ExplorationStep) error

	// Search embeds the area query and returns the top-K hits, each
	// with the indexed step number, score, and lightweight payload
	// the caller uses for budget-trimming logging.
	Search(ctx context.Context, areaQuery string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error)

	// Drop removes the per-run collection. Idempotent — dropping a
	// missing collection returns nil so a defer'd cleanup is safe
	// even when Upsert never created the collection (e.g. all steps
	// errored before any were indexed).
	Drop(ctx context.Context) error
}

// RunStepIndexSearchOpts parameterises a single Search call.
type RunStepIndexSearchOpts struct {
	// TopK is the maximum number of hits returned. Caller must pass a
	// positive value.
	TopK int

	// MinScore is the cosine-similarity floor; hits below it are
	// dropped before returning. 0 disables the filter.
	MinScore float64
}

// RunStepIndexHit is one search result. Score is the post-filter
// cosine score. Payload mirrors what was stored on Upsert.
type RunStepIndexHit struct {
	Step     int
	Score    float64
	Purpose  string
	RowCount int
	HasError bool
}

// runStepEmbedder is the embed surface RunStepIndex needs. Local
// interface (rather than importing libs/go-common/embedding directly)
// keeps the unit-test fake one method instead of four.
type runStepEmbedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Dimensions() int
	ModelName() string
}

// runStepClient is the slim subset of the qdrant go-client this file
// consumes. Defined here so tests can plug a fake without bringing
// in a real Qdrant container.
type runStepClient interface {
	CollectionExists(ctx context.Context, name string) (bool, error)
	CreateCollection(ctx context.Context, req *pb.CreateCollection) error
	DeleteCollection(ctx context.Context, name string) error
	Upsert(ctx context.Context, req *pb.UpsertPoints) (*pb.UpdateResult, error)
	Query(ctx context.Context, req *pb.QueryPoints) ([]*pb.ScoredPoint, error)
	ListCollections(ctx context.Context) ([]string, error)
}

// runStepIndex is the production implementation backed by Qdrant +
// an embedding provider.
type runStepIndex struct {
	client   runStepClient
	embedder runStepEmbedder
	runID    string

	// ensureMu guards the lazy collection-create sequence so
	// concurrent Upsert calls don't race into double-create. The
	// sync.Once handles the success case; ensureErr captures the
	// failure case so retries can pick a clean code path on the next
	// Upsert.
	ensureMu   sync.Mutex
	ensureOnce *sync.Once
	ensureErr  error
}

// NewRunStepIndex builds a Qdrant-backed run step index. Both the
// client and the embedder must be non-nil; runID must be non-empty
// because it forms the collection name.
func NewRunStepIndex(client runStepClient, embedder runStepEmbedder, runID string) (RunStepIndex, error) {
	if client == nil {
		return nil, errors.New("run_step_index: qdrant client is required")
	}
	if embedder == nil {
		return nil, errors.New("run_step_index: embedder is required")
	}
	if runID == "" {
		return nil, errors.New("run_step_index: runID is required")
	}
	return &runStepIndex{
		client:     client,
		embedder:   embedder,
		runID:      runID,
		ensureOnce: &sync.Once{},
	}, nil
}

// embedTextForStep is the text we feed the embedder. Matches the
// shape the analysis-area query is built from so the cosine similarity
// is meaningful: short purpose / analysis sentence + the SQL itself.
//
// Thinking is intentionally NOT included — it tends to be exploratory
// chain-of-thought that hurts ranking more than it helps.
func embedTextForStep(step models.ExplorationStep) string {
	var b strings.Builder
	if step.QueryPurpose != "" {
		b.WriteString(step.QueryPurpose)
		b.WriteByte('\n')
	}
	if step.Query != "" {
		b.WriteString("[SQL]: ")
		b.WriteString(step.Query)
	}
	return strings.TrimSpace(b.String())
}

// Upsert embeds and writes a single step. See interface doc for
// behaviour details.
func (r *runStepIndex) Upsert(ctx context.Context, step models.ExplorationStep) error {
	text := embedTextForStep(step)
	if text == "" {
		// Nothing meaningful to embed (action with no SQL and no
		// purpose); skip silently — the picker can't use it anyway.
		applog.WithFields(applog.Fields{
			"run_id": r.runID,
			"step":   step.Step,
			"action": step.Action,
		}).Debug("run_step_index: skipping upsert — empty embed text")
		return nil
	}

	embedStart := time.Now()
	vectors, err := r.embedder.Embed(ctx, []string{text})
	if err != nil {
		applog.WithFields(applog.Fields{
			"run_id": r.runID,
			"step":   step.Step,
			"error":  err.Error(),
		}).Debug("run_step_index: embed call failed")
		return fmt.Errorf("run_step_index: embed step %d: %w", step.Step, err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return fmt.Errorf("run_step_index: embedder returned %d vectors of len 0 for step %d", len(vectors), step.Step)
	}
	vec := vectors[0]

	if err := r.ensureCollectionOnce(ctx, len(vec)); err != nil {
		return err
	}

	payload, err := pb.TryValueMap(map[string]any{
		"step":      int64(step.Step),
		"purpose":   step.QueryPurpose,
		"row_count": int64(step.RowCount),
		"has_error": step.Error != "",
	})
	if err != nil {
		return fmt.Errorf("run_step_index: payload encode for step %d: %w", step.Step, err)
	}

	wait := true
	upsertStart := time.Now()
	_, err = r.client.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: RunStepIndexCollectionName(r.runID),
		Wait:           &wait,
		Points: []*pb.PointStruct{{
			Id:      pb.NewID(stepPointID(r.runID, step.Step)),
			Vectors: pb.NewVectorsDense(float64sToFloat32sLocal(vec)),
			Payload: payload,
		}},
	})
	if err != nil {
		return fmt.Errorf("run_step_index: upsert step %d: %w", step.Step, err)
	}
	applog.WithFields(applog.Fields{
		"run_id":           r.runID,
		"step":             step.Step,
		"row_count":        step.RowCount,
		"has_error":        step.Error != "",
		"text_chars":       len(text),
		"embed_dims":       len(vec),
		"embed_ms":         upsertStart.Sub(embedStart).Milliseconds(),
		"upsert_ms":        time.Since(upsertStart).Milliseconds(),
	}).Debug("run_step_index: step indexed")
	return nil
}

// Search embeds the area query and returns matching hits.
func (r *runStepIndex) Search(ctx context.Context, areaQuery string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
	q := strings.TrimSpace(areaQuery)
	if q == "" {
		return nil, errors.New("run_step_index: areaQuery is empty")
	}
	if opts.TopK <= 0 {
		return nil, errors.New("run_step_index: TopK must be positive")
	}

	embedStart := time.Now()
	vectors, err := r.embedder.Embed(ctx, []string{q})
	if err != nil {
		return nil, fmt.Errorf("run_step_index: embed area query: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("run_step_index: embedder returned %d vectors of len 0 for area query", len(vectors))
	}
	vec := vectors[0]

	limit := uint64(opts.TopK) //nolint:gosec // TopK is a small positive int
	req := &pb.QueryPoints{
		CollectionName: RunStepIndexCollectionName(r.runID),
		Query:          pb.NewQueryDense(float64sToFloat32sLocal(vec)),
		Limit:          &limit,
		WithPayload:    pb.NewWithPayload(true),
	}
	if opts.MinScore > 0 {
		threshold := float32(opts.MinScore)
		req.ScoreThreshold = &threshold
	}

	queryStart := time.Now()
	scored, err := r.client.Query(ctx, req)
	if err != nil {
		// Collection-missing → empty hits. Happens on first call when
		// every prior Upsert failed before reaching the create path.
		if isMissingCollectionErr(err) {
			applog.WithFields(applog.Fields{
				"run_id": r.runID,
				"query":  truncateForLog(q, 80),
			}).Debug("run_step_index: search hit a missing collection — returning empty")
			return nil, nil
		}
		return nil, fmt.Errorf("run_step_index: search: %w", err)
	}

	hits := make([]RunStepIndexHit, 0, len(scored))
	for _, sp := range scored {
		hit := RunStepIndexHit{
			Step:     int(intValRSI(sp.Payload, "step")),
			Score:    float64(sp.Score),
			Purpose:  strValRSI(sp.Payload, "purpose"),
			RowCount: int(intValRSI(sp.Payload, "row_count")),
			HasError: boolValRSI(sp.Payload, "has_error"),
		}
		hits = append(hits, hit)
	}
	// Stable order: by score desc, step asc on ties — for determinism in
	// the picker's downstream merge.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Step < hits[j].Step
	})
	var topScore, bottomScore float64
	if len(hits) > 0 {
		topScore = hits[0].Score
		bottomScore = hits[len(hits)-1].Score
	}
	applog.WithFields(applog.Fields{
		"run_id":      r.runID,
		"query":       truncateForLog(q, 80),
		"top_k":       opts.TopK,
		"min_score":   opts.MinScore,
		"hits":        len(hits),
		"top_score":   topScore,
		"bottom_score": bottomScore,
		"embed_ms":    queryStart.Sub(embedStart).Milliseconds(),
		"query_ms":    time.Since(queryStart).Milliseconds(),
	}).Debug("run_step_index: search completed")
	return hits, nil
}

// Drop removes the run's Qdrant collection. Idempotent.
func (r *runStepIndex) Drop(ctx context.Context) error {
	name := RunStepIndexCollectionName(r.runID)
	exists, err := r.client.CollectionExists(ctx, name)
	if err != nil {
		if isMissingCollectionErr(err) {
			applog.WithField("run_id", r.runID).Debug("run_step_index: drop — collection already missing, no-op")
			return nil
		}
		return fmt.Errorf("run_step_index: check collection: %w", err)
	}
	if !exists {
		applog.WithField("run_id", r.runID).Debug("run_step_index: drop — collection does not exist, no-op")
		return nil
	}
	if err := r.client.DeleteCollection(ctx, name); err != nil {
		return fmt.Errorf("run_step_index: delete collection: %w", err)
	}
	applog.WithFields(applog.Fields{
		"run_id":     r.runID,
		"collection": name,
	}).Info("run_step_index: per-run collection dropped")
	// Reset the once so a re-use of the same index after Drop
	// (uncommon but allowed) re-creates the collection.
	r.ensureMu.Lock()
	r.ensureOnce = &sync.Once{}
	r.ensureErr = nil
	r.ensureMu.Unlock()
	return nil
}

// ensureCollectionOnce serialises the collection-create across
// concurrent upserts. Only the first call hits Qdrant; the rest
// observe the cached err / nil. If the create returns AlreadyExists
// we treat it as success — Qdrant returned that race because some
// other goroutine got there first.
func (r *runStepIndex) ensureCollectionOnce(ctx context.Context, dims int) error {
	r.ensureMu.Lock()
	once := r.ensureOnce
	r.ensureMu.Unlock()

	once.Do(func() {
		err := r.ensureCollection(ctx, dims)
		if err != nil && strings.Contains(err.Error(), "AlreadyExists") {
			// Race resolved server-side; treat as success.
			err = nil
		}
		r.ensureMu.Lock()
		r.ensureErr = err
		r.ensureMu.Unlock()
	})

	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()
	return r.ensureErr
}

func (r *runStepIndex) ensureCollection(ctx context.Context, dims int) error {
	if dims <= 0 {
		return fmt.Errorf("run_step_index: dimensions must be positive, got %d", dims)
	}
	name := RunStepIndexCollectionName(r.runID)
	exists, err := r.client.CollectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("run_step_index: check collection: %w", err)
	}
	if exists {
		return nil
	}
	err = r.client.CreateCollection(ctx, &pb.CreateCollection{
		CollectionName: name,
		VectorsConfig: pb.NewVectorsConfig(&pb.VectorParams{
			Size:     uint64(dims), //nolint:gosec // dimensions is a small positive int
			Distance: pb.Distance_Cosine,
		}),
	})
	if err != nil {
		return fmt.Errorf("run_step_index: create collection: %w", err)
	}
	applog.WithFields(applog.Fields{
		"run_id":     r.runID,
		"collection": name,
		"dimensions": dims,
	}).Info("run_step_index: per-run collection created")
	return nil
}

// SweepOrphanRunStepIndexes lists Qdrant collections matching the
// per-run prefix and drops any that don't appear in keepRunIDs. Run
// at agent boot to clean up after a crashed run.
//
// The actual age check would require collection-level metadata which
// Qdrant doesn't expose; we instead require the caller to pass the
// list of run ids that are still considered live (non-completed
// discovery_runs younger than 24h). Anything not in keepRunIDs is
// orphaned and gets dropped.
func SweepOrphanRunStepIndexes(ctx context.Context, client runStepClient, keepRunIDs map[string]struct{}) (int, error) {
	if client == nil {
		return 0, errors.New("run_step_index: client is required")
	}
	collections, err := client.ListCollections(ctx)
	if err != nil {
		return 0, fmt.Errorf("run_step_index: list collections: %w", err)
	}
	candidates := 0
	dropped := 0
	for _, name := range collections {
		if !strings.HasPrefix(name, RunStepIndexCollectionPrefix) {
			continue
		}
		candidates++
		runID := strings.TrimPrefix(name, RunStepIndexCollectionPrefix)
		if _, keep := keepRunIDs[runID]; keep {
			applog.WithFields(applog.Fields{
				"collection": name,
				"run_id":     runID,
			}).Debug("run_step_index: sweep — keeping live run collection")
			continue
		}
		if err := client.DeleteCollection(ctx, name); err != nil {
			return dropped, fmt.Errorf("run_step_index: delete orphan %q: %w", name, err)
		}
		applog.WithFields(applog.Fields{
			"collection": name,
			"run_id":     runID,
		}).Info("run_step_index: sweep — dropped orphan collection")
		dropped++
	}
	applog.WithFields(applog.Fields{
		"candidates": candidates,
		"dropped":    dropped,
		"kept":       len(keepRunIDs),
	}).Info("run_step_index: orphan sweep finished")
	return dropped, nil
}

// stepPointID returns a deterministic UUID-shaped point id for
// (runID, step). Qdrant rejects arbitrary string ids, so we derive
// 16 bytes from SHA-256(runID::step) and format as 8-4-4-4-12.
// Stable across re-upserts (idempotency).
func stepPointID(runID string, step int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s::%d", runID, step)))
	b := h[:16]
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func float64sToFloat32sLocal(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

func intValRSI(m map[string]*pb.Value, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	if iv, ok := v.Kind.(*pb.Value_IntegerValue); ok {
		return iv.IntegerValue
	}
	return 0
}

func strValRSI(m map[string]*pb.Value, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if sv, ok := v.Kind.(*pb.Value_StringValue); ok {
		return sv.StringValue
	}
	return ""
}

func boolValRSI(m map[string]*pb.Value, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	if bv, ok := v.Kind.(*pb.Value_BoolValue); ok {
		return bv.BoolValue
	}
	return false
}

// isMissingCollectionErr matches the error strings Qdrant returns
// when a collection doesn't exist. Different gRPC versions wrap the
// message slightly differently; we cover both.
func isMissingCollectionErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Not found") || strings.Contains(msg, "doesn't exist")
}

// truncateForLog clips a string to n characters with an ellipsis
// suffix. Used to keep debug log lines bounded — the area query and
// step purposes are normally short but a long pasted prompt should
// not blow up the log.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

