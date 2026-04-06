package backfill

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	qdrantstore "github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore/qdrant"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/azure"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"

	_ "github.com/decisionbox-io/decisionbox/providers/embedding/azure-openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/voyage"
)

// RunBackfillEmbeddings runs the backfill-embeddings subcommand.
func RunBackfillEmbeddings(args []string) {
	fs := flag.NewFlagSet("backfill-embeddings", flag.ExitOnError)

	projectID := fs.String("project-id", "", "Project ID to backfill (default: all projects with embedding configured)")
	reindex := fs.Bool("reindex", false, "Force re-embed all documents (for model changes)")
	batchSize := fs.Int("batch-size", 50, "Embedding batch size")
	dryRun := fs.Bool("dry-run", false, "Show what would be done without making changes")
	denormalizeOnly := fs.Bool("denormalize-only", false, "Only populate MongoDB collections without embedding or Qdrant indexing")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Load infra config from env
	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		fmt.Fprintf(os.Stderr, "Error: MONGODB_URI environment variable is required\n")
		os.Exit(1)
	}
	mongoDBName := os.Getenv("MONGODB_DB")
	if mongoDBName == "" {
		mongoDBName = "decisionbox"
	}

	// Connect to MongoDB
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = mongoURI
	mongoCfg.Database = mongoDBName

	mongoClient, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connection failed: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := mongoClient.Disconnect(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: MongoDB disconnect failed: %v\n", err)
		}
	}()

	fmt.Printf("Connected to MongoDB (%s)\n", mongoDBName)

	// Initialize secret provider
	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Secret provider init failed: %v\n", err)
		return
	}

	// Connect to Qdrant (optional for denormalize-only mode)
	var qdrant vectorstore.Provider
	qdrantURL := os.Getenv("QDRANT_URL")
	if qdrantURL != "" && !*denormalizeOnly {
		qdrant, err = initQdrant(qdrantURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Qdrant connection failed: %v\n", err)
			return
		}
		fmt.Printf("Connected to Qdrant (%s)\n", qdrantURL)
		if closer, ok := qdrant.(interface{ Close() error }); ok {
			defer closer.Close()
		}
	}

	// Find projects to process
	projects, err := findProjects(ctx, mongoClient, *projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find projects: %v\n", err)
		return
	}

	fmt.Printf("Found %d project(s) to process\n\n", len(projects))

	for _, proj := range projects {
		if err := processProject(ctx, mongoClient, secretProvider, qdrant, proj, *batchSize, *reindex, *dryRun, *denormalizeOnly); err != nil {
			fmt.Fprintf(os.Stderr, "Error processing project %s: %v\n", proj.ID(), err)
		}
	}

	fmt.Println("\nBackfill complete.")
}

type projectInfo struct {
	OID      primitive.ObjectID `bson:"_id"`
	Name     string             `bson:"name"`
	Domain   string             `bson:"domain"`
	Category string             `bson:"category"`
	Embedding struct {
		Provider string `bson:"provider"`
		Model    string `bson:"model"`
	} `bson:"embedding"`
}

func (p projectInfo) ID() string {
	return p.OID.Hex()
}

func findProjects(ctx context.Context, client *gomongo.Client, projectID string) ([]projectInfo, error) {
	filter := bson.M{}
	if projectID != "" {
		oid, err := primitive.ObjectIDFromHex(projectID)
		if err != nil {
			return nil, fmt.Errorf("invalid project ID %q: %w", projectID, err)
		}
		filter["_id"] = oid
	}

	cursor, err := client.Collection("projects").Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var projects []projectInfo
	if err := cursor.All(ctx, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func processProject(ctx context.Context, client *gomongo.Client, secretProvider gosecrets.Provider, qdrant vectorstore.Provider, proj projectInfo, batchSize int, reindex, dryRun, denormalizeOnly bool) error {
	fmt.Printf("=== Project: %s (%s) ===\n", proj.Name, proj.ID())

	// Step 1: Denormalize discoveries into insights/recommendations collections
	denormalized, err := denormalizeDiscoveries(ctx, client, proj, dryRun)
	if err != nil {
		return fmt.Errorf("denormalize: %w", err)
	}
	fmt.Printf("  Denormalized: %d insights, %d recommendations\n", denormalized.insights, denormalized.recommendations)

	if denormalizeOnly {
		fmt.Println("  Skipping embedding (--denormalize-only)")
		return nil
	}

	// Step 2: Embed and index
	if proj.Embedding.Provider == "" {
		fmt.Println("  No embedding provider configured — skipping embedding")
		return nil
	}

	if qdrant == nil {
		fmt.Println("  Qdrant not configured — skipping indexing")
		return nil
	}

	// Create embedding provider
	apiKey, _ := secretProvider.Get(ctx, proj.ID(), "embedding-api-key")
	embProvider, err := goembedding.NewProvider(proj.Embedding.Provider, goembedding.ProviderConfig{
		"api_key": apiKey,
		"model":   proj.Embedding.Model,
	})
	if err != nil {
		return fmt.Errorf("create embedding provider: %w", err)
	}

	dims := embProvider.Dimensions()
	modelName := embProvider.ModelName()

	if err := qdrant.EnsureCollection(ctx, dims); err != nil {
		return fmt.Errorf("ensure qdrant collection: %w", err)
	}

	// Find documents to embed
	filter := bson.M{"project_id": proj.ID()}
	if !reindex {
		filter["embedding_model"] = bson.M{"$in": []interface{}{nil, ""}}
	}

	if dryRun {
		insightCount, _ := client.Collection("insights").CountDocuments(ctx, filter)
		recCount, _ := client.Collection("recommendations").CountDocuments(ctx, filter)
		fmt.Printf("  [dry-run] Would embed %d insights + %d recommendations with %s\n", insightCount, recCount, modelName)
		return nil
	}

	// Process insights
	insightCount, err := embedCollection(ctx, client, qdrant, embProvider, "insights", "insight", filter, batchSize)
	if err != nil {
		return fmt.Errorf("embed insights: %w", err)
	}

	// Process recommendations
	recCount, err := embedCollection(ctx, client, qdrant, embProvider, "recommendations", "recommendation", filter, batchSize)
	if err != nil {
		return fmt.Errorf("embed recommendations: %w", err)
	}

	fmt.Printf("  Embedded and indexed: %d insights + %d recommendations (%s, %d dims)\n",
		insightCount, recCount, modelName, dims)

	return nil
}

type denormalizeResult struct {
	insights        int
	recommendations int
}

func denormalizeDiscoveries(ctx context.Context, client *gomongo.Client, proj projectInfo, dryRun bool) (denormalizeResult, error) {
	// Find all discoveries for this project
	cursor, err := client.Collection("discoveries").Find(ctx, bson.M{"project_id": proj.ID()},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return denormalizeResult{}, err
	}
	defer cursor.Close(ctx)

	var totalInsights, totalRecs int

	for cursor.Next(ctx) {
		var disc struct {
			ID              string    `bson:"_id"`
			ProjectID       string    `bson:"project_id"`
			Domain          string    `bson:"domain"`
			Category        string    `bson:"category"`
			DiscoveryDate   time.Time `bson:"discovery_date"`
			Insights        []bson.M  `bson:"insights"`
			Recommendations []bson.M  `bson:"recommendations"`
		}
		if err := cursor.Decode(&disc); err != nil {
			continue
		}

		// Check if this discovery is already denormalized
		existingCount, _ := client.Collection("insights").CountDocuments(ctx, bson.M{"discovery_id": disc.ID})
		if existingCount > 0 {
			continue // already done
		}

		if dryRun {
			totalInsights += len(disc.Insights)
			totalRecs += len(disc.Recommendations)
			continue
		}

		// Denormalize insights
		now := time.Now()
		for _, ins := range disc.Insights {
			doc := bson.M{
				"_id":           uuid.New().String(),
				"project_id":   disc.ProjectID,
				"discovery_id": disc.ID,
				"domain":       disc.Domain,
				"category":     disc.Category,
				"created_at":   now,
			}
			// Copy all fields from the embedded insight
			for k, v := range ins {
				if k != "id" { // skip the embedded ID, we use UUID
					doc[k] = v
				}
			}
			if _, err := client.Collection("insights").InsertOne(ctx, doc); err != nil {
				// Ignore duplicate key errors (idempotent)
				if !mongo.IsDuplicateKeyError(err) {
					return denormalizeResult{}, fmt.Errorf("insert insight: %w", err)
				}
			}
			totalInsights++
		}

		// Denormalize recommendations
		for _, rec := range disc.Recommendations {
			doc := bson.M{
				"_id":           uuid.New().String(),
				"project_id":   disc.ProjectID,
				"discovery_id": disc.ID,
				"domain":       disc.Domain,
				"category":     disc.Category,
				"created_at":   now,
			}
			for k, v := range rec {
				if k != "id" {
					doc[k] = v
				}
			}
			if _, err := client.Collection("recommendations").InsertOne(ctx, doc); err != nil {
				if !mongo.IsDuplicateKeyError(err) {
					return denormalizeResult{}, fmt.Errorf("insert recommendation: %w", err)
				}
			}
			totalRecs++
		}
	}

	return denormalizeResult{insights: totalInsights, recommendations: totalRecs}, nil
}

func embedCollection(ctx context.Context, client *gomongo.Client, qdrant vectorstore.Provider, embProvider goembedding.Provider, collection, docType string, filter bson.M, batchSize int) (int, error) {
	cursor, err := client.Collection(collection).Find(ctx, filter)
	if err != nil {
		return 0, err
	}
	defer cursor.Close(ctx)

	modelName := embProvider.ModelName()
	total := 0
	var batch []batchItem

	for cursor.Next(ctx) {
		var doc struct {
			ID            string    `bson:"_id"`
			ProjectID     string    `bson:"project_id"`
			DiscoveryID   string    `bson:"discovery_id"`
			Name          string    `bson:"name"`
			Title         string    `bson:"title"`
			Description   string    `bson:"description"`
			AnalysisArea  string    `bson:"analysis_area"`
			Severity      string    `bson:"severity"`
			TargetSegment string    `bson:"target_segment"`
			Confidence    float64   `bson:"confidence"`
			CreatedAt     time.Time `bson:"created_at"`
		}
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		// Build embedding text
		var text string
		if docType == "insight" {
			text = fmt.Sprintf("%s. %s. Area: %s. Severity: %s. Segment: %s.",
				doc.Name, doc.Description, doc.AnalysisArea, doc.Severity, doc.TargetSegment)
		} else {
			text = fmt.Sprintf("%s. %s. Segment: %s.",
				doc.Title, doc.Description, doc.TargetSegment)
		}

		batch = append(batch, batchItem{
			ID:           doc.ID,
			Text:         text,
			ProjectID:    doc.ProjectID,
			DiscoveryID:  doc.DiscoveryID,
			AnalysisArea: doc.AnalysisArea,
			Severity:     doc.Severity,
			Confidence:   doc.Confidence,
			CreatedAt:    doc.CreatedAt,
		})

		if len(batch) >= batchSize {
			if err := processBatch(ctx, client, qdrant, embProvider, collection, docType, batch, modelName); err != nil {
				return total, err
			}
			total += len(batch)
			batch = batch[:0]
		}
	}

	// Process remaining batch
	if len(batch) > 0 {
		if err := processBatch(ctx, client, qdrant, embProvider, collection, docType, batch, modelName); err != nil {
			return total, err
		}
		total += len(batch)
	}

	return total, nil
}

type batchItem struct {
	ID           string
	Text         string
	ProjectID    string
	DiscoveryID  string
	AnalysisArea string
	Severity     string
	Confidence   float64
	CreatedAt    time.Time
}

func processBatch(ctx context.Context, client *gomongo.Client, qdrant vectorstore.Provider, embProvider goembedding.Provider, collection, docType string, batch []batchItem, modelName string) error {
	texts := make([]string, len(batch))
	for i, b := range batch {
		texts[i] = b.Text
	}

	vectors, err := embProvider.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed batch: %w", err)
	}

	var points []vectorstore.Point
	for i, b := range batch {
		// Update MongoDB
		_, err := client.Collection(collection).UpdateByID(ctx, b.ID, bson.M{
			"$set": bson.M{
				"embedding_text":  b.Text,
				"embedding_model": modelName,
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: failed to update %s %s: %v\n", docType, b.ID, err)
		}

		payload := map[string]interface{}{
			"type":            docType,
			"project_id":     b.ProjectID,
			"discovery_id":   b.DiscoveryID,
			"embedding_model": modelName,
			"confidence":     b.Confidence,
			"created_at":     b.CreatedAt.Format(time.RFC3339),
		}
		if b.Severity != "" {
			payload["severity"] = b.Severity
		}
		if b.AnalysisArea != "" {
			payload["analysis_area"] = b.AnalysisArea
		}

		points = append(points, vectorstore.Point{
			ID:     b.ID,
			Vector: vectors[i],
			Payload: payload,
		})
	}

	if err := qdrant.Upsert(ctx, points); err != nil {
		return fmt.Errorf("upsert qdrant batch: %w", err)
	}

	return nil
}

func initSecretProvider(client *gomongo.Client) (gosecrets.Provider, error) {
	cfg := gosecrets.LoadConfig()
	if cfg.Provider == "mongodb" || cfg.Provider == "" {
		return mongoSecrets.NewMongoProvider(
			client.Collection("secrets"),
			cfg.Namespace,
			cfg.EncryptionKey,
		)
	}
	return gosecrets.NewProvider(cfg)
}

func initQdrant(url string) (vectorstore.Provider, error) {
	host := url
	port := 6334
	if parts := splitHostPort(host); len(parts) == 2 {
		host = parts[0]
		if _, err := fmt.Sscanf(parts[1], "%d", &port); err != nil {
			return nil, fmt.Errorf("invalid Qdrant port %q: %w", parts[1], err)
		}
	}

	provider, err := qdrantstore.New(qdrantstore.Config{
		Host:   host,
		Port:   port,
		APIKey: os.Getenv("QDRANT_API_KEY"),
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := provider.HealthCheck(ctx); err != nil {
		return nil, err
	}

	return provider, nil
}

func splitHostPort(s string) []string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
