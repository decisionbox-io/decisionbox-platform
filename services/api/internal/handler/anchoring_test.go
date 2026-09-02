package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// probeSeq keeps each registration unique: the registry is package-global and
// RegisterWithMeta panics on a duplicate name, so fixed slugs would panic on
// the second iteration under `go test -count=2`.
var probeSeq atomic.Int32

func anchoringProbe(t *testing.T, name string, canAnchor bool) string {
	t.Helper()
	slug := fmt.Sprintf("%s_%d", name, probeSeq.Add(1))
	gowarehouse.RegisterWithMeta(slug, func(gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		return nil, fmt.Errorf("probe provider is not constructible")
	}, gowarehouse.ProviderMeta{
		Name:    slug,
		Dialect: "Probe SQL",
		// The registry is process-global, so a probe registered here is visible
		// to every other test in this package — including the invariant that
		// every registered warehouse provider declares pricing. A probe that
		// skipped it would fail that test rather than this one, from a file
		// that has nothing to do with pricing.
		DefaultPricing: &gowarehouse.WarehousePricing{CostModel: "per_query"},
		Capability:     gowarehouse.Capability{CanAnchor: gowarehouse.Anchoring(canAnchor)},
	})
	return slug
}

// A promotion must be REFUSED, not stored. Storing it is the worse failure:
// EffectiveAnchoring applies the provider as a ceiling and ignores the value,
// so the setting reads back as applied while changing nothing — and the user
// believes an enrichment-only source can now carry the project alone.
func TestRejectAnchoringPromotion(t *testing.T) {
	anchor := anchoringProbe(t, "probe_api_anchor", true)
	enrich := anchoringProbe(t, "probe_api_enrich", false)

	tests := []struct {
		name       string
		warehouses []models.WarehouseConfig
		wantOK     bool
	}{
		{
			name:       "no override is always fine",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: enrich}},
			wantOK:     true,
		},
		{
			name:       "demoting an anchoring provider is legal",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: anchor, Anchoring: gowarehouse.Anchoring(false)}},
			wantOK:     true,
		},
		{
			name:       "demoting a non-anchoring provider is legal (a no-op, but true)",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: enrich, Anchoring: gowarehouse.Anchoring(false)}},
			wantOK:     true,
		},
		{
			name:       "keeping an anchoring provider anchoring is legal",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: anchor, Anchoring: gowarehouse.Anchoring(true)}},
			wantOK:     true,
		},
		{
			name:       "promoting a non-anchoring provider is refused",
			warehouses: []models.WarehouseConfig{{ID: "wh_ga", Provider: enrich, Anchoring: gowarehouse.Anchoring(true)}},
			wantOK:     false,
		},
		{
			name: "one bad entry among good ones is still refused",
			warehouses: []models.WarehouseConfig{
				{ID: "wh_1", Provider: anchor},
				{ID: "wh_ga", Provider: enrich, Anchoring: gowarehouse.Anchoring(true)},
			},
			wantOK: false,
		},
		{
			name:       "an entry with no provider is skipped rather than rejected",
			warehouses: []models.WarehouseConfig{{ID: "wh_blank", Anchoring: gowarehouse.Anchoring(true)}},
			wantOK:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			got := rejectAnchoringPromotion(rec, tc.warehouses)

			if got != tc.wantOK {
				t.Fatalf("rejectAnchoringPromotion = %v, want %v", got, tc.wantOK)
			}
			if tc.wantOK {
				if rec.Code != http.StatusOK {
					t.Errorf("wrote status %d on an allowed request", rec.Code)
				}
				return
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			// The message must name the offending datasource and say which
			// direction is legal — a bare "invalid" would leave the user with
			// no way to tell what to change.
			body := rec.Body.String()
			if !strings.Contains(body, "wh_ga") {
				t.Errorf("error body does not name the offending datasource: %s", body)
			}
			if !strings.Contains(body, "never on") {
				t.Errorf("error body does not state that only demotion is legal: %s", body)
			}
		})
	}
}

// A project whose datasources can all only enrich has nothing to carry an
// analysis. It reaches the agent looking perfectly healthy and produces a
// confident restatement of what those sources already report, with no error
// anyone would connect to the cause — so it is refused where the message can
// still name the fix.
func TestRejectUnanchoredProject(t *testing.T) {
	anchor := anchoringProbe(t, "probe_unanchored_anchor", true)
	enrich := anchoringProbe(t, "probe_unanchored_enrich", false)

	tests := []struct {
		name       string
		warehouses []models.WarehouseConfig
		wantOK     bool
	}{
		{
			// How every project starts. Refusing this would make the product
			// unusable in order to say something true.
			name:       "no datasources yet",
			warehouses: nil,
			wantOK:     true,
		},
		{
			name:       "one system of record",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: anchor}},
			wantOK:     true,
		},
		{
			name:       "an enrichment source alongside a system of record",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: anchor}, {ID: "ds_2", Provider: enrich}},
			wantOK:     true,
		},
		{
			name:       "an enrichment source on its own",
			warehouses: []models.WarehouseConfig{{ID: "ds_1", Provider: enrich}},
			wantOK:     false,
		},
		{
			name:       "several enrichment sources are still not an anchor",
			warehouses: []models.WarehouseConfig{{ID: "ds_1", Provider: enrich}, {ID: "ds_2", Provider: enrich}},
			wantOK:     false,
		},
		{
			// The override demotes a provider that could have anchored, so the
			// project is left with nothing — the refusal must read the
			// resolved value, not the provider's declaration.
			name:       "the only source was demoted by hand",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: anchor, Anchoring: gowarehouse.Anchoring(false)}},
			wantOK:     false,
		},
		{
			// An unregistered provider resolves to anchoring: a binary that
			// has not linked the provider cannot construct the datasource
			// either, so the project is broken in a more obvious way long
			// before anchoring is consulted.
			name:       "an unregistered provider is not treated as unanchored",
			warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: "not_registered_anywhere"}},
			wantOK:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if got := rejectUnanchoredProject(w, tc.warehouses); got != tc.wantOK {
				t.Fatalf("rejectUnanchoredProject() = %v, want %v", got, tc.wantOK)
			}
			if tc.wantOK {
				if w.Code != http.StatusOK {
					t.Errorf("an accepted set wrote status %d", w.Code)
				}
				return
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
			// The refusal is only useful if it says what to do about it.
			if body := w.Body.String(); !strings.Contains(body, "anchoring") {
				t.Errorf("the refusal does not name the fix: %s", body)
			}
		})
	}
}

// The two HTTP routes that can leave a project with nothing to carry it. The
// helper above is correct in isolation and still lets both through if it is
// not called, so these assert the refusal where a client actually meets it.
func TestProjectRoutes_RefuseLeavingAProjectWithNothingToCarryIt(t *testing.T) {
	enrich := anchoringProbe(t, "probe_route_enrich", false)
	anchor := anchoringProbe(t, "probe_route_anchor", true)

	newHandler := func() (*ProjectsHandler, *mockProjectRepo) {
		projRepo := newMockProjectRepo()
		packRepo := newMockDomainPackRepo()
		packRepo.add(testDomainPack("gaming", "match3"))
		return NewProjectsHandler(projRepo, packRepo), projRepo
	}

	create := func(t *testing.T, provider string) *httptest.ResponseRecorder {
		t.Helper()
		h, _ := newHandler()
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

	t.Run("create with only an enrichment source", func(t *testing.T) {
		w := create(t, enrich)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "carry an analysis") {
			t.Errorf("the refusal does not say why: %s", w.Body.String())
		}
	})

	t.Run("create with a system of record", func(t *testing.T) {
		if w := create(t, anchor); w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
		}
	})

	// Swapping a single-warehouse project's only datasource for an enrichment
	// source reaches the same end state the create path refuses, one edit
	// later — and through a different route.
	t.Run("settings edit swaps the only datasource for an enrichment source", func(t *testing.T) {
		h, projRepo := newHandler()
		p := &models.Project{
			Name: "p", Domain: "gaming", Category: "match3",
			Warehouse: models.WarehouseConfig{Provider: anchor, Datasets: []string{"d1"}},
		}
		_ = projRepo.Create(context.Background(), p)

		body := fmt.Sprintf(`{"warehouse": {"provider": %q, "datasets": ["d1"]}}`, enrich)
		req := httptest.NewRequest("PUT", "/api/v1/projects/"+p.ID, strings.NewReader(body))
		req.SetPathValue("id", p.ID)
		w := httptest.NewRecorder()
		h.Update(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
		}
		if got := projRepo.projects[p.ID]; got != nil && got.Warehouse.Provider == enrich {
			t.Error("the refused datasource was persisted anyway")
		}
	})
}
