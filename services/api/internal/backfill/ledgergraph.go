package backfill

import (
	"context"
	"errors"
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
	// runStatusCompleted is the only run status that can have closed a thread:
	// the reflection phase runs after a SUCCESSFUL discovery, so a failed,
	// cancelled or still-running row is never the closing run. Leaving them in
	// the candidate set made a real closing run look ambiguous, and could
	// stamp a failed run as the one that closed a thread.
	runStatusCompleted = "completed"
	// runStatusPending / runStatusRunning are the lifecycle states a project
	// can still be writing its ledger from. The backfill derives provenance
	// from a snapshot and the reflection phase replaces whole finding
	// documents, so a project mid-run is left alone rather than raced.
	runStatusPending = "pending"
	runStatusRunning = "running"
)

// ledgerGraphCounts is what the run reports. The unlinked counts are the point:
// they are how an operator knows the gap exists rather than assuming the
// backfill filled it.
type ledgerGraphCounts struct {
	// ProjectsSkippedInFlight counts projects left untouched because a run
	// was still going. Reported rather than silently passed over: the
	// operator has to know the project still needs a pass.
	ProjectsSkippedInFlight int
	FindingsScanned         int
	FindingsLinked        int
	FindingsAlreadyLinked int
	FindingsUnlinkable    int
	// FindingsUnaddressable is the different diagnosis: the insight IS here
	// and its key matches, but this project's two insight id spaces diverged
	// — `backfill-embeddings` minted fresh standalone ids and dropped the
	// embedded ones — so no single id reaches both the insight page and the
	// graph's own stub lookup. No window and no matching fixes it; it needs
	// the id spaces reconciled.
	FindingsUnaddressable int
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
		if errors.Is(err, errProjectBecameBusy) {
			// Not a failure of the command: the project started working
			// again mid-pass and has to be done once it is quiet.
			counts.ProjectsSkippedInFlight = 1
			printLedgerGraphCounts(pid, counts)
			totals.add(counts)
			continue
		}
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
	c.ProjectsSkippedInFlight += o.ProjectsSkippedInFlight
	c.FindingsScanned += o.FindingsScanned
	c.FindingsLinked += o.FindingsLinked
	c.FindingsAlreadyLinked += o.FindingsAlreadyLinked
	c.FindingsUnlinkable += o.FindingsUnlinkable
	c.FindingsUnaddressable += o.FindingsUnaddressable
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
	if c.ProjectsSkippedInFlight > 0 {
		// Loud, and never folded into a "nothing to do": the project was not
		// backfilled and someone has to come back to it.
		fmt.Printf("  SKIPPED:   %d project(s) had a run in flight — re-run the backfill for them once it has finished\n",
			c.ProjectsSkippedInFlight)
	}
	fmt.Printf("  findings:  %d scanned, %d linked to an insight, %d already linked, %d not linkable\n",
		c.FindingsScanned, c.FindingsLinked, c.FindingsAlreadyLinked, c.FindingsUnlinkable)
	if c.FindingsUnaddressable > 0 {
		fmt.Printf("             %d matched an insight whose id cannot address both the insight page and the graph — this project's id spaces diverged and matching cannot fix it\n",
			c.FindingsUnaddressable)
	}
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

	// A run in flight is the one thing this command cannot work around. It
	// derives from a snapshot read here, and the reflection phase at the end
	// of a run REPLACES a finding document whole — so a run that reflects
	// between this read and these writes would overwrite provenance that was
	// just recovered, and the per-field guards below, which skip a field that
	// is already populated, could never put it back.
	inFlight, err := runInFlight(ctx, client, projectID)
	if err != nil {
		return counts, err
	}
	if inFlight {
		counts.ProjectsSkippedInFlight = 1
		return counts, nil
	}

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
	resolvable, err := loadEmbeddedInsightIDs(ctx, client, projectID)
	if err != nil {
		return counts, err
	}
	insightsByKey, unaddressable, err := loadInsightKeyIndex(ctx, client, projectID, resolvable)
	if err != nil {
		return counts, err
	}

	// Runs, for resolving a discovery id to the run that produced it and for
	// dating a task's closing run.
	runsByDiscovery, runTimes, err := loadRunIndex(ctx, client, projectID)
	if err != nil {
		return counts, err
	}
	discoveryByRun := make(map[string]string, len(runTimes))
	for _, r := range runTimes {
		if r.RunID != "" && r.DiscoveryID != "" {
			discoveryByRun[r.RunID] = r.DiscoveryID
		}
	}

	writes := make([]mongo.WriteModel, 0, batchSize)
	flush := func(collection string) error {
		if len(writes) == 0 || dryRun {
			writes = writes[:0]
			return nil
		}
		// Asked again, immediately before the write. A reflection that began
		// after the check at the top would read a finding before this
		// recovered its history and replace the document after — losing it,
		// and losing it permanently, because the next pass skips a field that
		// is no longer empty. Nothing here can stop that reflection; what it
		// can do is not add to the damage, and say so rather than report a
		// project as done when it was abandoned halfway.
		busy, err := runInFlight(ctx, client, projectID)
		if err != nil {
			return err
		}
		if busy {
			writes = writes[:0]
			return errProjectBecameBusy
		}
		if _, err := client.Collection(collection).BulkWrite(ctx, writes,
			options.BulkWrite().SetOrdered(false)); err != nil {
			return fmt.Errorf("bulk write %s: %w", collection, err)
		}
		writes = writes[:0]
		return nil
	}

	// Each field is written under its own filter, re-asserting in MongoDB that
	// it is still absent. The snapshot above was read at the start of the run;
	// a discovery that reflects while this is working populates real
	// sightings, insight ids and status history, and a $set guarded only by
	// the document id would overwrite that live provenance with values derived
	// from the snapshot. One update per field is a few more writes on a
	// one-shot, and it is the only way the guard actually holds.
	addWrite := func(id, field string, value interface{}) {
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{
				"_id":        id,
				"project_id": projectID,
				"$or": []bson.M{
					{field: bson.M{"$exists": false}},
					{field: nil},
					{field: bson.M{"$size": 0}},
				},
			}).
			SetUpdate(bson.M{"$set": bson.M{field: value}}))
	}

	// addToSet adds to an array that already has entries, which addWrite's
	// "still absent" filter deliberately refuses to touch. Guarded by the
	// document id alone because $addToSet cannot clobber: it adds what is
	// missing and leaves everything else, including whatever a run wrote
	// while this was working.
	addToSet := func(id, field string, values []string) {
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id, "project_id": projectID}).
			SetUpdate(bson.M{"$addToSet": bson.M{field: bson.M{"$each": values}}}))
	}

	for _, f := range findings {
		pending := 0

		// A finding seen once after this provenance shipped but before this
		// command ran carries exactly one insight id — the one that run
		// produced. Its older ones are still recoverable, and treating a
		// non-empty array as complete would leave them behind for good, since
		// every later pass would skip it for the same reason. Only the ids
		// that are missing are added, with $addToSet so a run writing beside
		// this cannot be overwritten and a repeat changes nothing.
		known := insightsByKey[f.NormalizedKey]
		switch {
		case len(known) == 0:
			switch {
			case len(f.SourceInsightIDs) > 0:
				counts.FindingsAlreadyLinked++
			case has(unaddressable, f.NormalizedKey):
				counts.FindingsUnaddressable++
			default:
				counts.FindingsUnlinkable++
			}
		case len(f.SourceInsightIDs) == 0:
			addWrite(f.ID, "source_insight_ids", known)
			pending++
			counts.FindingsLinked++
		default:
			if missing := notIn(known, f.SourceInsightIDs); len(missing) > 0 {
				addToSet(f.ID, "source_insight_ids", missing)
				pending++
				counts.FindingsLinked++
			} else {
				counts.FindingsAlreadyLinked++
			}
		}

		// One synthetic sighting from first_seen + source_discovery_id. The
		// intermediate runs a finding was seen in are genuinely unrecoverable —
		// seen_count is a bare counter and nothing recorded when each sighting
		// happened — so they are not invented. SeenCount stays the honest total
		// and the trail says it holds one entry.
		// LastSeen, not FirstSeen. source_discovery_id and the magnitude
		// fields are overwritten on every merge, so they all describe the
		// LATEST observation — dating that observation from the first would
		// place the run's own metrics before the run existed.
		if len(f.Sightings) == 0 && f.SourceDiscoveryID != "" {
			date := f.LastSeen
			if date.IsZero() {
				date = f.FirstSeen
			}
			addWrite(f.ID, "sightings", []commonmodels.FindingSighting{{
				RunID:         runsByDiscovery[f.SourceDiscoveryID],
				DiscoveryID:   f.SourceDiscoveryID,
				Date:          date,
				KeyMetric:     f.KeyMetric,
				AffectedCount: f.AffectedCount,
				Severity:      f.Severity,
			}})
			pending++
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
			// Deliberately unattributed. source_discovery_id names the run
			// that last SAW the finding, and a status is moved by a run that
			// re-judges it — which can do so on other evidence, without
			// seeing it again, touching only the status and updated_at. The
			// two are the same run often enough to look right and not often
			// enough to be true, and a wrong run on a verdict is worse than
			// none: it would place the verdict in the wrong window.
			addWrite(f.ID, "status_history", []commonmodels.FindingStatusChange{{
				Status: f.Status,
				At:     at,
				Source: commonmodels.FindingStatusSourceBackfill,
			}})
			pending++
			counts.StatusTrailsWritten++
		}

		if pending == 0 {
			continue
		}
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
		if task.UpdatedAt.IsZero() {
			continue
		}
		// The close date and the closing run are recovered independently. An
		// earlier pass may have stamped the date and left the run unresolved
		// because two runs were near it; a later pass with a tighter
		// --close-window should be able to finish the job rather than skip the
		// task because one of its fields is already there.
		pending := 0
		if task.ClosedAt.IsZero() {
			addWrite(task.ID, "closed_at", task.UpdatedAt)
			pending++
			counts.TasksClosedStamped++
		}
		if task.ClosedRunID == "" {
			runID, discoveryID, candidates := soleRunNear(runTimes, task.UpdatedAt, closeWindow)
			switch candidates {
			case 1:
				addWrite(task.ID, "closed_run_id", runID)
				pending++
				if discoveryID != "" {
					addWrite(task.ID, "closed_discovery_id", discoveryID)
					pending++
				}
				counts.TasksCloseRunResolved++
			case 0:
				counts.TasksCloseRunNoRun++
			default:
				counts.TasksCloseRunAmbiguous++
			}
		}
		// The run id and its discovery are two writes in one unordered batch,
		// so an interrupted pass can leave the run stamped and the discovery
		// missing — and the guard above would then skip the task forever,
		// because its run id is already there. Recovering the discovery needs
		// no inference: the run names its own.
		if task.ClosedRunID != "" && task.ClosedDiscoveryID == "" {
			if discoveryID := discoveryByRun[task.ClosedRunID]; discoveryID != "" {
				addWrite(task.ID, "closed_discovery_id", discoveryID)
				pending++
			}
		}
		// resolved_by_finding_ids is never derived. Nothing stored says which
		// finding answered a task, and a plausible-looking guess here would be
		// indistinguishable from a real answer on the page.
		if pending == 0 {
			continue
		}
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

// loadEmbeddedInsightIDs is the set of insight ids that appear INSIDE the
// project's discovery documents.
//
// A standalone insight's `_id` is normally the same UUID the agent assigned to
// the embedded one, which is what makes a finding's provenance clickable. It
// is not always: `backfill-embeddings` mints a fresh UUID for an insight whose
// embedded id was empty — the shape of discoveries old enough to predate that
// id — and the insight page resolves an id against the discovery document. A
// link built from such an id reports success here and 404s for the reader.
//
// So the backfill links only ids that exist in both places, and counts the
// rest as unlinkable. A missing link says "not linked"; a broken one looks
// right until someone clicks it.
func loadEmbeddedInsightIDs(ctx context.Context, client *gomongo.Client, projectID string) (map[string]struct{}, error) {
	cur, err := client.Collection(gomongo.CollectionDiscoveries).Find(ctx,
		bson.M{"project_id": projectID},
		options.Find().SetProjection(bson.M{"insights.id": 1}))
	if err != nil {
		return nil, fmt.Errorf("list discovery insight ids: %w", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		Insights []struct {
			ID string `bson:"id"`
		} `bson:"insights"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode discovery insight ids: %w", err)
	}
	out := map[string]struct{}{}
	for _, r := range rows {
		for _, in := range r.Insights {
			if in.ID != "" {
				out[in.ID] = struct{}{}
			}
		}
	}
	return out, nil
}

// loadInsightKeyIndex maps NormalizedFindingKey(area, name) to the insight ids
// that produce it. A key with several insights behind it (the same finding
// surfaced by several runs) links them all: each one really is a source.
//
// Only ids the discovery documents also carry are included — see
// loadEmbeddedInsightIDs.
func loadInsightKeyIndex(
	ctx context.Context, client *gomongo.Client, projectID string, resolvable map[string]struct{},
) (idx map[string][]string, unaddressable map[string]struct{}, err error) {
	unaddressable = map[string]struct{}{}
	cur, err := client.Collection(collectionInsights).Find(ctx,
		bson.M{"project_id": projectID},
		options.Find().SetProjection(bson.M{"_id": 1, "analysis_area": 1, "name": 1}))
	if err != nil {
		return nil, nil, fmt.Errorf("list insights: %w", err)
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID           string `bson:"_id"`
		AnalysisArea string `bson:"analysis_area"`
		Name         string `bson:"name"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("decode insights: %w", err)
	}
	idx = make(map[string][]string, len(rows))
	for _, r := range rows {
		if r.ID == "" || r.Name == "" {
			continue
		}
		if _, clickable := resolvable[r.ID]; !clickable {
			// The insight page resolves an id out of the DISCOVERY document,
			// so an id that is not in there reaches nothing. On a project
			// repaired by `backfill-embeddings` that is every standalone row:
			// it minted a fresh _id and dropped the embedded one, so the two
			// id spaces diverged and no single id addresses both the page and
			// the graph's own stub lookup. Counted apart from a reworded
			// insight, because the two call for completely different
			// responses — this one cannot be fixed by matching harder.
			unaddressable[commonmodels.NormalizedFindingKey(r.AnalysisArea, r.Name)] = struct{}{}
			continue
		}
		key := commonmodels.NormalizedFindingKey(r.AnalysisArea, r.Name)
		idx[key] = append(idx[key], r.ID)
		delete(unaddressable, key)
	}
	return idx, unaddressable, nil
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

// has is a set membership test that reads as one.
func has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// notIn returns the values of want that are missing from have.
func notIn(want, have []string) []string {
	present := make(map[string]struct{}, len(have))
	for _, h := range have {
		present[h] = struct{}{}
	}
	missing := make([]string, 0, len(want))
	for _, w := range want {
		if _, ok := present[w]; ok {
			continue
		}
		present[w] = struct{}{} // de-dupe within want too
		missing = append(missing, w)
	}
	return missing
}

// errProjectBecameBusy means a reflection started while this project was being
// backfilled. Not an error with the command — the project simply has to be
// done again once it is quiet — so the caller reports it as a skip.
var errProjectBecameBusy = errors.New("a reflection started while this project was being backfilled")

// runInFlight reports whether anything is still writing this project's ledger.
//
// Two questions, because one does not answer it. A pending or running run has
// its reflection phase still ahead of it. But a run is stamped `completed`
// BEFORE that phase runs — so a project whose every run reads as finished can
// still be mid-reflection, which is exactly when a finding document is being
// replaced. The phase marks itself for the duration, and that marker is the
// second question. A marker older than commonmodels.ReflectionMarkerTTL is
// disbelieved: the phase is bounded, and an agent killed mid-phase never clears
// its own.
func runInFlight(ctx context.Context, client *gomongo.Client, projectID string) (bool, error) {
	n, err := client.Collection(gomongo.CollectionDiscoveryRuns).CountDocuments(ctx,
		bson.M{
			"project_id": projectID,
			"status":     bson.M{"$in": []string{runStatusPending, runStatusRunning}},
		},
		options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("check for a run in flight: %w", err)
	}
	if n > 0 {
		return true, nil
	}

	var ledger struct {
		Reflecting []commonmodels.ReflectionMarker `bson:"reflecting"`
	}
	err = client.Collection(gomongo.CollectionDiscoveryLedger).
		FindOne(ctx, bson.M{"project_id": projectID},
			options.FindOne().SetProjection(bson.M{gomongo.ReflectingKey: 1})).
		Decode(&ledger)
	if err == mongo.ErrNoDocuments {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check for a reflection in flight: %w", err)
	}
	now := time.Now()
	for _, in := range ledger.Reflecting {
		if in.Fresh(now) {
			return true, nil
		}
	}
	return false, nil
}

// loadRunIndex returns discovery id → run id, and the project's runs in time
// order for the closing-run inference.
func loadRunIndex(ctx context.Context, client *gomongo.Client, projectID string) (map[string]string, []runRef, error) {
	cur, err := client.Collection(gomongo.CollectionDiscoveryRuns).Find(ctx,
		bson.M{"project_id": projectID, "status": runStatusCompleted},
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

// soleRunNear returns the one run that finished within window BEFORE at, plus
// how many runs qualified at all — capped at 2, since "more than one" is all
// the caller needs to know.
//
// Before, never after. Reflection is what closes a task and it runs after the
// completion event, so a run that finished after the task was last written
// cannot be the run that closed it. Measuring the gap in both directions let a
// later run be stamped onto a task it could not have touched — and the whole
// point of this command is that what it writes is derivable, not guessed.
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
			continue
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
