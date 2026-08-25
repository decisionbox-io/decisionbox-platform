// validation-replay is a diagnostic CLI that replays the LLM-native
// verifier + refuter over a persisted Mongo discovery, without
// triggering a fresh agent run. It calls into the production
// verifier package (services/agent/internal/validation/verifier) — the
// SAME code path the orchestrator wires in Phase 4.5 / 5.5 — so any
// behavior surfaced here mirrors what a live discovery would produce.
//
// Use cases:
//
//   - Regression-test the production verifier against past discoveries
//     after a prompt or config change.
//   - Sweep env-var knobs (token caps, refuter on/off, numeric
//     tolerance, min-sample threshold) without redeploying the agent.
//   - Inspect per-claim evidence for a specific insight without
//     re-running an entire discovery.
//
// Usage:
//
//	go run ./services/agent/cmd/validation-replay \
//	    --project-id <ID> \
//	    --discovery-id <ID> \
//	    --insight-index 1 \
//	    --mode both \
//	    --out /tmp/replay.json
//
// The CLI bypasses ai.SchemaProvider (Qdrant) — it uses a tiny
// warehouse-backed shim instead. Everything else (Bundle, Agent,
// Tools, Prompts, Combine) is the production code path.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/litellm"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/databricks"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/mssql"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/postgres"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/snowflake"
)

const defaultMongoURI = "mongodb://localhost:27017/decisionbox"

type cliFlags struct {
	projectID     string
	discoveryID   string
	insightIndex  int
	insightID     string
	docKind       string
	recIndex      int
	recID         string
	mode          string
	out           string
	mongoURI      string
	wAuthOverride string
	lAuthOverride string
	gcpKeyPath    string
	debug         bool
}

type stderrTracer struct{}

func (stderrTracer) OnRound(mode valmodels.AgentMode, round int, action verifier.ActionKind, info string) {
	if info != "" {
		fmt.Fprintf(os.Stderr, "  [%s r%d] %s\n", mode, round, info)
		return
	}
	fmt.Fprintf(os.Stderr, "  [%s r%d] action=%s\n", mode, round, action)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run() error {
	fl := parseFlags()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(fl.mongoURI))
	if err != nil {
		return fmt.Errorf("mongo connect: %w", err)
	}
	defer func() { _ = mongoClient.Disconnect(context.Background()) }()
	db := mongoClient.Database("decisionbox")

	project, err := loadProject(ctx, db, fl.projectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	discovery, err := loadDiscovery(ctx, db, fl.discoveryID)
	if err != nil {
		return fmt.Errorf("load discovery: %w", err)
	}
	stepByID, err := loadSteps(ctx, db, fl.discoveryID)
	if err != nil {
		return fmt.Errorf("load steps: %w", err)
	}
	fmt.Fprintf(os.Stderr, "loaded project=%q discovery=%s insights=%d recs=%d steps=%d\n",
		project.Name, fl.discoveryID, len(discovery.Insights), len(discovery.Recommendations), len(stepByID))

	llmProvider, err := initLLM(project, fl)
	if err != nil {
		return fmt.Errorf("init LLM: %w", err)
	}
	llmClient, err := ai.New(llmProvider, project.LLM.Model)
	if err != nil {
		return fmt.Errorf("init ai.Client: %w", err)
	}
	llmClient.SetProvenance(fl.projectID, fl.discoveryID, project.LLM.Provider)

	whProvider, err := initWarehouse(project, fl)
	if err != nil {
		return fmt.Errorf("init warehouse: %w", err)
	}
	defer whProvider.Close()

	cfg, runCaps := verifier.LoadConfigFromEnv()
	agent, err := verifier.NewAgent(llmClient, cfg)
	if err != nil {
		return fmt.Errorf("build agent: %w", err)
	}
	if fl.debug {
		agent = agent.WithTracer(stderrTracer{})
	}

	// queryexec.QueryExecutor with no SQL fixer + no retries — the
	// MVP replay path keeps validation deterministic; production
	// orchestrator wires the full self-healing executor.
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   whProvider,
		MaxRetries:  0,
		FilterField: project.Warehouse.FilterField,
		FilterValue: project.Warehouse.FilterValue,
	})

	defaultExecutor := &verifier.DefaultExecutor{
		SchemaProvider:      &warehouseSchemaProvider{wh: whProvider, defaultDataset: project.Warehouse.Datasets[0]},
		QueryExec:           executor,
		StepByID:            stepByID,
		Cfg:                 cfg.Bundle,
		MaxReadStepRowsCall: runCaps.MaxReadStepRowsCall,
	}

	wh := verifier.WarehouseInfo{
		Dialect:     whProvider.SQLDialect(),
		Dataset:     project.Warehouse.Datasets[0],
		FilterField: project.Warehouse.FilterField,
		FilterValue: project.Warehouse.FilterValue,
	}
	disc := verifier.DiscoveryContext{
		ProjectID: fl.projectID,
		RunID:     fl.discoveryID,
		Domain:    project.Domain,
		Language:  project.Language,
	}

	target, err := resolveTarget(fl, discovery)
	if err != nil {
		return err
	}

	report := MVPReport{
		ProjectID:      fl.projectID,
		DiscoveryID:    fl.discoveryID,
		LLM:            project.LLM.Provider + ":" + project.LLM.Model,
		Warehouse:      project.Warehouse.Provider + ":" + project.Warehouse.Datasets[0],
		RefuterEnabled: cfg.RefuterEnabled,
		RunCaps:        runCaps,
		Mode:           fl.mode,
		Targets:        []TargetResult{},
	}

	for _, t := range target {
		fmt.Fprintf(os.Stderr, "\n=== validating %s id=%s ===\n", t.kind, t.id)
		var bundle verifier.Bundle
		switch t.kind {
		case valmodels.DocInsight:
			bundle = verifier.BuildInsightBundle(t.insight, stepByID, wh, disc, cfg.Bundle)
		case valmodels.DocRecommendation:
			byID := indexInsights(discovery.Insights)
			bundle = verifier.BuildRecommendationBundle(t.rec, byID, stepByID, wh, disc, cfg.Bundle)
		}
		tr := TargetResult{
			Kind:          t.kind,
			ID:            t.id,
			Headline:      bundle.Doc.Headline,
			Description:   bundle.Doc.Description,
			SourceSteps:   bundle.Doc.SourceStepIDs,
			AffectedCount: bundle.Doc.AffectedCount,
		}

		if fl.mode == "verify" || fl.mode == "both" {
			v, vErr := agent.Verify(ctx, bundle, defaultExecutor)
			if vErr != nil {
				return fmt.Errorf("verify failed: %w", vErr)
			}
			tr.Verifier = &v
		}
		if (fl.mode == "refute" || fl.mode == "both") && cfg.RefuterEnabled {
			refuterBundle := bundle
			if tr.Verifier != nil && len(tr.Verifier.ClaimsConsidered) > 0 {
				refuterBundle.PriorClaims = append([]string(nil), tr.Verifier.ClaimsConsidered...)
			}
			r, rErr := agent.Refute(ctx, refuterBundle, defaultExecutor)
			if rErr != nil {
				return fmt.Errorf("refute failed: %w", rErr)
			}
			tr.Refuter = &r
		}

		refuterDisabled := !cfg.RefuterEnabled || fl.mode == "verify"
		combined, rd := valmodels.Combine(tr.Verifier, tr.Refuter, refuterDisabled)
		tr.Combined = combined
		tr.RefuterDisabled = rd
		printTargetSummary(os.Stderr, tr)
		report.Targets = append(report.Targets, tr)
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if fl.out != "" {
		if err := os.WriteFile(fl.out, out, 0o600); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		fmt.Fprintf(os.Stderr, "\nreport written to %s\n", fl.out)
	} else {
		_, _ = os.Stdout.Write(out)
		_, _ = os.Stdout.WriteString("\n")
	}
	return nil
}

type target struct {
	kind    valmodels.DocKind
	id      string
	insight *agentmodels.Insight
	rec     *agentmodels.Recommendation
}

func resolveTarget(fl cliFlags, d *DiscoveryDoc) ([]target, error) {
	out := make([]target, 0, 1)
	switch valmodels.DocKind(fl.docKind) {
	case valmodels.DocInsight:
		if fl.insightID != "" {
			for i := range d.Insights {
				if d.Insights[i].ID == fl.insightID {
					out = append(out, target{kind: valmodels.DocInsight, id: fl.insightID, insight: &d.Insights[i]})
					return out, nil
				}
			}
			return nil, fmt.Errorf("insight id %s not found in discovery", fl.insightID)
		}
		if fl.insightIndex < 0 || fl.insightIndex >= len(d.Insights) {
			return nil, fmt.Errorf("insight index %d out of range [0,%d)", fl.insightIndex, len(d.Insights))
		}
		ins := &d.Insights[fl.insightIndex]
		out = append(out, target{kind: valmodels.DocInsight, id: ins.ID, insight: ins})
	case valmodels.DocRecommendation:
		if fl.recID != "" {
			for i := range d.Recommendations {
				if d.Recommendations[i].ID == fl.recID {
					out = append(out, target{kind: valmodels.DocRecommendation, id: fl.recID, rec: &d.Recommendations[i]})
					return out, nil
				}
			}
			return nil, fmt.Errorf("recommendation id %s not found in discovery", fl.recID)
		}
		if fl.recIndex < 0 || fl.recIndex >= len(d.Recommendations) {
			return nil, fmt.Errorf("recommendation index %d out of range [0,%d)", fl.recIndex, len(d.Recommendations))
		}
		rec := &d.Recommendations[fl.recIndex]
		out = append(out, target{kind: valmodels.DocRecommendation, id: rec.ID, rec: rec})
	default:
		return nil, fmt.Errorf("invalid --doc-kind %q (want insight or recommendation)", fl.docKind)
	}
	return out, nil
}

func indexInsights(xs []agentmodels.Insight) map[string]*agentmodels.Insight {
	m := make(map[string]*agentmodels.Insight, len(xs))
	for i := range xs {
		m[xs[i].ID] = &xs[i]
	}
	return m
}

func parseFlags() cliFlags {
	var fl cliFlags
	flag.StringVar(&fl.projectID, "project-id", "", "project ObjectId")
	flag.StringVar(&fl.discoveryID, "discovery-id", "", "discovery ObjectId")
	flag.IntVar(&fl.insightIndex, "insight-index", 0, "0-based insight index (when --doc-kind=insight)")
	flag.StringVar(&fl.insightID, "insight-id", "", "explicit insight ID (overrides --insight-index)")
	flag.StringVar(&fl.docKind, "doc-kind", "insight", "insight | recommendation")
	flag.IntVar(&fl.recIndex, "recommendation-index", 0, "0-based recommendation index (when --doc-kind=recommendation)")
	flag.StringVar(&fl.recID, "recommendation-id", "", "explicit recommendation ID")
	flag.StringVar(&fl.mode, "mode", "both", "verify | refute | both")
	flag.StringVar(&fl.out, "out", "", "path to write JSON report (default: stdout)")
	flag.StringVar(&fl.mongoURI, "mongo-uri", getEnv("MONGO_URI", defaultMongoURI), "Mongo URI")
	flag.StringVar(&fl.wAuthOverride, "warehouse-auth-method", "", "override project.warehouse.config.auth_method (e.g. 'adc')")
	flag.StringVar(&fl.lAuthOverride, "llm-auth-method", "", "override project.llm.config.auth_method (e.g. 'iam_role')")
	flag.StringVar(&fl.gcpKeyPath, "gcp-sa-key", "", "path to a GCP service-account key JSON file (sets warehouse credentials_json)")
	flag.BoolVar(&fl.debug, "debug", false, "print per-round agent trace to stderr")
	flag.Parse()
	if fl.projectID == "" || fl.discoveryID == "" {
		fmt.Fprintln(os.Stderr, "usage: validation-replay --project-id <ID> --discovery-id <ID> [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	return fl
}

func initLLM(project *ProjectDoc, fl cliFlags) (gollm.Provider, error) {
	if project.LLM.Provider == "" {
		return nil, errors.New("project has no LLM provider configured")
	}
	cfg := gollm.ProviderConfig{
		"model":           project.LLM.Model,
		"max_retries":     "3",
		"timeout_seconds": "120",
	}
	for k, v := range project.LLM.Config {
		cfg[k] = v
	}
	if fl.lAuthOverride != "" {
		cfg["auth_method"] = fl.lAuthOverride
	}
	if cfg["auth_method"] == "" {
		switch project.LLM.Provider {
		case "bedrock":
			cfg["auth_method"] = "iam_role"
		case "vertex-ai":
			cfg["auth_method"] = "adc"
		}
	}
	return gollm.NewProvider(project.LLM.Provider, cfg)
}

func initWarehouse(project *ProjectDoc, fl cliFlags) (gowarehouse.Provider, error) {
	if project.Warehouse.Provider == "" {
		return nil, errors.New("project has no warehouse provider configured")
	}
	if len(project.Warehouse.Datasets) == 0 {
		return nil, errors.New("project has no warehouse datasets configured")
	}
	cfg := gowarehouse.ProviderConfig{
		"project_id": project.Warehouse.ProjectID,
		"dataset":    project.Warehouse.Datasets[0],
		"location":   project.Warehouse.Location,
	}
	for k, v := range project.Warehouse.Config {
		cfg[k] = v
	}
	if fl.wAuthOverride != "" {
		cfg["auth_method"] = fl.wAuthOverride
	}
	if fl.gcpKeyPath != "" {
		b, err := os.ReadFile(fl.gcpKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read --gcp-sa-key: %w", err)
		}
		cfg["credentials_json"] = string(b)
		if cfg["auth_method"] == "" {
			cfg["auth_method"] = "sa_key"
		}
	}
	if cfg["auth_method"] == "sa_key" && cfg["credentials_json"] == "" {
		cfg["auth_method"] = "adc"
	}
	return gowarehouse.NewProvider(project.Warehouse.Provider, cfg)
}

func loadProject(ctx context.Context, db *mongo.Database, idHex string) (*ProjectDoc, error) {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return nil, fmt.Errorf("project id not valid hex ObjectId: %w", err)
	}
	var p ProjectDoc
	if err := db.Collection("projects").FindOne(ctx, bson.M{"_id": oid}).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func loadDiscovery(ctx context.Context, db *mongo.Database, idHex string) (*DiscoveryDoc, error) {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return nil, fmt.Errorf("discovery id not valid hex ObjectId: %w", err)
	}
	var d DiscoveryDoc
	if err := db.Collection("discoveries").FindOne(ctx, bson.M{"_id": oid}).Decode(&d); err != nil {
		return nil, err
	}
	d.ID = idHex
	return &d, nil
}

func loadSteps(ctx context.Context, db *mongo.Database, discID string) (map[int]*agentmodels.ExplorationStep, error) {
	cur, err := db.Collection("discovery_exploration_steps").Find(ctx, bson.M{"discovery_id": discID})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make(map[int]*agentmodels.ExplorationStep)
	for cur.Next(ctx) {
		var s agentmodels.ExplorationStep
		if err := cur.Decode(&s); err != nil {
			return nil, err
		}
		s.QueryResult = unwrapAllRows(s.QueryResult)
		cp := s
		out[s.Step] = &cp
	}
	return out, nil
}

// unwrapAllRows normalises BigQuery int64 cell wrapping at load time
// so every downstream consumer sees plain numbers.
func unwrapAllRows(rows []map[string]any) []map[string]any {
	for i := range rows {
		for k, v := range rows[i] {
			rows[i][k] = unwrapBQInt64(v)
		}
	}
	return rows
}

func unwrapBQInt64(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if len(m) != 3 {
		return v
	}
	if _, ok := m["high"]; !ok {
		return v
	}
	if _, ok := m["unsigned"]; !ok {
		return v
	}
	low, ok := m["low"]
	if !ok {
		return v
	}
	switch n := low.(type) {
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return v
}

func getEnv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// MVPReport is the structured output the MVP writes to --out.
type MVPReport struct {
	ProjectID      string          `json:"project_id"`
	DiscoveryID    string          `json:"discovery_id"`
	LLM            string          `json:"llm"`
	Warehouse      string          `json:"warehouse"`
	RefuterEnabled bool            `json:"refuter_enabled"`
	RunCaps        verifier.RunCaps `json:"run_caps"`
	Mode           string          `json:"mode"`
	Targets        []TargetResult  `json:"targets"`
}

type TargetResult struct {
	Kind            valmodels.DocKind            `json:"kind"`
	ID              string                       `json:"id"`
	Headline        string                       `json:"headline,omitempty"`
	Description     string                       `json:"description"`
	SourceSteps     []int                        `json:"source_steps"`
	AffectedCount   int                          `json:"affected_count,omitempty"`
	Verifier        *valmodels.StructuredVerdict `json:"verifier,omitempty"`
	Refuter         *valmodels.StructuredVerdict `json:"refuter,omitempty"`
	Combined        valmodels.Status             `json:"combined"`
	RefuterDisabled bool                         `json:"refuter_disabled"`
}

func printTargetSummary(w *os.File, t TargetResult) {
	fmt.Fprintf(w, "  combined=%s refuter_disabled=%v\n", t.Combined, t.RefuterDisabled)
	if t.Verifier != nil {
		fmt.Fprintf(w, "  verifier: overall=%s claims=%d tokens=in:%d/out:%d reason=%q\n",
			t.Verifier.Overall, len(t.Verifier.ClaimVerdicts), t.Verifier.LLMTokensIn, t.Verifier.LLMTokensOut, t.Verifier.OverallReason)
	}
	if t.Refuter != nil {
		fmt.Fprintf(w, "  refuter:  overall=%s claims=%d tokens=in:%d/out:%d reason=%q\n",
			t.Refuter.Overall, len(t.Refuter.ClaimVerdicts), t.Refuter.LLMTokensIn, t.Refuter.LLMTokensOut, t.Refuter.OverallReason)
	}
}
