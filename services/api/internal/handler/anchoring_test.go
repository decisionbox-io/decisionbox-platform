package handler

import (
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
