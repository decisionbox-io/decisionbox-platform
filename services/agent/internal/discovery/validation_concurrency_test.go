package discovery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// inFlightLLM answers every verifier / refuter round with a final verdict
// after a short delay, recording the peak number of concurrent calls.
type inFlightLLM struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	delay    time.Duration
}

func (l *inFlightLLM) Chat(_ context.Context, _ string, system string, _ int) (*ai.ChatResult, error) {
	l.mu.Lock()
	l.inFlight++
	if l.inFlight > l.peak {
		l.peak = l.inFlight
	}
	l.mu.Unlock()
	time.Sleep(l.delay)
	l.mu.Lock()
	l.inFlight--
	l.mu.Unlock()
	mode := valmodels.ModeVerifier
	if strings.Contains(system, "REFUTER") {
		mode = valmodels.ModeRefuter
	}
	return &ai.ChatResult{Content: fmt.Sprintf(`{"submit_verdict": {"doc_id":"d","doc_kind":"insight","mode":%q,"claims_considered":["h"],"claim_verdicts":[{"claim_text":"h","is_headline":true,"status":"supported","evidence":{"kind":"step_row","step_id":1,"row":{"x":1}}}],"overall":"supported","overall_reason":""}}`, mode)}, nil
}

type emptyExecutor struct{}

func (emptyExecutor) LookupSchema(context.Context, []string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (emptyExecutor) QueryWarehouse(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (emptyExecutor) ReadStepRows(context.Context, verifier.StepRowsRequest) (map[string]any, error) {
	return map[string]any{}, nil
}

func concurrencyFixture(t *testing.T, concurrency int) (*validationPhase, *inFlightLLM, []models.Insight, map[int]*models.ExplorationStep) {
	t.Helper()
	llm := &inFlightLLM{delay: 20 * time.Millisecond}
	cfg := verifier.DefaultConfig()
	cfg.RefuterEnabled = true
	agent, err := verifier.NewAgent(llm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &validationPhase{
		agent:    agent,
		cfg:      cfg,
		caps:     verifier.RunCaps{MaxInsightsPerRun: 4, MaxRecommendationsPerRun: 15, Concurrency: concurrency},
		executor: emptyExecutor{},
	}
	insights := make([]models.Insight, 6)
	for i := range insights {
		insights[i] = models.Insight{ID: fmt.Sprintf("i%d", i), Name: fmt.Sprintf("finding %d", i),
			AffectedCount: (i + 1) * 10, SourceSteps: []int{1}}
	}
	steps := map[int]*models.ExplorationStep{1: {Step: 1, QueryResult: []map[string]interface{}{{"x": 1}}}}
	return p, llm, insights, steps
}

// TestValidateInsights_Concurrency pins the bounded fan-out: with
// Concurrency 3, items run in parallel (peak > 1, never above 3); the
// results keep affected_count-desc order; and the per-run cap is applied
// exactly as in the sequential path (the 2 least-affected are skipped).
func TestValidateInsights_Concurrency(t *testing.T) {
	p, llm, insights, steps := concurrencyFixture(t, 3)
	results, validated := p.validateInsights(context.Background(), insights, steps, "a", 0)

	if llm.peak < 2 || llm.peak > 3 {
		t.Errorf("peak concurrent LLM calls = %d, want 2..3", llm.peak)
	}
	if validated != 4 || len(results) != 6 {
		t.Fatalf("validated = %d, results = %d; want 4 and 6", validated, len(results))
	}
	wantOrder := []string{"i5", "i4", "i3", "i2", "i1", "i0"}
	for k, r := range results {
		if r.InsightID != wantOrder[k] {
			t.Errorf("results[%d] = %s, want %s (affected_count desc)", k, r.InsightID, wantOrder[k])
		}
	}
	for _, r := range results[:4] {
		if r.Verifier == nil || r.Refuter == nil {
			t.Errorf("%s: validated item missing verdicts", r.InsightID)
		}
	}
	for _, r := range results[4:] {
		if r.Combined != valmodels.StatusSkippedBudgetCap {
			t.Errorf("%s: combined = %s, want skipped_budget_cap", r.InsightID, r.Combined)
		}
	}
	for _, ins := range insights {
		if ins.Validation == nil {
			t.Errorf("%s: Validation not stamped on the insight", ins.ID)
		}
	}
}

// TestValidateInsights_DefaultIsSequential pins that the default keeps
// today's one-at-a-time behaviour.
func TestValidateInsights_DefaultIsSequential(t *testing.T) {
	p, llm, insights, steps := concurrencyFixture(t, 0)
	p.validateInsights(context.Background(), insights, steps, "a", 0)
	if llm.peak != 1 {
		t.Errorf("peak concurrent LLM calls = %d, want 1", llm.peak)
	}
}

func TestValidateRecommendations_Concurrency(t *testing.T) {
	p, llm, insights, steps := concurrencyFixture(t, 3)
	recs := make([]models.Recommendation, 5)
	for i := range recs {
		recs[i] = models.Recommendation{ID: fmt.Sprintf("r%d", i), Title: fmt.Sprintf("rec %d", i), RelatedInsightIDs: []string{"i0"}}
	}
	results := p.validateRecommendations(context.Background(), recs, insights, steps)
	if llm.peak < 2 || llm.peak > 3 {
		t.Errorf("peak concurrent LLM calls = %d, want 2..3", llm.peak)
	}
	for k, r := range results {
		if r.InsightID != recs[k].ID || recs[k].Validation == nil {
			t.Errorf("results[%d] = %s (validation %v), want %s stamped", k, r.InsightID, recs[k].Validation, recs[k].ID)
		}
	}
}
