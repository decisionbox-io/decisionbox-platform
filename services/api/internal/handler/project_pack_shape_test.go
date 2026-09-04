package handler

import (
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

// TestPackShapeMismatch_NoDatasourceIsNotAMismatch: nothing has been chosen
// for the pack to disagree with, and the anchoring guards own that case. A
// mismatch reported here would refuse it with the wrong reason.
func TestPackShapeMismatch_NoDatasourceIsNotAMismatch(t *testing.T) {
	pack := &models.DomainPack{Slug: "analytics", Shape: gowarehouse.ShapeCube}
	if msg := packShapeMismatch(pack, models.WarehouseConfig{}); msg != "" {
		t.Errorf("a project with no data source should not be a mismatch: %s", msg)
	}
}
