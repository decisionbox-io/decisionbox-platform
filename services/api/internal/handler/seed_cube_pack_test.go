package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// cubeSeedSlug is the reference pack written for a metrics-and-dimensions
// source. It exists so pack generation for such a source has an example of its
// own shape to imitate; without one the generator falls back to showing no
// example at all, which is the defined behaviour but a worse one.
const cubeSeedSlug = "digital-analytics"

// referenceExcerptChars is how much of each prompt body a generator is shown.
//
// The number belongs to packgen, in the enterprise repository, which trims
// every reference pack's prompts to a short excerpt so the example teaches
// structure rather than content. It is duplicated here deliberately and with
// its source named: this pack is written to be read through that window, and a
// test that did not assert the window would let the pack drift into saying
// what it needs to say on line 40, where nothing would ever read it.
//
// If packgen changes the value, this test becomes conservative rather than
// wrong — it would assert against a smaller window than the reader has.
const referenceExcerptChars = 400

func loadSeedPack(t *testing.T, slug string) *models.DomainPack {
	t.Helper()
	data, err := seedFS.ReadFile("seed/" + slug + ".json")
	if err != nil {
		t.Fatalf("reading the %s seed: %v", slug, err)
	}
	var portable portableFormat
	if err := json.Unmarshal(data, &portable); err != nil {
		t.Fatalf("parsing the %s seed: %v", slug, err)
	}
	if portable.Format != "decisionbox-domain-pack" {
		t.Fatalf("%s declares format %q", slug, portable.Format)
	}
	return &portable.Pack
}

// promptBodies returns every prompt the pack carries, labelled, since each one
// is trimmed independently.
func promptBodies(pack *models.DomainPack) map[string]string {
	out := map[string]string{
		"base_context":    pack.Prompts.Base.BaseContext,
		"exploration":     pack.Prompts.Base.Exploration,
		"recommendations": pack.Prompts.Base.Recommendations,
	}
	for _, area := range pack.AnalysisAreas.Base {
		out["area:"+area.ID] = area.Prompt
	}
	for cat, areas := range pack.AnalysisAreas.Categories {
		for _, area := range areas {
			out["area:"+cat+"/"+area.ID] = area.Prompt
		}
	}
	return out
}

// TestCubeSeedPack_IsSavable runs the pack through the same contract the API's
// save and import handlers run. A seed pack that would be rejected on save is
// one an operator can never edit and re-save through the product.
func TestCubeSeedPack_IsSavable(t *testing.T) {
	pack := loadSeedPack(t, cubeSeedSlug)
	if err := models.ValidateDomainPack(pack); err != nil {
		t.Errorf("the cube reference pack would be rejected on save: %v", err)
	}
}

// TestCubeSeedPack_DeclaresItsShape is what keeps it out of SQL pack
// generation. Selection filters by shape, and an undeclared shape reads as
// entities — so a cube pack that forgot to say so would be offered as the
// example for every warehouse pack, which is a quality regression with no
// error attached and the reason this pack could not be published earlier.
func TestCubeSeedPack_DeclaresItsShape(t *testing.T) {
	pack := loadSeedPack(t, cubeSeedSlug)
	if got := pack.EffectiveShape(); got != warehouse.ShapeCube {
		t.Errorf("the cube reference pack resolves to shape %q, want %q", got, warehouse.ShapeCube)
	}
	if !pack.IsPublished {
		t.Error("an unpublished pack is not in the pool selection reads, so it can never be the example")
	}
}

// TestCubeSeedPack_CarriesNoSQLContract: the pack teaches by being imitated, so
// a stray SQL placeholder in it teaches a generator to emit one for a source
// that cannot substitute it.
func TestCubeSeedPack_CarriesNoSQLContract(t *testing.T) {
	pack := loadSeedPack(t, cubeSeedSlug)
	for label, body := range promptBodies(pack) {
		for _, tok := range []string{"{{DIALECT}}", "{{DATASET}}", "{{REF:"} {
			if strings.Contains(body, tok) {
				t.Errorf("%s carries %s, which nothing substitutes for a cube source", label, tok)
			}
		}
	}
}

// TestCubeSeedPack_SaysItIsACubeInsideTheExcerptWindow is the assertion this
// pack was actually written against.
//
// A generator sees only the first referenceExcerptChars of each prompt. A pack
// that explains its shape in a section further down is, from the only reader
// that matters, a pack that does not explain its shape at all — and the failure
// is silent, because the pack itself remains perfectly good.
func TestCubeSeedPack_SaysItIsACubeInsideTheExcerptWindow(t *testing.T) {
	// Any one of these establishes that a query here is a selection rather
	// than a statement. Several spellings, because insisting on one phrase
	// would make this a test of wording rather than of substance.
	markers := []string{
		"no tables",
		"metrics and dimensions",
		"a metric and a dimension",
		"break it down by",
		"selection",
	}
	pack := loadSeedPack(t, cubeSeedSlug)
	for label, body := range promptBodies(pack) {
		head := string([]rune(body)[:min(len([]rune(body)), referenceExcerptChars)])
		// Whitespace-normalised before matching: these prompts are hard-wrapped
		// prose, so a marker phrase routinely straddles a line break. Matching
		// the raw text would make this a test of where the paragraph happens to
		// wrap, and it would pass or fail on a reflow that changed nothing.
		flat := strings.ToLower(strings.Join(strings.Fields(head), " "))
		found := false
		for _, m := range markers {
			if strings.Contains(flat, m) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: the first %d characters never say the source is a cube, so a generator reading the excerpt would not learn it:\n%s",
				label, referenceExcerptChars, head)
		}
	}
}

// TestListDomains_DoesNotOfferACubePack closes the loop this pack opens.
//
// The pack must be published — selection reads the published pool, and an
// unpublished pack can never be the example. Published also means listed, and
// the list this endpoint returns is the new-project domain picker. A project's
// pack belongs to its primary data source, which must be able to carry the
// analysis, and no cube can; so offering one would put an option in front of a
// customer that project creation then refuses.
func TestListDomains_DoesNotOfferACubePack(t *testing.T) {
	repo := newMockDomainPackRepo()
	table := testDomainPack("warehouse-domain", "cat")
	cube := testDomainPack("cube-domain", "cat")
	cube.Shape = warehouse.ShapeCube
	repo.add(table)
	repo.add(cube)

	h := NewDomainsHandler(repo)
	req := httptest.NewRequest("GET", "/api/v1/domains", nil)
	w := httptest.NewRecorder()
	h.ListDomains(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// Responses are wrapped in the API envelope, so decode through it rather
	// than asserting on the raw body.
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the response: %v (body: %s)", err, w.Body.String())
	}
	ids := map[string]bool{}
	for _, d := range envelope.Data {
		ids[d.ID] = true
	}
	if ids["cube-domain"] {
		t.Error("the picker offered a cube pack, which project creation refuses for every project")
	}
	// The other half: a filter that dropped everything would also pass the
	// assertion above, and would empty the picker for every customer.
	if !ids["warehouse-domain"] {
		t.Error("the picker dropped a table-shaped pack")
	}
}

// TestListDomains_AnUndeclaredShapeIsStillOffered covers the entire existing
// corpus, none of which carries the field.
func TestListDomains_AnUndeclaredShapeIsStillOffered(t *testing.T) {
	repo := newMockDomainPackRepo()
	legacy := testDomainPack("legacy-domain", "cat")
	if legacy.Shape != "" {
		t.Fatalf("this test is about a pack with no shape, got %q", legacy.Shape)
	}
	repo.add(legacy)

	w := httptest.NewRecorder()
	NewDomainsHandler(repo).ListDomains(w, httptest.NewRequest("GET", "/api/v1/domains", nil))
	if !strings.Contains(w.Body.String(), "legacy-domain") {
		t.Errorf("a pack with no declared shape vanished from the picker: %s", w.Body.String())
	}
}

// jsonFieldNames lists the json tag names a struct declares, ignoring
// encoding options like ",omitempty".
func jsonFieldNames(t *testing.T, v any) []string {
	t.Helper()
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	return out
}

// TestCubeSeedPack_RecommendationExampleMatchesTheModel keeps the pack's output
// example and the struct that parses it from drifting apart.
//
// The example is what a model copies, so a field missing from it is a field
// that arrives empty — silently, since an absent key is not a parse error. It
// is derived from the struct rather than listed here so that ADDING a field to
// the model fails this test until the pack is updated too, which is the drift
// that would otherwise go unnoticed for a release.
func TestCubeSeedPack_RecommendationExampleMatchesTheModel(t *testing.T) {
	// Fields a pack must not teach the model to emit, each for its own reason.
	notThePacksToWrite := map[string]string{
		"id":              "assigned when the recommendation is stored",
		"description_md":  "rendered from description, not written by the model",
		"validation":      "attached by the validation phase after generation",
		"expected_impact": "checked through its own fields below, since it is an object",
	}

	body := loadSeedPack(t, cubeSeedSlug).Prompts.Base.Recommendations
	for _, field := range jsonFieldNames(t, models.Recommendation{}) {
		if _, skip := notThePacksToWrite[field]; skip {
			continue
		}
		if !strings.Contains(body, `"`+field+`"`) {
			t.Errorf("the recommendations example never shows %q, so a model copying it would leave that field empty", field)
		}
	}
	for _, field := range jsonFieldNames(t, models.Impact{}) {
		if !strings.Contains(body, `"`+field+`"`) {
			t.Errorf("the recommendations example never shows expected_impact.%q", field)
		}
	}
}

// TestCubeSeedPack_TeachesTheActionEnvelope: the exploration loop parses one
// JSON action per turn, and a pack that explains only how to REASON leaves the
// model to guess how to ANSWER. The existing table-shaped packs all teach this;
// the cube one has to as well, or a pack generated by imitating it inherits the
// omission.
func TestCubeSeedPack_TeachesTheActionEnvelope(t *testing.T) {
	body := loadSeedPack(t, cubeSeedSlug).Prompts.Base.Exploration
	// search_tables is in this list for a reason that is easy to get wrong, and
	// that I did get wrong: {{SCHEMA_INFO}} is rendered from TABLE schemas
	// (schema_context.buildCatalog), so a cube source contributes nothing to it.
	// The vector index built by SchemaIndexer.buildCatalogIndex is the only
	// runtime surface carrying its metric and dimension names, and search_tables
	// is how a run reaches that index. A cube pack that omits it leaves the
	// model with no way to learn a single name the source will accept.
	for _, key := range []string{`"thinking"`, `"query"`, `"datasource_id"`, `"done"`, `"search_tables"`} {
		if !strings.Contains(body, key) {
			t.Errorf("the exploration prompt never shows %s, so the model is not told how to reply", key)
		}
	}
	// And it must not teach SQL while doing so. The envelope is shared with the
	// warehouse packs; what goes inside `query` is not.
	for _, sql := range []string{"SELECT ", "FROM ", "GROUP BY"} {
		if strings.Contains(body, sql) {
			t.Errorf("the exploration prompt contains %q — the action envelope is shared with SQL packs, its payload is not", sql)
		}
	}
}

// jsonBlock pulls the first fenced ```json block out of a prompt.
func jsonBlock(t *testing.T, body string) string {
	t.Helper()
	const fence = "```json"
	i := strings.Index(body, fence)
	if i < 0 {
		t.Fatal("the prompt carries no fenced json example")
	}
	rest := body[i+len(fence):]
	j := strings.Index(rest, "```")
	if j < 0 {
		t.Fatal("the fenced json example is never closed")
	}
	return rest[:j]
}

// TestCubeSeedPack_RecommendationExampleParsesAsTheModel is the type half of
// the same agreement, and the half a field-name check cannot see.
//
// The example the model copies has to decode into the struct that stores it. A
// string where the model has an int, or a sentence where it has an object,
// leaves that field at its zero value with no error raised anywhere — the
// dashboard renders a priority of 0 and an empty impact, and nothing in the
// pipeline ever says why.
func TestCubeSeedPack_RecommendationExampleParsesAsTheModel(t *testing.T) {
	var envelope struct {
		Recommendations []models.Recommendation `json:"recommendations"`
	}
	raw := jsonBlock(t, loadSeedPack(t, cubeSeedSlug).Prompts.Base.Recommendations)
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("the recommendations example does not decode as the model that stores it: %v", err)
	}
	if len(envelope.Recommendations) == 0 {
		t.Fatal("the example decoded to no recommendations, so it proves nothing")
	}
	// Zero values would decode happily from an example that simply omitted the
	// fields, so check the two that carry a type the wrong shape would break.
	r := envelope.Recommendations[0]
	if r.Priority == 0 {
		t.Error("the example's priority decoded to 0 — it is an integer field, and a string there is silently dropped")
	}
	if r.ExpectedImpact.Metric == "" {
		t.Error("the example's expected_impact decoded empty — it is an object, and a sentence there is silently dropped")
	}
}

// TestCubeSeedPack_UsesOnlyPlaceholdersTheRunSubstitutes catches the class of
// mistake that put a Business Profile section in the exploration prompt.
//
// A prompt is assembled as buildBaseContext(base_context) + exploration, and
// only the base-context half has {{PROFILE}} substituted into it. The
// exploration half then gets {{DATASET}}, {{SCHEMA_INFO}}, {{FILTER*}} and
// {{ANALYSIS_AREAS}} replaced and nothing else — so a {{PROFILE}} written
// there reaches the model as four literal braces. It renders as an empty
// section rather than an error, which is why no other seed pack has one and
// why nothing would have reported it.
func TestCubeSeedPack_UsesOnlyPlaceholdersTheRunSubstitutes(t *testing.T) {
	// Substituted into the exploration half by Orchestrator.buildPrompts.
	substituted := map[string]bool{
		"{{DATASET}}": true, "{{SCHEMA_INFO}}": true, "{{FILTER}}": true,
		"{{FILTER_CONTEXT}}": true, "{{FILTER_RULE}}": true, "{{ANALYSIS_AREAS}}": true,
	}
	body := loadSeedPack(t, cubeSeedSlug).Prompts.Base.Exploration
	for _, part := range strings.Split(body, "{{") {
		i := strings.Index(part, "}}")
		if i < 0 {
			continue
		}
		token := "{{" + part[:i] + "}}"
		if !substituted[token] {
			t.Errorf("the exploration prompt carries %s, which nothing substitutes there — it reaches the model as literal braces", token)
		}
	}
}
