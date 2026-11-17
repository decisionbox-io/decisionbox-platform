package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// Round 1 review: the project-wide category weights were a field a client
// could send and the update silently dropped.
//
// `category_weights` is how a deployment says "compliance is three times
// anything else for us", across every datasource. It could be supplied at
// creation and then never changed or cleared again, while the route answered
// 200 — the shape of silent failure the merge exists to avoid.

func projectWeightsHandler(t *testing.T) (*ProjectsHandler, *mockProjectRepo) {
	t.Helper()
	projRepo := newMockProjectRepo()
	packRepo := newMockDomainPackRepo()
	packRepo.add(testDomainPack("gaming", "match3"))
	return NewProjectsHandler(projRepo, packRepo), projRepo
}

func putProjectWeights(t *testing.T, h *ProjectsHandler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.Update(w, req)
	return w
}

func seedWeightedProject(t *testing.T, repo *mockProjectRepo, weights map[string]float64) *models.Project {
	t.Helper()
	p := &models.Project{
		Name: "p", Domain: "gaming", Category: "match3",
		Warehouse:       models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"d1"}},
		CategoryWeights: weights,
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p
}

func TestProjectUpdateMergesCategoryWeights(t *testing.T) {
	t.Run("sets them on a project that had none", func(t *testing.T) {
		h, repo := projectWeightsHandler(t)
		p := seedWeightedProject(t, repo, nil)

		w := putProjectWeights(t, h, p.ID, `{"category_weights":{"compliance":3,"revenue_leak":2}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		got := repo.projects[p.ID].CategoryWeights
		if got["compliance"] != 3 || got["revenue_leak"] != 2 {
			t.Errorf("CategoryWeights = %v, want the submitted weights", got)
		}
	})

	t.Run("changes one that was already set", func(t *testing.T) {
		h, repo := projectWeightsHandler(t)
		p := seedWeightedProject(t, repo, map[string]float64{"compliance": 3})

		if w := putProjectWeights(t, h, p.ID, `{"category_weights":{"compliance":1.5}}`); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		if got := repo.projects[p.ID].CategoryWeights["compliance"]; got != 1.5 {
			t.Errorf("compliance = %v, want 1.5", got)
		}
	})

	// An empty-but-present map is the client clearing every weight, which is
	// a different instruction from not sending the field.
	t.Run("clears them when sent empty", func(t *testing.T) {
		h, repo := projectWeightsHandler(t)
		p := seedWeightedProject(t, repo, map[string]float64{"compliance": 3})

		if w := putProjectWeights(t, h, p.ID, `{"category_weights":{}}`); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		if got := repo.projects[p.ID].CategoryWeights; len(got) != 0 {
			t.Errorf("CategoryWeights = %v, want cleared", got)
		}
	})

	t.Run("leaves them alone when the field is absent", func(t *testing.T) {
		h, repo := projectWeightsHandler(t)
		p := seedWeightedProject(t, repo, map[string]float64{"compliance": 3})

		if w := putProjectWeights(t, h, p.ID, `{"name":"renamed"}`); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		if got := repo.projects[p.ID].CategoryWeights["compliance"]; got != 3 {
			t.Errorf("compliance = %v, want the stored 3 — an absent field is not a clear", got)
		}
	})
}

// TestProjectUpdateRefusesAnIllegalWeight -- the engine ignores a weight
// outside (0, 3], so accepting one would answer 200 to a client whose setting
// will never take effect.
func TestProjectUpdateRefusesAnIllegalWeight(t *testing.T) {
	for _, weight := range []string{"0", "-1", "9"} {
		t.Run("weight "+weight, func(t *testing.T) {
			h, repo := projectWeightsHandler(t)
			p := seedWeightedProject(t, repo, nil)

			body := fmt.Sprintf(`{"category_weights":{"compliance":%s}}`, weight)
			w := putProjectWeights(t, h, p.ID, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if got := repo.projects[p.ID].CategoryWeights; len(got) != 0 {
				t.Errorf("the refused weight was persisted: %v", got)
			}
		})
	}
}

// TestProjectUpdateRefusesAnInvalidPromptsRiskConfig -- the same route also
// carries `prompts`, so the area mapping and the override are checked here
// too. The two halves are validated together, because an area's mapping is
// read against the project's own taxonomy.
func TestProjectUpdateRefusesAnInvalidPromptsRiskConfig(t *testing.T) {
	h, repo := projectWeightsHandler(t)
	p := seedWeightedProject(t, repo, nil)

	body := `{"prompts":{"exploration":"e","recommendations":"r","base_context":"b",` +
		`"analysis_areas":{"churn":{"name":"Churn","risk_categories":["invented_here"]}}}}`
	w := putProjectWeights(t, h, p.ID, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invented_here") {
		t.Errorf("the response does not name the offending id: %s", w.Body.String())
	}
}

// TestProjectUpdateAcceptsAValidRiskConfiguration -- the positive case, and
// the compat case: a project with none of this is unaffected by the checks.
func TestProjectUpdateAcceptsAValidRiskConfiguration(t *testing.T) {
	h, repo := projectWeightsHandler(t)
	p := seedWeightedProject(t, repo, nil)

	body := `{"category_weights":{"compliance":2.5},` +
		`"prompts":{"exploration":"e","recommendations":"r","base_context":"b",` +
		`"risk_taxonomy":[{"id":"compliance","label":"Compliance","weight":3}],` +
		`"analysis_areas":{"churn":{"name":"Churn","risk_categories":["compliance"],"severity_weight":1.5}}}}`
	if w := putProjectWeights(t, h, p.ID, body); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	stored := repo.projects[p.ID]
	if stored.CategoryWeights["compliance"] != 2.5 {
		t.Errorf("CategoryWeights = %v", stored.CategoryWeights)
	}
	if stored.Prompts == nil || len(stored.Prompts.RiskTaxonomy) != 1 {
		t.Errorf("the taxonomy did not persist: %+v", stored.Prompts)
	}

	t.Run("a project with no risk configuration still saves", func(t *testing.T) {
		h, repo := projectWeightsHandler(t)
		bare := seedWeightedProject(t, repo, nil)
		if w := putProjectWeights(t, h, bare.ID, `{"name":"renamed"}`); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})
}

// --- Round 2 review -------------------------------------------------------

// TestProjectCreateRefusesAnIllegalWeight.
//
// Create used to accept one, which was worse than it sounds: the UPDATE route
// checks the same rules unconditionally, so a project created with
// `category_weights: {"compliance": 9}` had EVERY later edit rejected with a
// 400 about a field the edit did not touch — including an unrelated rename.
// A refusal at create is one a client can act on; a refusal three screens
// later is not.
func TestProjectCreateRefusesAnIllegalWeight(t *testing.T) {
	h, repo := projectWeightsHandler(t)

	body := `{"name":"p","domain":"gaming","category":"match3",
		"warehouse":{"provider":"bigquery","datasets":["d1"]},
		"llm":{"provider":"claude","model":"claude-sonnet-4-6"},
		"category_weights":{"compliance":9}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if len(repo.projects) != 0 {
		t.Error("the project was created despite the refusal")
	}
}

// TestProjectCreateAcceptsAValidConfigurationAndStaysEditable is the other
// half: a project created with legal weights must not then be refused an
// ordinary edit.
func TestProjectCreateAcceptsAValidConfigurationAndStaysEditable(t *testing.T) {
	h, repo := projectWeightsHandler(t)

	body := `{"name":"p","domain":"gaming","category":"match3",
		"warehouse":{"provider":"bigquery","datasets":["d1"]},
		"llm":{"provider":"claude","model":"claude-sonnet-4-6"},
		"category_weights":{"compliance":2.5}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	var created string
	for id := range repo.projects {
		created = id
	}
	if created == "" {
		t.Fatal("the project was not created")
	}
	if got := putProjectWeights(t, h, created, `{"name":"renamed"}`); got.Code != http.StatusOK {
		t.Errorf("an unrelated rename was refused: %d %s", got.Code, got.Body.String())
	}
}

// TestProjectCreateValidatesTheSeededConfiguration -- a seeded project's
// taxonomy and area mappings come from the pack, which is why the check runs
// after seeding rather than on the request body.
func TestProjectCreateValidatesTheSeededConfiguration(t *testing.T) {
	projRepo := newMockProjectRepo()
	packRepo := newMockDomainPackRepo()
	pack := testDomainPack("gaming", "match3")
	// A pack nothing could have saved, which is the only way this state is
	// reachable — the seed is copied verbatim, so a project created from it
	// would otherwise carry the same illegal weight.
	pack.RiskTaxonomy = []models.PackRiskCategory{{ID: "compliance", Label: "Compliance", Weight: 9}}
	pack.AnalysisAreas.Base[0].RiskCategories = []string{"compliance"}
	packRepo.add(pack)
	h := NewProjectsHandler(projRepo, packRepo)

	body := `{"name":"p","domain":"gaming","category":"match3",
		"warehouse":{"provider":"bigquery","datasets":["d1"]},
		"llm":{"provider":"claude","model":"claude-sonnet-4-6"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (the seeded taxonomy is invalid); body = %s", w.Code, w.Body.String())
	}
}
