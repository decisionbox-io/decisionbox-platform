package backfill

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/managedai"
	"github.com/decisionbox-io/decisionbox/services/api/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// projectsCollection is where project documents live.
const projectsCollection = "projects"

// RunBackfillLLMLayers runs the backfill-llm-layers subcommand.
//
// Managed deployments have had their AI configuration stamped by managedai
// since long before per-layer models — but only `llm`, `blurb_llm` and
// `embedding`. Per-layer models added three more fields (`llm_providers`,
// `layer_models`, `llm_config_version`), and managedai writes them on a
// project WRITE. A project last written before that release carries none of
// them, so every layer resolves through the single legacy `llm` model instead
// of its own.
//
// Nothing is broken by that — the layer resolver falls back to the default row
// and the run succeeds. What cannot happen is the feature: per-layer routing,
// and any per-customer per-layer override in the gateway, have no effect on
// such a project. This backfill closes that gap on projects that already
// exist. New projects are stamped at create time and never need it.
func RunBackfillLLMLayers(args []string) {
	fs := flag.NewFlagSet("backfill-llm-layers", flag.ExitOnError)
	projectID := fs.String("project-id", "", "Project ID to backfill (default: every project that needs it)")
	dryRun := fs.Bool("dry-run", false, "Report what would change without writing anything")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	if err := runBackfillLLMLayers(*projectID, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "backfill-llm-layers: %v\n", err)
		os.Exit(1)
	}
}

// runBackfillLLMLayers owns the timeout so the caller can os.Exit on error
// without stranding a deferred cancel.
func runBackfillLLMLayers(projectID string, dryRun bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Managed mode only, and deliberately so. On a self-hosted deployment the
	// per-layer configuration belongs to the operator: a project with no
	// layer_models is one nobody has configured yet, and the product sends the
	// user to settings to choose. Stamping a preset there would answer a
	// question the user is meant to answer and silence the gate that asks it.
	// There is no managed preset to stamp there either.
	managedai.Load()
	if !managedai.Enabled() {
		return fmt.Errorf("managed mode is off (%s unset): this backfill only applies to managed "+
			"deployments, where the AI configuration is operator-supplied. A self-hosted project's "+
			"per-layer configuration is the user's to set in project settings", managedai.EnvGatewayURL)
	}
	// Half-configured managed mode would stamp aliases pointing at a gateway
	// that cannot serve them. Fail before writing, not after.
	if err := managedai.Validate(); err != nil {
		return fmt.Errorf("managed mode is incompletely configured, refusing to stamp: %w", err)
	}

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
		if derr := client.Disconnect(ctx); derr != nil {
			fmt.Fprintf(os.Stderr, "Warning: MongoDB disconnect failed: %v\n", derr)
		}
	}()
	fmt.Printf("Connected to MongoDB (%s)\n", mongoDBName)
	if dryRun {
		fmt.Println("DRY RUN — nothing will be written.")
	}

	counts, err := backfillLLMLayers(ctx, client.Collection(projectsCollection), projectID, dryRun)
	if err != nil {
		return err
	}
	fmt.Printf("\nprojects scanned=%d stamped=%d already-configured=%d\n",
		counts.scanned, counts.stamped, counts.skipped)
	if dryRun && counts.stamped > 0 {
		fmt.Println("Re-run without -dry-run to apply.")
	}
	return nil
}

type llmLayerCounts struct{ scanned, stamped, skipped int }

// projectLayerState is the slice of a project document this backfill reads.
//
// A narrow projection rather than models.Project: `_id` is an ObjectID in
// Mongo while models.Project.ID is a string, and the update has to filter on
// the ObjectID. Reading only what is needed also means a project document that
// gains unrelated fields can never fail to decode here.
type projectLayerState struct {
	ID               primitive.ObjectID             `bson:"_id"`
	Name             string                         `bson:"name"`
	LayerModels      map[string]llmlayer.LayerModel `bson:"layer_models"`
	LLMConfigVersion int                            `bson:"llm_config_version"`
}

// backfillLLMLayers stamps the per-layer configuration onto projects that
// predate it. Split from the command so it can be tested against a real Mongo.
func backfillLLMLayers(ctx context.Context, col *mongo.Collection, projectID string, dryRun bool) (llmLayerCounts, error) {
	var counts llmLayerCounts

	filter := bson.M{}
	if projectID != "" {
		oid, err := primitive.ObjectIDFromHex(projectID)
		if err != nil {
			return counts, fmt.Errorf("invalid project ID %q: %w", projectID, err)
		}
		filter["_id"] = oid
	}

	// The managed preset is derived from the deployment's environment, not
	// from any project, so it is computed once. managedai.Apply is asked for
	// it — rather than the alias names being rebuilt here — so this backfill
	// never becomes a second definition of what a managed project looks like.
	var preset models.Project
	managedai.Apply(&preset)
	if len(preset.LayerModels) == 0 || preset.LLMConfigVersion == 0 {
		return counts, fmt.Errorf("managedai produced no per-layer configuration; refusing to stamp nothing")
	}

	cur, err := col.Find(ctx, filter)
	if err != nil {
		return counts, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	for cur.Next(ctx) {
		var p projectLayerState
		if derr := cur.Decode(&p); derr != nil {
			return counts, fmt.Errorf("decode project: %w", derr)
		}
		counts.scanned++

		if !needsLLMLayerStamp(p) {
			counts.skipped++
			continue
		}

		fmt.Printf("  %s  %-40s  -> %d layer rows, %d provider entry, version %d\n",
			p.ID.Hex(), truncate(p.Name, 40), len(preset.LayerModels), len(preset.LLMProviders), preset.LLMConfigVersion)
		counts.stamped++
		if dryRun {
			continue
		}

		// A targeted $set, NOT a whole-document write.
		//
		// managedai.Apply replaces the embedding block wholesale, which drops
		// a pinned `dimensions` when AI_GATEWAY_EMBED_DIMENSIONS is unset. On
		// a live project that value may have been probed and match an existing
		// vector collection, so rewriting the whole AI block to migrate three
		// fields risks a re-probe or a mismatch for no benefit. The other
		// fields need no migration regardless: managedai has written them on
		// every project write since long before per-layer models.
		if _, uerr := col.UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": bson.M{
			"llm_providers":      preset.LLMProviders,
			"layer_models":       preset.LayerModels,
			"llm_config_version": preset.LLMConfigVersion,
		}}); uerr != nil {
			return counts, fmt.Errorf("stamp project %s: %w", p.ID.Hex(), uerr)
		}
	}
	if cerr := cur.Err(); cerr != nil {
		return counts, fmt.Errorf("iterate projects: %w", cerr)
	}
	return counts, nil
}

// needsLLMLayerStamp reports whether a project is missing the per-layer
// configuration.
//
// Both halves are checked. A project with rows but no version reads as
// unconfigured to the gate; a version with no rows resolves nothing. Either
// way it has not been stamped by the current managedai, and either way the
// repair is the same. The version comparison is "<", not "== 0", so a project
// stamped by an older generation is picked up when the generation moves.
func needsLLMLayerStamp(p projectLayerState) bool {
	return p.LLMConfigVersion < llmlayer.ConfigVersion || len(p.LayerModels) == 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
