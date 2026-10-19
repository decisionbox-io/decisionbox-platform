package backfill

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Discovery Ledger investigation-graph backfill.
//
// The reflection phase records a finding's provenance from now on, but a
// project with a year of history has none of it. Re-discovering is not an
// option — a run costs real warehouse time and LLM spend, and it would produce
// *this* month's findings, not last year's.
//
// So this recovers what is honestly derivable from what is already stored, in
// descending order of confidence, and reports what it could not recover rather
// than filling the gap with a guess. A wrong provenance edge is worse than a
// missing one: a missing one shows as "not linked" and an operator knows where
// they stand, while a wrong one sends someone to the SQL for a different
// finding and looks right.
//
// Nothing here uses a vector search. Semantic similarity is the right tool for
// deduping candidate findings at write time, when both texts are in hand and a
// human can see the result; it is the wrong tool for asserting after the fact
// that a particular insight is where a particular finding came from.
//
// Every write is conditional on the target field being absent, so a re-run is
// a no-op and an interrupted run can simply be repeated.

const (
	// defaultCloseWindow is how close a convergence point has to be to a task's
	// updated_at for that run to be called the one that closed it. Tasks are
	// closed by the reflection phase, which runs minutes after the run it
	// belongs to, so a few hours is generous; what matters is that it is tight
	// enough for "exactly one run falls inside it" to mean something on a
	// project that runs daily.
	defaultCloseWindow = 6 * time.Hour
	// defaultLedgerBatchSize bounds each bulk write.
	defaultLedgerBatchSize = 200
)

// ledgerGraphCounts is what the run reports. The unlinked counts are the point:
// they are how an operator knows the gap exists rather than assuming the
// backfill filled it.
type ledgerGraphCounts struct {
	FindingsScanned       int
	FindingsLinked        int
	FindingsAlreadyLinked int
	FindingsUnlinkable    int
	SightingsWritten      int
	StatusTrailsWritten   int
	TasksScanned          int
	TasksClosedStamped    int
	TasksCloseRunResolved int
	// The two reasons a closing run is left unset are different diagnoses and
	// are counted apart. "Several candidates" says to try a tighter
	// --close-window; "no candidate" says there are no run documents to match
	// against at all, and no window will help.
	TasksCloseRunAmbiguous int
	TasksCloseRunNoRun     int
}

// RunBackfillLedgerGraph runs the backfill-ledger-graph subcommand.
func RunBackfillLedgerGraph(args []string) {
	fs := flag.NewFlagSet("backfill-ledger-graph", flag.ExitOnError)
	projectID := fs.String("project-id", "", "Project ID to backfill (default: every project with ledger findings)")
	dryRun := fs.Bool("dry-run", false, "Report what would change without writing anything")
	batchSize := fs.Int("batch-size", defaultLedgerBatchSize, "Maximum writes per bulk operation")
	closeWindow := fs.Duration("close-window", defaultCloseWindow,
		"How close a run must be to a task's updated_at to be called its closing run; a window containing two runs leaves it unset")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	// The work is a separate function so the MongoDB disconnect can be
	// deferred: os.Exit skips deferred calls, so the exit code has to be
	// decided here, after that defer has already run.
	if err := runLedgerGraphBackfill(context.Background(),
		*projectID, *batchSize, *closeWindow, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runLedgerGraphBackfill(ctx context.Context, projectID string,
	batchSize int, closeWindow time.Duration, dryRun bool) error {
	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		return fmt.Errorf("MONGODB_URI environment variable is required")
	}
	mongoDBName := os.Getenv("MONGODB_DB")
	if mongoDBName == "" {
		mongoDBName = "decisionbox"
	}

	cfg := gomongo.DefaultConfig()
	cfg.URI = mongoURI
	cfg.Database = mongoDBName
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("mongodb connection failed: %w", err)
	}
	defer func() {
		if err := client.Disconnect(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: MongoDB disconnect failed: %v\n", err)
		}
	}()
	fmt.Printf("Connected to MongoDB (%s)\n", mongoDBName)
	if dryRun {
		fmt.Println("DRY RUN — nothing will be written.")
	}

	projects, err := ledgerProjectIDs(ctx, client, projectID)
	if err != nil {
		return fmt.Errorf("list projects with a ledger: %w", err)
	}
	fmt.Printf("Found %d project(s) with ledger findings\n\n", len(projects))

	// One project's failure must not abandon the rest — this is a one-shot an
	// operator runs over every project, and a partial result that says which
	// project failed is more useful than nothing.
	var totals ledgerGraphCounts
	failed := 0
	for _, pid := range projects {
		counts, err := backfillLedgerGraphProject(ctx, client, pid, batchSize, closeWindow, dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error on project %s: %v\n", pid, err)
			failed++
			continue
		}
		printLedgerGraphCounts(pid, counts)
		totals.add(counts)
	}
	fmt.Println()
	printLedgerGraphCounts("TOTAL", totals)
	if failed > 0 {
		return fmt.Errorf("%d of %d project(s) failed; see the errors above", failed, len(projects))
	}
	fmt.Println("\nBackfill complete.")
	return nil
}

func (c *ledgerGraphCounts) add(o ledgerGraphCounts) {
	c.FindingsScanned += o.FindingsScanned
	c.FindingsLinked += o.FindingsLinked
	c.FindingsAlreadyLinked += o.FindingsAlreadyLinked
	c.FindingsUnlinkable += o.FindingsUnlinkable
	c.SightingsWritten += o.SightingsWritten
	c.StatusTrailsWritten += o.StatusTrailsWritten
	c.TasksScanned += o.TasksScanned
	c.TasksClosedStamped += o.TasksClosedStamped
	c.TasksCloseRunResolved += o.TasksCloseRunResolved
	c.TasksCloseRunAmbiguous += o.TasksCloseRunAmbiguous
	c.TasksCloseRunNoRun += o.TasksCloseRunNoRun
}

func printLedgerGraphCounts(label string, c ledgerGraphCounts) {
	fmt.Printf("%s\n", label)
	fmt.Printf("  findings:  %d scanned, %d linked to an insight, %d already linked, %d not linkable\n",
		c.FindingsScanned, c.FindingsLinked, c.FindingsAlreadyLinked, c.FindingsUnlinkable)
	fmt.Printf("  trails:    %d sightings, %d status histories\n", c.SightingsWritten, c.StatusTrailsWritten)
	fmt.Printf("  tasks:     %d scanned, %d close stamps, %d closing runs resolved, %d unset (several candidate runs), %d unset (no candidate run)\n",
		c.TasksScanned, c.TasksClosedStamped, c.TasksCloseRunResolved,
		c.TasksCloseRunAmbiguous, c.TasksCloseRunNoRun)
}

// ledgerProjectIDs returns the project ids to process: the one asked for, or
// every project that has ledger findings.
func ledgerProjectIDs(ctx context.Context, client *gomongo.Client, projectID string) ([]string, error) {
	if projectID != "" {
		return []string{projectID}, nil
	}
	raw, err := client.Collection(gomongo.CollectionDiscoveryLedgerFindings).Distinct(ctx, "project_id", bson.M{})
	if err != nil {
		return nil, fmt.Errorf("distinct project ids: %w", err)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// backfillLedgerGraphProject recovers one project's derivable edges.
func backfillLedgerGraphProject(ctx context.Context, client *gomongo.Client, projectID string,
	batchSize int, closeWindow time.Duration, dryRun bool) (ledgerGraphCounts, error) {
	var counts ledgerGraphCounts

	findings, err := loadBackfillFindings(ctx, client, projectID)
	if err != nil {
		return counts, err
	}
	counts.FindingsScanned = len(findings)

	// Finding → insight, by exact normalized key.
	//
	// NormalizedFindingKey(area, name) is computable from a standalone
	// insight's analysis_area + name, and findings already store the key, so an
	// exact match is a real equality and not a similarity score. A finding that
	// absorbed a REWORDED insight through the semantic-dedup path will not
	// match, and is counted rather than guessed at.
	insightsByKey, err := loadInsightKeyIndex(ctx, client, projectID)
	if err != nil {
		return counts, err
	}

	// Runs, for resolving a discovery id to the run that produced it and for
	// dating a task's closing run.
	runsByDiscovery, runTimes, err := loadRunIndex(ctx, client, projectID)
	if err != nil {
		return counts, err
	}

	writes := make([]mongo.WriteModel, 0, batchSize)
	flush := func(collection string) error {
		if len(writes) == 0 || dryRun {
			writes = writes[:0]
			return nil
		}
		if _, err := client.Collection(collection).BulkWrite(ctx, writes,
			options.BulkWrite().SetOrdered(false)); err != nil {
			return fmt.Errorf("bulk write %s: %w", collection, err)
		}
		writes = writes[:0]
		return nil
	}

	for _, f := range findings {
		set := bson.M{}

		if len(f.SourceInsightIDs) > 0 {
			counts.FindingsAlreadyLinked++
		} else if ids := insightsByKey[f.NormalizedKey]; len(ids) > 0 {
			set["source_insight_ids"] = ids
			counts.FindingsLinked++
		} else {
			counts.FindingsUnlinkable++
		}

		// One synthetic sighting from first_seen + source_discovery_id. The
		// intermediate runs a finding was seen in are genuinely unrecoverable —
		// seen_count is a bare counter and nothing recorded when each sighting
		// happened — so they are not invented. SeenCount stays the honest total
		// and the trail says it holds one entry.
		if len(f.Sightings) == 0 && f.SourceDiscoveryID != "" {
			date := f.FirstSeen
			if date.IsZero() {
				date = f.LastSeen
			}
			set["sightings"] = []commonmodels.FindingSighting{{
				RunID:         runsByDiscovery[f.SourceDiscoveryID],
				DiscoveryID:   f.SourceDiscoveryID,
				Date:          date,
				KeyMetric:     f.KeyMetric,
				AffectedCount: f.AffectedCount,
				Severity:      f.Severity,
			}}
			counts.SightingsWritten++
		}

		// A status with no trail at all reads as a status that was never set.
		// One entry, marked as derived, says "this is where it stands and we do
		// not know why" — which is the truth — rather than inventing a reason.
		if len(f.StatusHistory) == 0 && f.Status != "" {
			at := f.UpdatedAt
			if at.IsZero() {
				at = f.LastSeen
			}
			set["status_history"] = []commonmodels.FindingStatusChange{{
				Status:      f.Status,
				RunID:       runsByDiscovery[f.SourceDiscoveryID],
				DiscoveryID: f.SourceDiscoveryID,
				At:          at,
				Source:      commonmodels.FindingStatusSourceBackfill,
			}}
			counts.StatusTrailsWritten++
		}

		if len(set) == 0 {
			continue
		}
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": f.ID, "project_id": projectID}).
			SetUpdate(bson.M{"$set": set}))
		if len(writes) >= batchSize {
			if err := flush(gomongo.CollectionDiscoveryLedgerFindings); err != nil {
				return counts, err
			}
		}
	}
	if err := flush(gomongo.CollectionDiscoveryLedgerFindings); err != nil {
		return counts, err
	}

	// Task close metadata. closed_at is simply updated_at — a closed task's
	// last write IS its close. The closing RUN is an inference, so it is
	// stamped only when exactly one run falls inside the window; two runs
	// inside it means the answer is unknown, and an unknown closing run is
	// better left unset than set to one of two candidates.
	tasks, err := loadBackfillTasks(ctx, client, projectID)
	if err != nil {
		return counts, err
	}
	counts.TasksScanned = len(tasks)
	for _, task := range tasks {
		if !task.ClosedAt.IsZero() || task.UpdatedAt.IsZero() {
			continue
		}
		set := bson.M{"closed_at": task.UpdatedAt}
		counts.TasksClosedStamped++
		runID, discoveryID, candidates := soleRunNear(runTimes, task.UpdatedAt, closeWindow)
		switch candidates {
		case 1:
			set["closed_run_id"] = runID
			if discoveryID != "" {
				set["closed_discovery_id"] = discoveryID
			}
			counts.TasksCloseRunResolved++
		case 0:
			counts.TasksCloseRunNoRun++
		default:
			counts.TasksCloseRunAmbiguous++
		}
		// resolved_by_finding_ids is never derived. Nothing stored says which
		// finding answered a task, and a plausible-looking guess here would be
		// indistinguishable from a real answer on the page.
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": task.ID, "project_id": projectID}).
			SetUpdate(bson.M{"$set": set}))
		if len(writes) >= batchSize {
			if err := flush(gomongo.CollectionDiscoveryLedgerTasks); err != nil {
				return counts, err
			}
		}
	}
	if err := flush(gomongo.CollectionDiscoveryLedgerTasks); err != nil {
		return counts, err
	}

	return counts, nil
}

func loadBackfillFindings(ctx context.Context, client *gomongo.Client, projectID string) ([]commonmodels.LedgerFinding, error) {
	cur, err := client.Collection(gomongo.CollectionDiscoveryLedgerFindings).
		Find(ctx, bson.M{"project_id": projectID})
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

func loadBackfillTasks(ctx context.Context, client *gomongo.Client, projectID string) ([]commonmodels.LedgerTask, error) {
	cur, err := client.Collection(gomongo.CollectionDiscoveryLedgerTasks).Find(ctx, bson.M{
		"project_id": projectID,
		"status": bson.M{"$in": []string{
			commonmodels.LedgerTaskStatusDone, commonmodels.LedgerTaskStatusDropped,
		}},
	})
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

// loadInsightKeyIndex maps NormalizedFindingKey(area, name) to the insight ids
// that produce it. A key with several insights behind it (the same finding
// surfaced by several runs) links them all: each one really is a source.
func loadInsightKeyIndex(ctx context.Context, client *gomongo.Client, projectID string) (map[string][]string, error) {
	cur, err := client.Collection(collectionInsights).Find(ctx,
		bson.M{"project_id": projectID},
		options.Find().SetProjection(bson.M{"_id": 1, "analysis_area": 1, "name": 1}))
	if err != nil {
		return nil, fmt.Errorf("list insights: %w", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID           string `bson:"_id"`
		AnalysisArea string `bson:"analysis_area"`
		Name         string `bson:"name"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode insights: %w", err)
	}
	idx := make(map[string][]string, len(rows))
	for _, r := range rows {
		if r.ID == "" || r.Name == "" {
			continue
		}
		key := commonmodels.NormalizedFindingKey(r.AnalysisArea, r.Name)
		idx[key] = append(idx[key], r.ID)
	}
	return idx, nil
}

// collectionInsights is the standalone insight collection. go-common defines
// name constants for the collections both services write; `insights` is read
// here and by the enterprise API as a literal, as it is elsewhere.
const collectionInsights = "insights"

// runRef is a run and the discovery it produced, with the time it finished.
type runRef struct {
	RunID       string
	DiscoveryID string
	At          time.Time
}

// loadRunIndex returns discovery id → run id, and the project's runs in time
// order for the closing-run inference.
func loadRunIndex(ctx context.Context, client *gomongo.Client, projectID string) (map[string]string, []runRef, error) {
	cur, err := client.Collection(gomongo.CollectionDiscoveryRuns).Find(ctx,
		bson.M{"project_id": projectID},
		options.Find().
			SetProjection(bson.M{"_id": 1, "discovery_id": 1, "completed_at": 1, "updated_at": 1, "started_at": 1}).
			SetSort(bson.D{{Key: "started_at", Value: 1}}))
	if err != nil {
		return nil, nil, fmt.Errorf("list discovery runs: %w", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID          string     `bson:"_id"`
		DiscoveryID string     `bson:"discovery_id"`
		CompletedAt *time.Time `bson:"completed_at"`
		UpdatedAt   time.Time  `bson:"updated_at"`
		StartedAt   time.Time  `bson:"started_at"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("decode discovery runs: %w", err)
	}
	byDiscovery := make(map[string]string, len(rows))
	refs := make([]runRef, 0, len(rows))
	for _, r := range rows {
		if r.DiscoveryID != "" {
			byDiscovery[r.DiscoveryID] = r.ID
		}
		// A task is closed by the reflection phase, which runs after the run
		// completed — so the completion time is the one to compare against.
		at := r.UpdatedAt
		if r.CompletedAt != nil && !r.CompletedAt.IsZero() {
			at = *r.CompletedAt
		}
		if at.IsZero() {
			at = r.StartedAt
		}
		if at.IsZero() {
			continue
		}
		refs = append(refs, runRef{RunID: r.ID, DiscoveryID: r.DiscoveryID, At: at})
	}
	return byDiscovery, refs, nil
}

// soleRunNear returns the one run within window of at, plus how many runs were
// in the window at all — capped at 2, since "more than one" is all the caller
// needs to know.
//
// Two candidates is the case this exists for: on a project that runs often,
// several runs are near any timestamp, and picking the closest would stamp a
// specific run id on a guess. Zero candidates is a different diagnosis — there
// are no run documents to match against — and the caller reports the two apart
// so an operator is not sent looking for a tighter window that cannot help.
func soleRunNear(runs []runRef, at time.Time, window time.Duration) (runID, discoveryID string, candidates int) {
	var found runRef
	for _, r := range runs {
		delta := at.Sub(r.At)
		if delta < 0 {
			delta = -delta
		}
		if delta <= window {
			candidates++
			if candidates > 1 {
				return "", "", 2
			}
			found = r
		}
	}
	if candidates != 1 {
		return "", "", 0
	}
	return found.RunID, found.DiscoveryID, 1
}
