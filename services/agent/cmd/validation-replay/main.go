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
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"os"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/litellm"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"   // registers "aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/azure" // registers "azure"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"   // registers "gcp"
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

	// The project's LLM key, resolved as the agent resolves it: the
	// llm-credentials secret, else LLM_API_KEY. Bedrock / Vertex projects
	// authenticate by role and need neither.
	// Resolve the two validation layers through llmlayer, so the replay runs
	// the models the RUN would have used — including a project that points
	// the verifier and the refuter at different entries.
	secretProvider := openSecretProvider(db)
	layerCfg := project.llmLayerConfig()

	verifierSpec, err := layerCfg.SpecUnchecked(llmlayer.LayerValidationVerifier)
	if err != nil {
		return fmt.Errorf("resolve the verifier layer: %w", err)
	}
	refuterSpec, err := layerCfg.SpecUnchecked(llmlayer.LayerValidationRefuter)
	if err != nil {
		return fmt.Errorf("resolve the refuter layer: %w", err)
	}

	// Neither client is built for a mode that will not call it: building a
	// provider reads a credential and opens a connection, so a broken
	// override aborts a replay that was never going to use it. Gating only
	// the refuter still let `--mode=refute` die on missing VERIFIER
	// credentials with a working refuter configured. Discovery, the
	// standalone validator and ask-serve all gate their layer lists the
	// same way.
	//
	// The refuter shares the verifier's client when both layers resolve to
	// the same target AND ask for the same reasoning, so an unmixed project
	// still opens one connection. Reasoning is deliberately not part of
	// Target — it is a per-REQUEST setting and does not change the
	// connection — but it IS carried by the client, so two layers that
	// differ by it need two clients or the refuter would send the
	// verifier's setting and the replay would stop reproducing the run.
	cfg, runCaps := verifier.LoadConfigFromEnv()
	verifierWillRun, refuterWillRun := replayLayers(fl.mode, cfg.RefuterEnabled)

	var verifierClient, refuterClient *ai.Client
	var verifierProv, refuterProv *countingProvider
	var callCounters []*countingProvider
	if verifierWillRun {
		c, calls, cerr := replayClient(ctx, secretProvider, fl, verifierSpec)
		if cerr != nil {
			return fmt.Errorf("init verifier LLM: %w", cerr)
		}
		verifierClient, verifierProv = c, calls
		callCounters = append(callCounters, calls)
	}
	switch {
	case !refuterWillRun:
	case verifierClient != nil && sameReplayClient(verifierSpec, refuterSpec):
		refuterClient, refuterProv = verifierClient, verifierProv
	default:
		rc, refuterCalls, rerr := replayClient(ctx, secretProvider, fl, refuterSpec)
		if rerr != nil {
			return fmt.Errorf("init refuter LLM: %w", rerr)
		}
		refuterClient, refuterProv = rc, refuterCalls
		callCounters = append(callCounters, refuterCalls)
	}

	whProvider, err := initWarehouse(project, fl)
	if err != nil {
		return fmt.Errorf("init warehouse: %w", err)
	}
	defer whProvider.Close()

	// Resolved exactly as RunDiscovery resolves it: each half's ceiling comes
	// from the model that half runs on, with the deployment override clamped
	// down to it. A replay that sends a budget production would not have sent
	// is not reproducing the run — a layer capped below the deployment value
	// gets its calls rejected here and returns `unverifiable` where
	// production returned a verdict, and a replay that substituted the
	// no-information fallback would ask for more than production ever did.
	verifierModelCap, verifierLiveReasoning := replayModelCaps(ctx, verifierProv, verifierSpec)
	refuterModelCap := 0
	refuterLiveReasoning := false
	if cfg.RefuterEnabled {
		refuterModelCap, refuterLiveReasoning = replayModelCaps(ctx, refuterProv, refuterSpec)
	}
	cfg.ApplyModelOutputCaps(verifierModelCap, refuterModelCap)
	// Live detection only ever widens, exactly as buildLayerSet applies it:
	// an uncatalogued gateway alias that reports reasoning makes production
	// request an effort even with the project toggle off, and a replay that
	// did not would send a different request than the run it is reproducing.
	if verifierLiveReasoning && verifierClient != nil {
		verifierClient.SetReasoning(true)
	}
	if refuterLiveReasoning && refuterClient != nil {
		refuterClient.SetReasoning(true)
	}

	// A typed nil in the map would satisfy llmFor's non-nil check and be
	// called, so only the clients that were actually built go in — and the
	// fallback is whichever of them exists.
	byMode := map[valmodels.AgentMode]verifier.LLMClient{}
	fallback := verifier.LLMClient(nil)
	if verifierClient != nil {
		byMode[valmodels.ModeVerifier] = verifierClient
		fallback = verifierClient
	}
	if refuterClient != nil {
		byMode[valmodels.ModeRefuter] = refuterClient
		if fallback == nil {
			fallback = refuterClient
		}
	}
	agent, err := verifier.NewAgentPerMode(byMode, fallback, cfg)
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
	if everyCallFailedAcross(callCounters) {
		return errors.New("every LLM call failed, so the verdicts above are not verdicts; check the project's LLM provider credentials (its secret, or the environment fallback for the default entry)")
	}
	return nil
}

// replayModelOutputCap resolves the model's own output cap the way production
// does: the endpoint's live answer when it gives one, else the layer's
// catalogue ceiling.
//
// The ceiling alone is not production's answer. resolveModelBudget prefers
// the live ModelInfoResolver reading over the catalogue, so an uncatalogued
// gateway model reporting 1024 made production ask for 1024 while the replay
// still asked for the validation default — and the replay returned a
// rejection, or `unverifiable`, for a verdict production had produced. A
// tool whose whole job is reproducing a run cannot differ on the budget.
//
// It answers the model's cap, not the final budget: the operator override and
// the clamp against it are Config.ApplyModelOutputCaps's job, the same way
// production applies them.
func replayModelCaps(ctx context.Context, p *countingProvider, spec llmlayer.LayerSpec) (outputCap int, reasoning bool) {
	// Start from the CATALOG cap, then clamp by the layer's override — the
	// order resolveModelBudget uses. LayerSpec.OutputCeiling returns the
	// override in preference to the catalog, which is right for a caller
	// clamping a product budget down but wrong here: a 8,000 override on a
	// model whose catalog cap is 4,096 would have the replay ask for 8,000
	// where production asks for 4,096, and collect a rejection for a run that
	// succeeded.
	base := gollm.GetMaxOutputTokens(spec.Provider, spec.Model)
	live, liveReasoning := liveModelCaps(ctx, p, spec.Model)
	if live > 0 {
		base = live
	}
	return gollm.ClampMaxTokens(base, gollm.MaxOutputOverride(gollm.ProviderConfig(spec.Config))), liveReasoning
}

// liveModelCaps asks the endpoint what it will actually accept and whether it
// reasons, or zero values when it cannot say. Best-effort and bounded: a replay must not hang on a
// gateway that will not answer.
//
// countingProvider embeds the interface, so the capability has to be read
// off the provider it wraps — a type assertion on the wrapper only sees
// gollm.Provider's own methods and would always miss.
func liveModelCaps(ctx context.Context, p *countingProvider, model string) (outputCap int, reasoning bool) {
	if p == nil {
		return 0, false
	}
	r, ok := p.Provider.(gollm.ModelInfoResolver)
	if !ok {
		return 0, false
	}
	lctx, cancel := context.WithTimeout(ctx, replayModelInfoTimeout)
	defer cancel()
	caps, err := r.ResolveModelInfo(lctx, model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live model-info lookup failed for %s: %v (using the catalogue ceiling)\n", model, err)
		return 0, false
	}
	return caps.MaxOutputTokens, caps.Reasoning
}

// replayModelInfoTimeout bounds the live lookup above, matching the agent's
// own modelInfoResolveTimeout.
const replayModelInfoTimeout = 8 * time.Second

// replayLayers reports which validation layers this replay will actually
// call, which is what decides whether their clients are worth building.
//
// The pair is derived in one place because the two gates are not symmetric:
// the refuter also answers to a deployment switch, and reading that switch
// at only one of the two decision points is how a verifier-only gate
// shipped.
func replayLayers(mode string, refuterEnabled bool) (verifier, refuter bool) {
	return mode != "refute", refuterEnabled && mode != "verify"
}

// openSecretProvider opens the project secret store the agent reads
// (SECRET_PROVIDER, default mongodb in the same database). Nil when it
// cannot be opened — the caller then falls back to the environment.
func openSecretProvider(db *mongo.Database) gosecrets.Provider {
	cfg := gosecrets.LoadConfig()
	var (
		sp  gosecrets.Provider
		err error
	)
	if cfg.Provider == "" || cfg.Provider == "mongodb" {
		sp, err = mongoSecrets.NewMongoProvider(db.Collection("secrets"), cfg.Namespace, cfg.EncryptionKey)
	} else {
		sp, err = gosecrets.NewProvider(cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: secret store unavailable (%v); using the environment only\n", err)
		return nil
	}
	return sp
}

// countingProvider counts the chat calls that returned a response and the
// ones that failed, so the replay can tell "every call failed" (a missing
// key, an unreachable endpoint) from a response without usage accounting.
type countingProvider struct {
	gollm.Provider
	ok, failed atomic.Int64
}

func (p *countingProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	resp, err := p.Provider.Chat(ctx, req)
	if err != nil {
		p.failed.Add(1)
	} else {
		p.ok.Add(1)
	}
	return resp, err
}

// everyCallFailed reports whether calls were made and none succeeded.
func (p *countingProvider) everyCallFailed() bool {
	return p.ok.Load() == 0 && p.failed.Load() > 0
}

// everyCallFailedAcross reports whether calls were made across every counter
// and none succeeded. With the verifier and the refuter on different models
// there are two counters, and "no verdict is a verdict" only holds when
// NEITHER model ever answered.
func everyCallFailedAcross(counters []*countingProvider) bool {
	var ok, failed int64
	for _, c := range counters {
		ok += c.ok.Load()
		failed += c.failed.Load()
	}
	return ok == 0 && failed > 0
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

// replayClient builds one layer's provider + client for the replay, counting
// its calls.
//
// It goes through llmlayer.BuildProvider rather than assembling a config
// here, which is what keeps the replay honest: the credential chain, the merge
// order and the prompt-cache resolution are the production ones, so a replay
// cannot resolve a model or a setting differently from the run it is
// reproducing. The duplicated prompt-cache rule this file used to carry is
// gone with it.
func replayClient(ctx context.Context, secretProvider gosecrets.Provider, fl cliFlags, spec llmlayer.LayerSpec) (*ai.Client, *countingProvider, error) {
	if spec.Provider == "" {
		return nil, nil, errors.New("project has no LLM provider configured")
	}
	// The replay's auth override, and the cloud defaults the CLI applies when
	// a project document carries none.
	cfg := make(map[string]string, len(spec.Config)+1)
	for k, v := range spec.Config {
		cfg[k] = v
	}
	if fl.lAuthOverride != "" {
		cfg["auth_method"] = fl.lAuthOverride
	}
	if cfg["auth_method"] == "" {
		switch spec.Provider {
		case "bedrock":
			cfg["auth_method"] = "iam_role"
		case "vertex-ai":
			cfg["auth_method"] = "adc"
		}
	}
	spec.Config = cfg

	cred, credErr := llmlayer.ResolveCredential(ctx, secretProvider, fl.projectID, spec, llmlayer.BuildOptions{})
	if credErr != nil {
		fmt.Fprintf(os.Stderr, "warning: reading the %s layer's credential: %v\n", spec.Layer, credErr)
	}
	fmt.Fprintf(os.Stderr, "layer %s: provider_id=%s provider=%s model=%s credential source: %s\n",
		spec.Layer, spec.ProviderID, spec.Provider, spec.Model, cred.Source)

	provider, err := gollm.NewProvider(spec.Provider, llmlayer.ProviderConfig(spec, cred, llmlayer.BuildOptions{
		MaxRetries:     3,
		TimeoutSeconds: 120,
	}))
	if err != nil {
		return nil, nil, err
	}
	calls := &countingProvider{Provider: provider}
	client, err := ai.New(calls, spec.Model)
	if err != nil {
		return nil, nil, fmt.Errorf("init ai.Client: %w", err)
	}
	client.SetProvenance(fl.projectID, fl.discoveryID, spec.Provider)
	// Reasoning is requested PER CALL, so a layer that opted in needs it set
	// on the client — the config map does not carry it to the request.
	// Without this a replay sent the default effort while the production
	// client sent "on", and a reasoning-capable provider (Ollama reads it to
	// set `think`) returned different verdicts under supposedly identical
	// configuration. A replay that does not reproduce the run is worse than
	// no replay.
	client.SetReasoning(spec.Reasoning)
	return client, calls, nil
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
	ProjectID      string           `json:"project_id"`
	DiscoveryID    string           `json:"discovery_id"`
	LLM            string           `json:"llm"`
	Warehouse      string           `json:"warehouse"`
	RefuterEnabled bool             `json:"refuter_enabled"`
	RunCaps        verifier.RunCaps `json:"run_caps"`
	Mode           string           `json:"mode"`
	Targets        []TargetResult   `json:"targets"`
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

// sameReplayClient reports whether two layers can share one ai.Client.
//
// The provider connection is shared on Target alone, but a Client also
// carries the reasoning setting — which Target deliberately excludes,
// because reasoning is requested per call and does not change the
// connection. Two layers differing only by it are one connection and two
// clients, and production builds them that way.
func sameReplayClient(a, b llmlayer.LayerSpec) bool {
	return a.Target() == b.Target() && a.Reasoning == b.Reasoning
}
