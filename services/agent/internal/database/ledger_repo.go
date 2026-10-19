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
// desc) for the ranked read path, (project_id, normalized_key) for exact dedup
// lookups, and (project_id, archived) for the active-set reads every prompt
// path goes through.
func (r *LedgerFindingRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "status", Value: 1}, {Key: "last_seen", Value: -1}}},
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "normalized_key", Value: 1}}},
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "archived", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_ledger_findings indexes: %w", err)
	}
	return nil
}

// notArchived is the active-set filter. An archived finding is retained history
// — it keeps the graph's edges intact — but it is out of the active set, so it
// reaches no prompt and no investigation view.
var notArchived = bson.M{"$ne": true}

// List returns the project's ACTIVE findings (unranked — the read path ranks in
// Go via LedgerFinding.Rank; the reflection phase re-judges statuses).
//
// Archived findings are excluded, which is what keeps prompt size and content
// identical to the behaviour before the retention cap became a soft archive:
// this is the list both prompt paths read (the exploration phase's ledger
// context and the reflection phase's prior-findings page). The dedup pass needs
// the archived ones too and calls ListIncludingArchived instead — two named
// methods rather than one boolean parameter, because a boolean at a call site
// is how an archived finding ends up back in a prompt six months from now.
func (r *LedgerFindingRepository) List(ctx context.Context, projectID string) ([]commonmodels.LedgerFinding, error) {
	return r.find(ctx, bson.M{"project_id": projectID, "archived": notArchived})
}

// ListIncludingArchived returns every finding for the project, archived ones
// included. Used by the consolidation pass: a finding the retention cap
// archived and this run has seen again must merge into the existing document
// (and be un-archived), not be written a second time under the same
// normalized_key.
func (r *LedgerFindingRepository) ListIncludingArchived(ctx context.Context, projectID string) ([]commonmodels.LedgerFinding, error) {
	return r.find(ctx, bson.M{"project_id": projectID})
}

func (r *LedgerFindingRepository) find(ctx context.Context, filter bson.M) ([]commonmodels.LedgerFinding, error) {
	cur, err := r.collection.Find(ctx, filter)
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

// Prune caps the project's ACTIVE finding count at max, archiving the least
// valuable first: resolved/refuted findings (oldest last_seen first), then —
// only if still over — the oldest active ones. A non-positive max disables
// pruning.
//
// It archives rather than deletes. Ledger findings are the nodes the
// investigation graph's edges point at — a finding's source insights, the task
// it answered, the runs that saw it — and a hard delete at the retention cap
// would silently break every one of them. An archived finding is excluded from
// the active set (so prompts and the investigation view are unchanged) and
// still present as history. A later run that sees it again un-archives it.
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
	// Only the ACTIVE set is scanned and counted against max. Counting the
	// archived rows too would make the collection grow past max permanently and
	// archive a fresh slice of live findings on every single run after that.
	cur, err := r.collection.Find(ctx, bson.M{"project_id": projectID, "archived": notArchived},
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
	if _, err := r.collection.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": victims}},
		bson.M{"$set": bson.M{"archived": true, "archived_at": time.Now()}}); err != nil {
		return fmt.Errorf("prune ledger findings (archive): %w", err)
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

// TaskClose is everything a run knows at the moment it closes a thread: which
// run closed it, the discovery that run produced, why, and the findings that
// answered it. Closing used to write the status and the timestamp alone, which
// left "we followed this up — what did we find?" unanswerable.
type TaskClose struct {
	Status               string
	RunID                string
	DiscoveryID          string
	At                   time.Time
	Reason               string
	ResolvedByFindingIDs []string
}

// Close transitions a task to a terminal status (done / dropped) and stamps the
// close metadata in the same write.
//
// ResolvedByFindingIDs is written only when it has entries, so a `dropped` — a
// proven dead end, which by definition has nothing to point at — does not store
// an empty array.
func (r *LedgerTaskRepository) Close(ctx context.Context, projectID, id string, u TaskClose) error {
	at := u.At
	if at.IsZero() {
		at = time.Now()
	}
	set := bson.M{
		"status":     u.Status,
		"updated_at": at,
		"closed_at":  at,
	}
	if u.RunID != "" {
		set["closed_run_id"] = u.RunID
	}
	if u.DiscoveryID != "" {
		set["closed_discovery_id"] = u.DiscoveryID
	}
	if u.Reason != "" {
		set["close_reason"] = u.Reason
	}
	if len(u.ResolvedByFindingIDs) > 0 {
		set["resolved_by_finding_ids"] = u.ResolvedByFindingIDs
	}
	if _, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": id, "project_id": projectID},
		bson.M{"$set": set}); err != nil {
		return fmt.Errorf("close ledger task: %w", err)
	}
	return nil
}

// MarkPursued records that a run had these threads in front of it, whether or
// not it closed any of them — the honest answer to "which threads did this run
// pursue?". One $addToSet over the whole batch rather than a write per task,
// and idempotent, so a re-run of the reflection phase cannot duplicate a run id.
func (r *LedgerTaskRepository) MarkPursued(ctx context.Context, projectID string, ids []string, runID string) error {
	if len(ids) == 0 || runID == "" {
		return nil
	}
	if _, err := r.collection.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": ids}, "project_id": projectID},
		bson.M{"$addToSet": bson.M{"pursued_in_run_ids": runID}}); err != nil {
		return fmt.Errorf("mark ledger tasks pursued: %w", err)
	}
	return nil
}

// LedgerLearningRepository persists the durable learnings a run produced about
// the warehouse / domain, attributed to that run. The reflection phase is the
// writer; the enterprise investigation graph is the reader.
type LedgerLearningRepository struct {
	collection *mongo.Collection
}

// NewLedgerLearningRepository creates the repository.
func NewLedgerLearningRepository(client *DB) *LedgerLearningRepository {
	return &LedgerLearningRepository{collection: client.Collection(CollectionDiscoveryLedgerLearnings)}
}

// EnsureIndexes creates the dedup lookup (project_id, normalized_key) and the
// (project_id, created_at desc) list index.
func (r *LedgerLearningRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "normalized_key", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "created_at", Value: -1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_ledger_learnings indexes: %w", err)
	}
	return nil
}

// Upsert records a learning, deduped on (project_id, normalized_key): a
// learning a later run repeats bumps seen_count and re-attributes itself to
// that run rather than writing a second row. The caller stamps the id, which is
// used only when the row is new.
func (r *LedgerLearningRepository) Upsert(ctx context.Context, l *commonmodels.LedgerLearning) error {
	if l.NormalizedKey == "" {
		return fmt.Errorf("upsert ledger learning: empty normalized key")
	}
	now := time.Now()
	_, err := r.collection.UpdateOne(ctx,
		bson.M{"project_id": l.ProjectID, "normalized_key": l.NormalizedKey},
		bson.M{
			"$set": bson.M{
				"category":     l.Category,
				"note":         l.Note,
				"relevance":    l.Relevance,
				"run_id":       l.RunID,
				"discovery_id": l.DiscoveryID,
				"updated_at":   now,
			},
			"$inc": bson.M{"seen_count": 1},
			"$setOnInsert": bson.M{
				"_id":        l.ID,
				"created_at": now,
			},
		},
		options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert ledger learning: %w", err)
	}
	return nil
}

// List returns the project's learnings, newest first. limit <= 0 means all.
func (r *LedgerLearningRepository) List(ctx context.Context, projectID string, limit int) ([]commonmodels.LedgerLearning, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := r.collection.Find(ctx, bson.M{"project_id": projectID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list ledger learnings: %w", err)
	}
	defer cur.Close(ctx)
	var out []commonmodels.LedgerLearning
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode ledger learnings: %w", err)
	}
	return out, nil
}
