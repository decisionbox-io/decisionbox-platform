package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// shapeProbe registers a warehouse provider of a given shape that can carry a
// project on its own, so a create using it is refused for shape or accepted,
// never refused for having nothing to anchor on.
func shapeProbe(t *testing.T, name string, shape gowarehouse.SourceShape) string {
	t.Helper()
	slug := fmt.Sprintf("%s_%d", name, probeSeq.Add(1))
	gowarehouse.RegisterWithMeta(slug, func(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		return nil, fmt.Errorf("probe provider is not constructible")
	}, gowarehouse.ProviderMeta{
		Name:           slug,
		Dialect:        "Probe SQL",
		DefaultPricing: &gowarehouse.WarehousePricing{CostModel: "per_query"},
		Capability: gowarehouse.Capability{
			CanAnchor: gowarehouse.Anchoring(true),
			Shape:     shape,
		},
	})
	return slug
}

// createWithPack posts a single-datasource create using the given provider and
// a pack of the given shape, and returns the recorder.
func createWithPack(t *testing.T, provider string, packShape gowarehouse.SourceShape) *httptest.ResponseRecorder {
	t.Helper()
	pack := testDomainPack("gaming", "match3")
	pack.Shape = packShape

	packRepo := newMockDomainPackRepo()
	packRepo.add(pack)
	h := NewProjectsHandler(newMockProjectRepo(), packRepo)

	body := fmt.Sprintf(`{
		"name": "p", "domain": "gaming", "category": "match3",
		"warehouse": {"provider": %q, "datasets": ["d1"]},
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}
	}`, provider)
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	return w
}

// TestCreate_RefusesACubePackOnATableProject is the hole that opens the moment
// a cube pack can be saved at all.
//
// The domain list the picker reads is every published pack, and a generated
// pack is published — so the first cube pack in the corpus is offered to a
// customer creating a BigQuery project. Its prompts tell the model to choose
// metrics and dimensions against a source that has tables, which produces a
// discovery run that fails without erroring.
func TestCreate_RefusesACubePackOnATableProject(t *testing.T) {
	tables := shapeProbe(t, "probe_shape_tables", gowarehouse.ShapeEntities)
	w := createWithPack(t, tables, gowarehouse.ShapeCube)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	// The refusal has to name both halves, or the customer cannot tell whether
	// the pack or the data source is the thing they picked wrongly.
	body := w.Body.String()
	for _, want := range []string{string(gowarehouse.ShapeCube), string(gowarehouse.ShapeEntities), "gaming"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}
}

// TestCreate_RefusesATablePackOnACubeProject is the same pairing the other way
// round. It is the less likely direction — a cube cannot normally carry a
// project — but the rule is about the pairing, not about which half is odd.
func TestCreate_RefusesATablePackOnACubeProject(t *testing.T) {
	cube := shapeProbe(t, "probe_shape_cube", gowarehouse.ShapeCube)
	if w := createWithPack(t, cube, gowarehouse.ShapeEntities); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestCreate_AcceptsAMatchingPair pins that the guard is a shape check and not
// a ban on cube packs. Both matched pairings must go through, or the check has
// simply moved the blocker somewhere new.
func TestCreate_AcceptsAMatchingPair(t *testing.T) {
	for _, shape := range []gowarehouse.SourceShape{gowarehouse.ShapeEntities, gowarehouse.ShapeCube} {
		provider := shapeProbe(t, "probe_shape_match", shape)
		if w := createWithPack(t, provider, shape); w.Code != http.StatusCreated {
			t.Errorf("shape %q: status = %d, want 201; body: %s", shape, w.Code, w.Body.String())
		}
	}
}

// TestCreate_AnUndeclaredShapeIsTableShaped covers the entire existing corpus.
// No pack written before shape was recorded carries the field, and every one
// of them targets tables — resolving that to "no shape" would refuse every
// project create in the product.
func TestCreate_AnUndeclaredShapeIsTableShaped(t *testing.T) {
	tables := shapeProbe(t, "probe_shape_legacy", gowarehouse.ShapeEntities)
	if w := createWithPack(t, tables, ""); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

// TestPackShapeMismatch_UnknownProviderKeepsTheCheck pins the direction the
// helper fails in. A provider this build cannot resolve is read as
// table-shaped — the default applied everywhere else — so an unregistered
// spelling refuses a cube pack rather than waving it through.
func TestPackShapeMismatch_UnknownProviderKeepsTheCheck(t *testing.T) {
	pack := &models.DomainPack{Slug: "analytics", Shape: gowarehouse.ShapeCube}
	primary := models.WarehouseConfig{Provider: "not-registered-anywhere"}
	if msg := packShapeMismatch(pack, primary); msg == "" {
		t.Error("an unresolvable provider should keep the check, not skip it")
	}
}

// TestPackShapeMismatch_NoDatasourceStillChecksTheShape replaces an earlier
// test that asserted the opposite, and the reason it was wrong is worth
// keeping.
//
// "Nothing has been chosen to disagree with" sounds right and is not: the pack
// has to agree with whatever datasource the project ends up with, that
// datasource must be able to carry the analysis, and no cube can. Waving the
// pack through here creates a project holding a cube pack that the
// settings-edit guard then refuses to give any anchoring datasource to —
// unusable and unrepairable through the API, built out of two guards
// disagreeing about the empty case rather than out of either being wrong
// alone.
func TestPackShapeMismatch_NoDatasourceStillChecksTheShape(t *testing.T) {
	cube := &models.DomainPack{Slug: "analytics", Shape: gowarehouse.ShapeCube}
	msg := packShapeMismatch(cube, models.WarehouseConfig{})
	if msg == "" {
		t.Fatal("a cube pack was accepted for a project with no data source, which can never become a valid pairing")
	}
	// And the refusal must not claim a data source it cannot see.
	if strings.Contains(msg, "this project's data source is") {
		t.Errorf("the refusal describes a data source the project does not have: %s", msg)
	}

	// The ordinary case must still pass, or no project could be created before
	// its warehouse is connected — which is how the product is set up.
	table := &models.DomainPack{Slug: "gaming"}
	if msg := packShapeMismatch(table, models.WarehouseConfig{}); msg != "" {
		t.Errorf("a table-shaped pack was refused for a project with no data source yet: %s", msg)
	}
}

// TestCreate_RefusesACubePackBeforeAnyDatasourceExists drives it through the
// route, because the helper being right is not the same as the route calling
// it on a body with no warehouse in it.
func TestCreate_RefusesACubePackBeforeAnyDatasourceExists(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Shape = gowarehouse.ShapeCube
	packRepo := newMockDomainPackRepo()
	packRepo.add(pack)
	h := NewProjectsHandler(newMockProjectRepo(), packRepo)

	body := `{"name": "p", "domain": "gaming", "category": "match3",
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestCreate_AllowsATablePackBeforeAnyDatasourceExists is the other half, and
// the one that would break the product if the guard above overreached.
func TestCreate_AllowsATablePackBeforeAnyDatasourceExists(t *testing.T) {
	packRepo := newMockDomainPackRepo()
	packRepo.add(testDomainPack("gaming", "match3"))
	h := NewProjectsHandler(newMockProjectRepo(), packRepo)

	body := `{"name": "p", "domain": "gaming", "category": "match3",
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

// TestSettingsEdit_RefusesAPackTheNewDatasourceDoesNotMatch closes the gap the
// create-time check cannot see.
//
// A project may be created before it has a data source — that is how the
// product is set up — and its pack is seeded then, with nothing to disagree
// with. The pairing only becomes checkable when the data source arrives, which
// for a single-datasource project is this settings edit.
func TestSettingsEdit_RefusesAPackTheNewDatasourceDoesNotMatch(t *testing.T) {
	tables := shapeProbe(t, "probe_edit_tables", gowarehouse.ShapeEntities)
	cube := shapeProbe(t, "probe_edit_cube", gowarehouse.ShapeCube)

	edit := func(t *testing.T, packShape gowarehouse.SourceShape, provider string) *httptest.ResponseRecorder {
		t.Helper()
		pack := testDomainPack("gaming", "match3")
		pack.Shape = packShape
		packRepo := newMockDomainPackRepo()
		packRepo.add(pack)

		projRepo := newMockProjectRepo()
		h := NewProjectsHandler(projRepo, packRepo)

		// Created with no data source, exactly as the blank-project flow does.
		p := &models.Project{Name: "p", Domain: "gaming", Category: "match3"}
		if err := projRepo.Create(context.Background(), p); err != nil {
			t.Fatalf("seeding the project: %v", err)
		}

		body := fmt.Sprintf(`{"warehouse": {"provider": %q, "datasets": ["d1"]}}`, provider)
		req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
		req.SetPathValue("id", p.ID)
		w := httptest.NewRecorder()
		h.Update(w, req)
		return w
	}

	t.Run("cube pack, table data source", func(t *testing.T) {
		w := edit(t, gowarehouse.ShapeCube, tables)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), string(gowarehouse.ShapeCube)) {
			t.Errorf("the refusal does not name the pack's shape: %s", w.Body.String())
		}
	})

	t.Run("table pack, cube data source", func(t *testing.T) {
		if w := edit(t, gowarehouse.ShapeEntities, cube); w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
		}
	})

	// The ordinary edit every existing project makes. A guard that refused this
	// would break connecting a warehouse to any project in the product.
	t.Run("table pack, table data source", func(t *testing.T) {
		if w := edit(t, "", tables); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
	})

	t.Run("cube pack, cube data source", func(t *testing.T) {
		if w := edit(t, gowarehouse.ShapeCube, cube); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
	})
}

// TestSettingsEdit_AMissingPackDoesNotBlockTheEdit pins the direction this
// guard fails in, which is the opposite of the one in packShapeMismatch.
//
// The pack may have been deleted or renamed since the project was created.
// Refusing an unrelated settings edit over that would be a worse outcome than
// the mismatch being guarded against, and the customer would have no way to
// act on it.
func TestSettingsEdit_AMissingPackDoesNotBlockTheEdit(t *testing.T) {
	tables := shapeProbe(t, "probe_edit_nopack", gowarehouse.ShapeEntities)

	projRepo := newMockProjectRepo()
	h := NewProjectsHandler(projRepo, newMockDomainPackRepo()) // empty corpus
	p := &models.Project{Name: "p", Domain: "deleted-since", Category: "match3"}
	if err := projRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("seeding the project: %v", err)
	}

	body := fmt.Sprintf(`{"warehouse": {"provider": %q, "datasets": ["d1"]}}`, tables)
	req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Update(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestCreate_CustomPromptsDoNotSkipTheShapeCheck.
//
// The check used to live inside the `p.Prompts == nil` seeding branch, so a
// request that supplied its own prompts never looked the pack up and the whole
// refusal was skippable by sending a `prompts` field. Custom prompts do not
// make a project's domain compatible with its data source; they only stop the
// pack being copied into it.
func TestCreate_CustomPromptsDoNotSkipTheShapeCheck(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Shape = gowarehouse.ShapeCube
	packRepo := newMockDomainPackRepo()
	packRepo.add(pack)
	h := NewProjectsHandler(newMockProjectRepo(), packRepo)

	body := `{"name": "p", "domain": "gaming", "category": "match3",
		"prompts": {"base_context": "mine", "exploration": "mine", "recommendations": "mine"},
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestCreate_CustomPromptsWithAnUnknownDomainStillPass pins the behaviour that
// must NOT change alongside it. This route has never looked a domain up for a
// client that brought its own prompts, so rejecting an unknown one now would
// be a second change riding along with the fix above — and would break any
// caller using a domain string the pack corpus does not have.
func TestCreate_CustomPromptsWithAnUnknownDomainStillPass(t *testing.T) {
	h := NewProjectsHandler(newMockProjectRepo(), newMockDomainPackRepo())
	body := `{"name": "p", "domain": "no-such-pack", "category": "any",
		"prompts": {"base_context": "mine", "exploration": "mine", "recommendations": "mine"},
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

// TestCreate_SeedingStillRefusesAnUnknownDomain is the other side of that
// switch: a request with no prompts still depends on the pack existing.
func TestCreate_SeedingStillRefusesAnUnknownDomain(t *testing.T) {
	h := NewProjectsHandler(newMockProjectRepo(), newMockDomainPackRepo())
	body := `{"name": "p", "domain": "no-such-pack", "category": "any",
		"llm": {"provider": "claude", "model": "claude-sonnet-4-6"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
