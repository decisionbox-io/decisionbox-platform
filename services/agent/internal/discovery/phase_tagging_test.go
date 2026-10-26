package discovery

import (
	"context"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// phaseRecorder captures the phase stamped on every LLM usage event.
type phaseRecorder struct {
	policy.Checker
	phases []string
}

func (r *phaseRecorder) ObserveLLMTokens(_ context.Context, _ string, ev policy.LLMUsageEvent) {
	r.phases = append(r.phases, ev.Phase)
}

// TestRunPhaseQuestions_TagsLLMCallsWithPhase pins that the questions hop's
// LLM calls carry phase "questions" on the usage event, so cost per phase
// is attributable.
func TestRunPhaseQuestions_TagsLLMCallsWithPhase(t *testing.T) {
	t.Setenv("DISCOVERY_QUESTIONS_ENABLED", "true")
	rec := &phaseRecorder{Checker: policy.NewNoopChecker()}
	policy.RegisterChecker(rec)
	t.Cleanup(func() { policy.RegisterChecker(policy.NewNoopChecker()) })

	resp := `{"questions":[{"question":"Does code 4 mean closed?","rationale":"opaque enum","linked_target":{"type":"insight","id":"a"},"answer_type":"boolean"}]}`
	client, _ := stubClient(t, resp, nil)
	o := newTestOrchestrator(client, &fakeQuestionRepo{})

	o.RunPhaseQuestions(context.Background(), &models.DiscoveryResult{ID: "disc-1",
		Insights: []models.Insight{insightWith("a", "opaque", valmodels.StatusUnverifiable, 0.9)}})

	if len(rec.phases) == 0 {
		t.Fatal("expected at least one LLM usage event")
	}
	for _, p := range rec.phases {
		if p != models.PhaseQuestions {
			t.Errorf("usage event phase = %q, want %q", p, models.PhaseQuestions)
		}
	}
}
