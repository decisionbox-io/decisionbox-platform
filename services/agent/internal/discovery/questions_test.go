package discovery

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- test doubles ---

// qStubLLM is a minimal gollm.Provider returning canned content (or an error).
type qStubLLM struct {
	content string
	err     error
	calls   int
}

func (s *qStubLLM) Chat(_ context.Context, _ gollm.ChatRequest) (*gollm.ChatResponse, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &gollm.ChatResponse{Content: s.content}, nil
}
func (s *qStubLLM) Validate(_ context.Context) error { return nil }

func stubClient(t *testing.T, content string, err error) (*ai.Client, *qStubLLM) {
	t.Helper()
	prov := &qStubLLM{content: content, err: err}
	c, cerr := ai.New(prov, "test-model")
	if cerr != nil {
		t.Fatalf("ai.New: %v", cerr)
	}
	return c, prov
}

// fakeQuestionRepo records inserts and serves a canned existing list.
type fakeQuestionRepo struct {
	inserted  []commonmodels.DiscoveryQuestion
	existing  []commonmodels.DiscoveryQuestion
	insertErr error
}

func (f *fakeQuestionRepo) Insert(_ context.Context, qs []commonmodels.DiscoveryQuestion) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, qs...)
	return nil
}
func (f *fakeQuestionRepo) ListForProject(_ context.Context, _ string, _ ...string) ([]commonmodels.DiscoveryQuestion, error) {
	return f.existing, nil
}

// newTestOrchestrator builds an orchestrator with the questions phase wired to
// the given LLM + repo, and a cheap model budget so resolveModelBudget doesn't
// hit the catalog.
func newTestOrchestrator(client *ai.Client, repo questionPersister) *Orchestrator {
	return &Orchestrator{
		aiClient:                   client,
		questionRepo:               repo,
		clarifyingQuestionsEnabled: true,
		projectID:                  "proj-1",
		runID:                      "run-1",
		datasets:                   []string{"ds"},
		llmInputWindow:             200000,
		llmOutputCap:               4000,
	}
}

func insightWith(id, name string, combined valmodels.Status, conf float64) models.Insight {
	in := models.Insight{ID: id, Name: name, AnalysisArea: "churn", Description: "desc", Confidence: conf}
	if combined != "" {
		in.Validation = &models.InsightValidation{Combined: combined}
	}
	return in
}

// --- buildUncertaintyDigest ---

// A 0-confidence finding (least-confident, or one the model gave no confidence
// for) is below any positive threshold and must count as uncertain.
func TestBuildUncertaintyDigest_ZeroConfidenceIsUncertain(t *testing.T) {
	insights := []models.Insight{insightWith("z", "no confidence", valmodels.StatusSupported, 0)}
	if items := buildUncertaintyDigest(insights, nil, nil, 0.5); len(items) != 1 {
		t.Fatalf("zero-confidence digest = %d items, want 1", len(items))
	}
}

func TestBuildUncertaintyDigest_CleanRunIsEmpty(t *testing.T) {
	insights := []models.Insight{
		insightWith("a", "clean", valmodels.StatusSupported, 0.9),
		insightWith("b", "confident", "", 0.95),
	}
	if items := buildUncertaintyDigest(insights, nil, nil, 0.5); len(items) != 0 {
		t.Fatalf("clean run digest = %d items, want 0", len(items))
	}
}

func TestBuildUncertaintyDigest_CapturesUncertainty(t *testing.T) {
	insights := []models.Insight{
		insightWith("unv", "unverifiable one", valmodels.StatusUnverifiable, 0.9),
		insightWith("part", "partial one", valmodels.StatusPartial, 0.9),
		insightWith("low", "low conf", valmodels.StatusSupported, 0.30),
		insightWith("ok", "fine", valmodels.StatusSupported, 0.9),
	}
	recs := []models.Recommendation{{ID: "r1", Title: "rec", Confidence: 0.2}}
	analysis := []models.AnalysisStep{{AreaID: "levels", AreaName: "Levels", Error: "boom"}}

	items := buildUncertaintyDigest(insights, recs, analysis, 0.5)
	// 3 uncertain insights + 1 low-conf rec + 1 failed area = 5
	if len(items) != 5 {
		t.Fatalf("digest = %d items, want 5: %+v", len(items), items)
	}
	// The clean insight must not appear.
	for _, it := range items {
		if it.TargetID == "ok" {
			t.Fatalf("clean insight leaked into digest")
		}
	}
}

// --- parseQuestions ---

func TestParseQuestions_Shapes(t *testing.T) {
	envelope := `{"questions":[{"question":"q1","rationale":"r","linked_target":{"type":"insight","id":"a"},"answer_type":"boolean"}]}`
	if got, raw, err := parseQuestions(envelope); err != nil || len(got) != 1 || raw != 1 {
		t.Fatalf("envelope: got %d raw %d err %v", len(got), raw, err)
	}
	bare := `[{"question":"q","rationale":"r","linked_target":{"type":"table","id":"d.t"},"answer_type":"free_text"}]`
	if got, raw, err := parseQuestions(bare); err != nil || len(got) != 1 || raw != 1 {
		t.Fatalf("bare array: got %d raw %d err %v", len(got), raw, err)
	}
	if got, raw, err := parseQuestions(`{"questions":[]}`); err != nil || len(got) != 0 || raw != 0 {
		t.Fatalf("empty: got %d raw %d err %v (want 0,0,nil)", len(got), raw, err)
	}
	fenced := "```json\n{\"questions\":[]}\n```"
	if _, _, err := parseQuestions(fenced); err != nil {
		t.Fatalf("fenced empty should parse: %v", err)
	}
	if _, _, err := parseQuestions(`{"items":[]}`); err == nil {
		t.Fatalf("missing questions key should error")
	}
}

func TestParseQuestions_SkipsMalformedItem(t *testing.T) {
	// second item is a bare string, not an object — dropped, rest kept, raw=2.
	resp := `{"questions":[{"question":"q","rationale":"r","linked_target":{"type":"insight","id":"a"},"answer_type":"boolean"},"nope"]}`
	got, raw, err := parseQuestions(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || raw != 2 {
		t.Fatalf("got %d kept, raw %d (want 1, 2)", len(got), raw)
	}
}

// A non-empty array of non-object items → kept empty but rawCount>0, so the
// caller must NOT treat it as a legitimately empty result.
func TestParseQuestions_NonObjectItemsReportRawCount(t *testing.T) {
	got, raw, err := parseQuestions(`{"questions":["Does code 4 mean closed?"]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 || raw != 1 {
		t.Fatalf("got %d kept, raw %d (want 0, 1)", len(got), raw)
	}
}

// generateQuestions must retry (not silently return empty) when the model emits
// a non-empty array whose items are all non-objects.
func TestGenerateQuestions_RetriesOnNonObjectItems(t *testing.T) {
	client, prov := stubClient(t, `{"questions":["a string, not an object"]}`, nil)
	o := newTestOrchestrator(client, &fakeQuestionRepo{})
	items := []uncertaintyItem{{TargetType: "insight", TargetID: "a", Reason: "x"}}
	if _, err := o.generateQuestions(context.Background(), items, nil, map[string]bool{"insight:a": true}, 5); err == nil {
		t.Fatalf("expected error after non-object items across retries")
	}
	if prov.calls < 2 {
		t.Fatalf("expected the repair prompt to fire (>=2 calls), got %d", prov.calls)
	}
}

// --- postProcessQuestions ---

func mkParsed(q, rationale, ttype, tid, atype string, opts ...[2]string) parsedQuestion {
	var pq parsedQuestion
	pq.Question = q
	pq.Rationale = rationale
	pq.LinkedTarget.Type = ttype
	pq.LinkedTarget.ID = tid
	pq.AnswerType = atype
	for _, o := range opts {
		pq.Options = append(pq.Options, struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		}{ID: o[0], Label: o[1]})
	}
	return pq
}

func TestPostProcess_GroundingAndRationale(t *testing.T) {
	valid := map[string]bool{"insight:a": true}
	parsed := []parsedQuestion{
		mkParsed("has rationale", "why", "insight", "a", "boolean"),
		mkParsed("no rationale", "", "insight", "a", "boolean"),      // dropped: no rationale
		mkParsed("bad target", "why", "insight", "ghost", "boolean"), // dropped: unresolved target
		mkParsed("bad type", "why", "insight", "a", "banana"),        // dropped: invalid answer type
	}
	out := postProcessQuestions(parsed, valid, nil, 5)
	if len(out) != 1 || out[0].Question != "has rationale" {
		t.Fatalf("grounding filter kept %d: %+v", len(out), out)
	}
}

func TestPostProcess_ChoiceGetsOtherEscape(t *testing.T) {
	valid := map[string]bool{"insight:a": true}
	parsed := []parsedQuestion{
		mkParsed("pick", "why", "insight", "a", "single_choice", [2]string{"o1", "One"}, [2]string{"o2", "Two"}),
	}
	out := postProcessQuestions(parsed, valid, nil, 5)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	opts := out[0].Options
	if len(opts) != 3 || opts[len(opts)-1].ID != commonmodels.OtherOptionID {
		t.Fatalf("choice must end with the __other escape, got %+v", opts)
	}
}

func TestPostProcess_EmptyChoiceDegradesToFreeText(t *testing.T) {
	valid := map[string]bool{"insight:a": true}
	parsed := []parsedQuestion{mkParsed("pick", "why", "insight", "a", "multi_choice")}
	out := postProcessQuestions(parsed, valid, nil, 5)
	if len(out) != 1 || out[0].AnswerType != commonmodels.AnswerTypeFreeText || len(out[0].Options) != 0 {
		t.Fatalf("empty choice should degrade to free_text w/o options: %+v", out)
	}
}

func TestPostProcess_BooleanCarriesNoOptions(t *testing.T) {
	valid := map[string]bool{"insight:a": true}
	// even if the model attaches options to a boolean, we strip them.
	parsed := []parsedQuestion{mkParsed("closed?", "why", "insight", "a", "boolean", [2]string{"x", "X"})}
	out := postProcessQuestions(parsed, valid, nil, 5)
	if len(out) != 1 || len(out[0].Options) != 0 {
		t.Fatalf("boolean must carry no options: %+v", out)
	}
}

func TestPostProcess_DedupAndCap(t *testing.T) {
	valid := map[string]bool{"insight:a": true, "insight:b": true, "insight:c": true}
	existingKey := commonmodels.NormalizedQuestionKey("already asked")
	parsed := []parsedQuestion{
		mkParsed("already asked", "why", "insight", "a", "boolean"), // dropped: dup vs existing
		mkParsed("fresh one", "why", "insight", "b", "boolean"),
		mkParsed("fresh one", "why", "insight", "b", "boolean"), // dropped: dup within batch
		mkParsed("another", "why", "insight", "c", "boolean"),
	}
	out := postProcessQuestions(parsed, valid, map[string]bool{existingKey: true}, 1)
	if len(out) != 1 {
		t.Fatalf("cap N=1 → want 1, got %d: %+v", len(out), out)
	}
	if out[0].Question != "fresh one" {
		t.Fatalf("dedup vs existing failed; first kept = %q", out[0].Question)
	}
}

// --- generateQuestions (full LLM path via stub) ---

func TestGenerateQuestions_StubProvider(t *testing.T) {
	resp := `{"questions":[{"question":"Does code 4 mean closed?","rationale":"opaque enum","linked_target":{"type":"insight","id":"a"},"answer_type":"boolean"}]}`
	client, prov := stubClient(t, resp, nil)
	o := newTestOrchestrator(client, &fakeQuestionRepo{})
	items := []uncertaintyItem{{TargetType: "insight", TargetID: "a", Label: "x", Reason: "unverifiable"}}
	got, err := o.generateQuestions(context.Background(), items, nil, map[string]bool{"insight:a": true}, 5)
	if err != nil {
		t.Fatalf("generateQuestions: %v", err)
	}
	if len(got) != 1 || prov.calls != 1 {
		t.Fatalf("got %d questions in %d calls", len(got), prov.calls)
	}
}

func TestGenerateQuestions_LLMError(t *testing.T) {
	client, _ := stubClient(t, "", context.DeadlineExceeded)
	o := newTestOrchestrator(client, &fakeQuestionRepo{})
	items := []uncertaintyItem{{TargetType: "insight", TargetID: "a", Reason: "x"}}
	if _, err := o.generateQuestions(context.Background(), items, nil, map[string]bool{"insight:a": true}, 5); err == nil {
		t.Fatalf("expected error from LLM failure")
	}
}

// A parseable response whose items all fail grounding must trigger the repair
// prompt (a second call), not silently yield zero questions.
func TestGenerateQuestions_RetriesOnAllDropped(t *testing.T) {
	resp := `{"questions":[{"question":"q","rationale":"","linked_target":{"type":"insight","id":"ghost"},"answer_type":"boolean"}]}`
	client, prov := stubClient(t, resp, nil)
	o := newTestOrchestrator(client, &fakeQuestionRepo{})
	items := []uncertaintyItem{{TargetType: "insight", TargetID: "a", Reason: "x"}}
	_, err := o.generateQuestions(context.Background(), items, nil, map[string]bool{"insight:a": true}, 5)
	if err == nil {
		t.Fatalf("expected an error after all items dropped across retries")
	}
	if prov.calls < 2 {
		t.Fatalf("expected the repair prompt to fire (>=2 calls), got %d", prov.calls)
	}
}

// A legitimately empty response returns zero WITHOUT retrying.
func TestGenerateQuestions_EmptyNoRetry(t *testing.T) {
	client, prov := stubClient(t, `{"questions":[]}`, nil)
	o := newTestOrchestrator(client, &fakeQuestionRepo{})
	items := []uncertaintyItem{{TargetType: "insight", TargetID: "a", Reason: "x"}}
	got, err := o.generateQuestions(context.Background(), items, nil, map[string]bool{"insight:a": true}, 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty response: got %d err %v (want 0, nil)", len(got), err)
	}
	if prov.calls != 1 {
		t.Fatalf("empty response must not retry, got %d calls", prov.calls)
	}
}

// --- runPhaseQuestions (gating + best-effort) ---

func TestRunPhaseQuestions_ToggleOff_NoWork(t *testing.T) {
	t.Setenv("DISCOVERY_QUESTIONS_ENABLED", "true")
	client, prov := stubClient(t, `{"questions":[]}`, nil)
	repo := &fakeQuestionRepo{}
	o := newTestOrchestrator(client, repo)
	o.clarifyingQuestionsEnabled = false // Layer B off

	o.runPhaseQuestions(context.Background(), &models.DiscoveryResult{ID: "d1"},
		[]models.Insight{insightWith("a", "x", valmodels.StatusUnverifiable, 0.9)}, nil, nil)

	if prov.calls != 0 || len(repo.inserted) != 0 {
		t.Fatalf("toggle off must do no work: calls=%d inserted=%d", prov.calls, len(repo.inserted))
	}
}

func TestRunPhaseQuestions_NoUncertainty_ShortCircuits(t *testing.T) {
	t.Setenv("DISCOVERY_QUESTIONS_ENABLED", "true")
	client, prov := stubClient(t, `{"questions":[]}`, nil)
	repo := &fakeQuestionRepo{}
	o := newTestOrchestrator(client, repo)

	// clean, confident insight → empty digest → no LLM call.
	o.runPhaseQuestions(context.Background(), &models.DiscoveryResult{ID: "d1"},
		[]models.Insight{insightWith("a", "clean", valmodels.StatusSupported, 0.95)}, nil, nil)

	if prov.calls != 0 {
		t.Fatalf("clean run must not call the LLM, got %d calls", prov.calls)
	}
}

func TestRunPhaseQuestions_HappyPath_Inserts(t *testing.T) {
	t.Setenv("DISCOVERY_QUESTIONS_ENABLED", "true")
	resp := `{"questions":[{"question":"Does code 4 mean closed?","rationale":"opaque enum","linked_target":{"type":"insight","id":"a"},"answer_type":"boolean"}]}`
	client, _ := stubClient(t, resp, nil)
	repo := &fakeQuestionRepo{}
	o := newTestOrchestrator(client, repo)

	o.runPhaseQuestions(context.Background(), &models.DiscoveryResult{ID: "disc-1"},
		[]models.Insight{insightWith("a", "opaque", valmodels.StatusUnverifiable, 0.9)}, nil, nil)

	if len(repo.inserted) != 1 {
		t.Fatalf("want 1 inserted question, got %d", len(repo.inserted))
	}
	q := repo.inserted[0]
	if q.ProjectID != "proj-1" || q.RunID != "run-1" || q.DiscoveryID != "disc-1" {
		t.Fatalf("ids not stamped: %+v", q)
	}
	if q.Status != commonmodels.DiscoveryQuestionStatusPending || q.ID == "" || q.NormalizedKey == "" {
		t.Fatalf("status/id/key not stamped: %+v", q)
	}
}

func TestRunPhaseQuestions_InsertError_Swallowed(t *testing.T) {
	t.Setenv("DISCOVERY_QUESTIONS_ENABLED", "true")
	resp := `{"questions":[{"question":"q?","rationale":"why","linked_target":{"type":"insight","id":"a"},"answer_type":"boolean"}]}`
	client, _ := stubClient(t, resp, nil)
	repo := &fakeQuestionRepo{insertErr: context.DeadlineExceeded}
	o := newTestOrchestrator(client, repo)

	// Must not panic / propagate — best-effort.
	o.runPhaseQuestions(context.Background(), &models.DiscoveryResult{ID: "d1"},
		[]models.Insight{insightWith("a", "opaque", valmodels.StatusUnverifiable, 0.9)}, nil, nil)
}

// --- schema shape ---

func TestQuestionsSchema_Shape(t *testing.T) {
	schema := questionsResponseSchema()
	props, _ := schema["properties"].(map[string]interface{})
	if _, ok := props["questions"]; !ok {
		t.Fatalf("schema missing top-level questions property")
	}
	// The item schema must require the grounding fields.
	arr := props["questions"].(map[string]interface{})
	item := arr["items"].(map[string]interface{})
	req, _ := item["required"].([]interface{})
	joined := ""
	for _, r := range req {
		joined += r.(string) + ","
	}
	for _, want := range []string{"question", "rationale", "linked_target", "answer_type"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("item schema must require %q; required=%s", want, joined)
		}
	}
}
