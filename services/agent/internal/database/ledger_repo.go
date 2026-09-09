package database

import (
	"context"
	"fmt"
	"sort"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Discovery Ledger repositories (enterprise#261). The agent's end-of-run
// reflection phase is the writer; the read path (next run) and the enterprise
// API/RAG are the readers. Shared document types live in libs/go-common/models
// so the two modules cannot drift on BSON tags.

// LedgerRepository persists the per-project ledger doc (coverage + convergence).
type LedgerRepository struct {
	collection *mongo.Collection
}

// NewLedgerRepository creates the repository.
func NewLedgerRepository(client *DB) *LedgerRepository {
	return &LedgerRepository{collection: client.Collection(CollectionDiscoveryLedger)}
}

// EnsureIndexes creates the project_id lookup index (one doc per project).
func (r *LedgerRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "project_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_ledger indexes: %w", err)
	}
	return nil
}

// Get returns the project's ledger. When none exists yet it returns a fresh
// zero-value ledger (ProjectID set) rather than an error, so callers don't have
// to special-case the first run.
func (r *LedgerRepository) Get(ctx context.Context, projectID string) (*commonmodels.DiscoveryLedger, error) {
	var l commonmodels.DiscoveryLedger
	err := r.collection.FindOne(ctx, bson.M{"project_id": projectID}).Decode(&l)
	if err == mongo.ErrNoDocuments {
		return &commonmodels.DiscoveryLedger{ProjectID: projectID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get discovery ledger: %w", err)
	}
	return &l, nil
}

// Save upserts the project's ledger by project_id.
func (r *LedgerRepository) Save(ctx context.Context, l *commonmodels.DiscoveryLedger) error {
	now := time.Now()
	l.UpdatedAt = now
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	_, err := r.collection.UpdateOne(ctx,
		bson.M{"project_id": l.ProjectID},
		bson.M{"$set": l},
		options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("save discovery ledger: %w", err)
	}
	return nil
}

// LedgerFindingRepository persists durable findings carried across runs.
type LedgerFindingRepository struct {
	collection *mongo.Collection
}

// NewLedgerFindingRepository creates the repository.
func NewLedgerFindingRepository(client *DB) *LedgerFindingRepository {
	return &LedgerFindingRepository{collection: client.Collection(CollectionDiscoveryLedgerFindings)}
}

// EnsureIndexes creates the read/dedup indexes: (project_id, status, last_seen
// desc) for the ranked read path, and (project_id, normalized_key) for exact
// dedup lookups.
func (r *LedgerFindingRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "status", Value: 1}, {Key: "last_seen", Value: -1}}},
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "normalized_key", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_ledger_findings indexes: %w", err)
	}
	return nil
}

// List returns every finding for the project (unranked — the read path ranks in
// Go via LedgerFinding.Rank; the reflection phase re-judges statuses).
func (r *LedgerFindingRepository) List(ctx context.Context, projectID string) ([]commonmodels.LedgerFinding, error) {
	cur, err := r.collection.Find(ctx, bson.M{"project_id": projectID})
	if err != nil {
		return nil, fmt.Errorf("list ledger findings: %w", err)
	}
	defer cur.Close(ctx)
	var out []commonmodels.LedgerFinding
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode ledger findings: %w", err)
	}
	return out, nil
}

// Upsert writes/replaces a finding by _id. The caller stamps _id + timestamps.
func (r *LedgerFindingRepository) Upsert(ctx context.Context, f *commonmodels.LedgerFinding) error {
	if f.ID == "" {
		return fmt.Errorf("upsert ledger finding: empty id")
	}
	_, err := r.collection.ReplaceOne(ctx, bson.M{"_id": f.ID}, f, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert ledger finding: %w", err)
	}
	return nil
}

// Prune caps the project's finding count at max, deleting the least valuable
// first: resolved/refuted findings (oldest last_seen first), then — only if
// still over — the oldest active ones. A non-positive max disables pruning.
func (r *LedgerFindingRepository) Prune(ctx context.Context, projectID string, max int) error {
	if max <= 0 {
		return nil
	}
	// Fetch a lightweight projection (id/status/last_seen) so we can order by
	// "least valuable" in Go without pulling full documents.
	type lite struct {
		ID       string    `bson:"_id"`
		Status   string    `bson:"status"`
		LastSeen time.Time `bson:"last_seen"`
	}
	cur, err := r.collection.Find(ctx, bson.M{"project_id": projectID},
		options.Find().SetProjection(bson.M{"_id": 1, "status": 1, "last_seen": 1}))
	if err != nil {
		return fmt.Errorf("prune ledger findings (scan): %w", err)
	}
	var rows []lite
	if err := cur.All(ctx, &rows); err != nil {
		return fmt.Errorf("prune ledger findings (decode): %w", err)
	}
	if len(rows) <= max {
		return nil
	}
	// Least valuable first: done statuses before active, then oldest first.
	doneRank := func(s string) int {
		switch s {
		case commonmodels.LedgerFindingStatusRefuted:
			return 0
		case commonmodels.LedgerFindingStatusResolved:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if di, dj := doneRank(rows[i].Status), doneRank(rows[j].Status); di != dj {
			return di < dj
		}
		return rows[i].LastSeen.Before(rows[j].LastSeen)
	})
	overflow := len(rows) - max
	victims := make([]string, 0, overflow)
	for i := 0; i < overflow; i++ {
		victims = append(victims, rows[i].ID)
	}
	if _, err := r.collection.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": victims}}); err != nil {
		return fmt.Errorf("prune ledger findings (delete): %w", err)
	}
	return nil
}

// LedgerProposalRepository persists proposed domain-pack (analysis-area) deltas.
// The agent's reflection phase is the writer (status "proposed"); the enterprise
// evolution workflow reads/updates/applies them.
type LedgerProposalRepository struct {
	collection *mongo.Collection
}

// NewLedgerProposalRepository creates the repository.
func NewLedgerProposalRepository(client *DB) *LedgerProposalRepository {
	return &LedgerProposalRepository{collection: client.Collection(CollectionDiscoveryPackProposals)}
}

// EnsureIndexes creates the (project_id, status, created_at desc) list index.
func (r *LedgerProposalRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "status", Value: 1}, {Key: "created_at", Value: -1}},
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_pack_proposals indexes: %w", err)
	}
	return nil
}

// Insert writes a batch of freshly-proposed deltas. A nil/empty batch is a no-op.
func (r *LedgerProposalRepository) Insert(ctx context.Context, proposals []commonmodels.PackProposal) error {
	if len(proposals) == 0 {
		return nil
	}
	docs := make([]interface{}, len(proposals))
	for i := range proposals {
		docs[i] = proposals[i]
	}
	if _, err := r.collection.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("insert pack proposals: %w", err)
	}
	return nil
}

// ListForProject returns the project's proposals, newest first, optionally
// filtered to the given statuses. Used by the reflection phase to skip
// re-proposing a delta that already has an open proposal.
func (r *LedgerProposalRepository) ListForProject(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.PackProposal, error) {
	filter := bson.M{"project_id": projectID}
	if len(statuses) > 0 {
		filter["status"] = bson.M{"$in": statuses}
	}
	cur, err := r.collection.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("list pack proposals: %w", err)
	}
	defer cur.Close(ctx)
	var out []commonmodels.PackProposal
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode pack proposals: %w", err)
	}
	return out, nil
}

// LedgerTaskRepository persists the open-thread / next-task queue.
type LedgerTaskRepository struct {
	collection *mongo.Collection
}

// NewLedgerTaskRepository creates the repository.
func NewLedgerTaskRepository(client *DB) *LedgerTaskRepository {
	return &LedgerTaskRepository{collection: client.Collection(CollectionDiscoveryLedgerTasks)}
}

// EnsureIndexes creates the (project_id, status, created_at desc) list index.
func (r *LedgerTaskRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "status", Value: 1}, {Key: "created_at", Value: -1}},
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_ledger_tasks indexes: %w", err)
	}
	return nil
}

// List returns the project's tasks, newest first, optionally filtered to the
// given statuses. No statuses means "all".
func (r *LedgerTaskRepository) List(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.LedgerTask, error) {
	filter := bson.M{"project_id": projectID}
	if len(statuses) > 0 {
		filter["status"] = bson.M{"$in": statuses}
	}
	cur, err := r.collection.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("list ledger tasks: %w", err)
	}
	defer cur.Close(ctx)
	var out []commonmodels.LedgerTask
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode ledger tasks: %w", err)
	}
	return out, nil
}

// Insert writes a batch of new tasks. A nil/empty batch is a no-op.
func (r *LedgerTaskRepository) Insert(ctx context.Context, tasks []commonmodels.LedgerTask) error {
	if len(tasks) == 0 {
		return nil
	}
	docs := make([]interface{}, len(tasks))
	for i := range tasks {
		docs[i] = tasks[i]
	}
	if _, err := r.collection.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("insert ledger tasks: %w", err)
	}
	return nil
}

// UpdateStatus transitions a task (e.g. open → done when a run acted on it).
func (r *LedgerTaskRepository) UpdateStatus(ctx context.Context, projectID, id, status string) error {
	_, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": id, "project_id": projectID},
		bson.M{"$set": bson.M{"status": status, "updated_at": time.Now()}})
	if err != nil {
		return fmt.Errorf("update ledger task status: %w", err)
	}
	return nil
}
